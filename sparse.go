// sparse.go - Android sparse image format converter
//
// Android sparse image format reference:
//   https://source.android.com/devices/bootloader/images
//
// File header (28 bytes, little-endian):
//   uint32 magic       0xed26ff3a
//   uint16 major       0x0001
//   uint16 minor       0x0000
//   uint16 file_header_size   28
//   uint16 chunk_header_size  12
//   uint32 block_size         (e.g. 4096)
//   uint32 total_blocks       (in output image)
//   uint32 total_chunks       (number of chunks)
//   uint32 image_checksum     (crc32 of output image, 0 if unknown)
//
// Chunk header (12 bytes, little-endian):
//   uint16 chunk_type
//   uint16 reserved
//   uint32 chunk_blocks       (number of output blocks)
//   uint32 total_size         (bytes of input data including header)
//
// Chunk types:
//   0xCAC1 CHUNK_TYPE_RAW      - raw data follows
//   0xCAC2 CHUNK_TYPE_FILL     - 4 bytes of fill value follow
//   0xCAC3 CHUNK_TYPE_DONT_CARE - skip N blocks (no data)
//   0xCAC4 CHUNK_TYPE_CRC32    - 4 bytes crc follow

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

const (
	SparseMagic       uint32 = 0xed26ff3a
	SparseHeaderSize  uint16 = 32
	SparseChunkSize   uint16 = 16
	BlockSize                = 4096
	ChunkTypeRaw      uint16 = 0xCAC1
	ChunkTypeFill     uint16 = 0xCAC2
	ChunkTypeDontCare uint16 = 0xCAC3
	ChunkTypeCRC32    uint16 = 0xCAC4
)

// rawToSparse converts a raw binary image to Android sparse format.
// The input is assumed to be a contiguous raw block that should be
// written verbatim to the partition. We split it into 1MB raw chunks
// to keep each chunk header small.
//
// outputSize specifies the size of the output image (which determines
// total_blocks in the sparse header). If 0, defaults to actual data size.
// If larger than data, we'll pad with a DONT_CARE chunk for the rest
// (which tells the flasher to skip those blocks without writing).
//
// Note: a real sparse converter would scan for zero blocks and use
// FILL/DONT_CARE chunks throughout, but for our purposes (bootloader.ubi,
// cp.bin, dsp.bin, etc.) the whole image is non-sparse so we just
// wrap it as raw chunks.
// writeSparseChunk 追加一个 chunk 头（ASR 格式：16字节，含额外4字节偏移量）
func writeSparseChunk(out []byte, chunkType uint16, chunkBlocks uint32, totalSize uint32, offsetBlocks uint32) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, chunkType)
	binary.Write(&b, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&b, binary.LittleEndian, chunkBlocks)
	binary.Write(&b, binary.LittleEndian, totalSize)
	binary.Write(&b, binary.LittleEndian, offsetBlocks)
	return append(out, b.Bytes()...)
}

// rawToSparse converts a raw binary image to ASR sparse format (32-byte header,
// 16-byte chunk header). The flasher requires this exact format.
// outputSize, if non-zero, is the partition size; data is only block-aligned
// (not padded to outputSize).
func rawToSparse(data []byte, outputSize uint32) ([]byte, error) {
	return rawToSparseAtOffset(data, outputSize, 0)
}

// rawToSparseAtOffset converts raw data at a given block offset into a sparse
// image. It prepends a DONT_CARE chunk to skip the offset blocks, so the data
// is written at the correct position in the partition. Used for segmented
// downloads of large partitions (e.g. cp.bin) where each segment is an
// independent sparse image located by its offset.
//
// outputSize is the full partition size (used for total_blocks in the header,
// which the flasher uses as the partition-size reference). The segment data is
// only block-aligned, never padded to the full partition, so the produced
// sparse image stays small (data + headers).
func rawToSparseAtOffset(data []byte, outputSize uint32, offsetBlocks uint32) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty input data")
	}
	// Block-align the segment data (pad to block boundary only, NOT to outputSize)
	dataBlocks := uint32((len(data) + BlockSize - 1) / BlockSize)
	paddedLen := int(dataBlocks) * BlockSize
	if paddedLen > len(data) {
		padded := make([]byte, paddedLen)
		copy(padded, data)
		data = padded
	}

	// Partition total blocks: full partition size for header (flasher reference)
	partBlocks := uint32(0)
	if outputSize > 0 {
		partBlocks = uint32((outputSize + BlockSize - 1) / BlockSize)
	}
	if partBlocks < dataBlocks+offsetBlocks {
		partBlocks = dataBlocks + offsetBlocks
	}
	tailBlocks := uint32(0)
	if partBlocks > offsetBlocks+dataBlocks {
		tailBlocks = partBlocks - offsetBlocks - dataBlocks
	}

	// Chunks: [DONT_CARE skip offset] + RAW data + [DONT_CARE tail] + CRC32
	numChunks := uint32(1) // RAW
	if offsetBlocks > 0 {
		numChunks++
	}
	if tailBlocks > 0 {
		numChunks++
	}
	numChunks++ // CRC32

	// Segment data checksum (matches stock firmware CRC32 chunk)
	checksum := crc32.ChecksumIEEE(data)

	// Build header (ASR format: 32 bytes). image_checksum=0 (stock uses 0 here,
	// CRC is carried by the CRC32 chunk).
	var header bytes.Buffer
	binary.Write(&header, binary.LittleEndian, SparseMagic)       // magic
	binary.Write(&header, binary.LittleEndian, uint16(1))         // major
	binary.Write(&header, binary.LittleEndian, uint16(0))         // minor
	binary.Write(&header, binary.LittleEndian, SparseHeaderSize)  // file_header_size = 32
	binary.Write(&header, binary.LittleEndian, SparseChunkSize)   // chunk_header_size = 16
	binary.Write(&header, binary.LittleEndian, uint32(BlockSize)) // block_size
	binary.Write(&header, binary.LittleEndian, partBlocks)        // total_blocks
	binary.Write(&header, binary.LittleEndian, numChunks)         // total_chunks
	binary.Write(&header, binary.LittleEndian, uint32(0))         // image_checksum = 0
	binary.Write(&header, binary.LittleEndian, uint32(0))         // reserved (ASR extra field, 4 bytes)

	out := header.Bytes()

	// DONT_CARE chunk to skip the offset blocks (positions the segment)
	if offsetBlocks > 0 {
		out = writeSparseChunk(out, ChunkTypeDontCare, offsetBlocks, uint32(SparseChunkSize), 0)
	}

	// Raw data chunk
	chunkBlocks := dataBlocks
	chunkDataPadded := int(chunkBlocks) * BlockSize
	totalSize := uint32(SparseChunkSize) + uint32(chunkDataPadded)
	out = writeSparseChunk(out, ChunkTypeRaw, chunkBlocks, totalSize, 0)
	out = append(out, data[0:len(data)]...)
	if chunkDataPadded > len(data) {
		out = append(out, make([]byte, chunkDataPadded-len(data))...)
	}

	// DONT_CARE tail chunk (fill remaining blocks to total_blocks)
	if tailBlocks > 0 {
		out = writeSparseChunk(out, ChunkTypeDontCare, tailBlocks, uint32(SparseChunkSize), 0)
	}

	// CRC32 chunk (segment data checksum)
	var crcBuf bytes.Buffer
	binary.Write(&crcBuf, binary.LittleEndian, ChunkTypeCRC32)
	binary.Write(&crcBuf, binary.LittleEndian, uint16(0))
	binary.Write(&crcBuf, binary.LittleEndian, uint32(0))
	binary.Write(&crcBuf, binary.LittleEndian, uint32(SparseChunkSize+4)) // 16 header + 4 crc
	binary.Write(&crcBuf, binary.LittleEndian, uint32(0))
	out = append(out, crcBuf.Bytes()...)
	out = append(out, byte(checksum), byte(checksum>>8), byte(checksum>>16), byte(checksum>>24))

	return out, nil
}

// isSparseFormat checks if the data starts with the sparse magic.
func isSparseFormat(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	magic := binary.LittleEndian.Uint32(data[:4])
	return magic == SparseMagic
}

// convertZipToSparse reads a firmware zip, finds non-sparse image files
// in download.json, and converts them in-place to sparse format.
// Returns the (possibly modified) files map and the new download.json.
func convertZipToSparse(files map[string][]byte, commands []DownloadCommand) (map[string][]byte, []DownloadCommand) {
	// Build set of image filenames referenced by flash/partition/call commands
	imageNames := make(map[string]bool)
	for _, c := range commands {
		if c.Image != "" {
			imageNames[c.Image] = true
		}
	}

	for name, data := range files {
		if name == "download.json" {
			continue
		}
		if !imageNames[name] {
			continue
		}
		// Skip already-sparse files
		if isSparseFormat(data) {
			fmt.Printf("    [sparse] %s 已是 sparse 格式 (magic 0xed26ff3a)\n", name)
			continue
		}
		// Skip preboot.img and flasher.img - they're loaded via `call`, not `flash:`
		if name == "preboot.img" || name == "flasher.img" {
			fmt.Printf("    [跳过] %s 通过 call 加载，不需 sparse\n", name)
			continue
		}
		// Skip flashinfo.bin and partition.bin - they use `partition` command with raw
// data. partition.bin is also flashed to ptable (needs sparse), handled separately
// in the flash command path (see executeFlashCommands).
		if name == "flashinfo.bin" || name == "partition.bin" {
			fmt.Printf("    [跳过] %s 通过 partition 加载，不需 sparse\n", name)
			continue
		}
		// Skip files larger than maxDownloadSize - they use segmented raw download
		// (flashSegmented). Sparse conversion would break when splitting at segment
		// boundaries, and the ABOOT flasher accepts raw data directly (proven by the
		// stock QFlash firmware which downloads cp.bin as raw 0xe320f000 chunks).
		if len(data) > maxDownloadSize {
			fmt.Printf("    [跳过] %s 超过 max-download-size (%d > %d)，用 raw 分段下载\n", name, len(data), maxDownloadSize)
			continue
		}

		// Convert raw to sparse - output size defaults to actual data size (no padding)
		fmt.Printf("    [sparse] 转换 %s (raw %d bytes)...\n", name, len(data))
		sparse, err := rawToSparse(data, 0)
		if err != nil {
			fmt.Printf("    [错误] %s sparse 转换失败: %v\n", name, err)
			continue
		}
		fmt.Printf("    [sparse] %s -> %d bytes (sparse)\n", name, len(sparse))
		files[name] = sparse
	}
	return files, commands
}

// writeSparseToFile is a helper for debugging - writes a sparse image to disk.
func writeSparseToFile(name string, data []byte) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, bytes.NewReader(data))
	return err
}
