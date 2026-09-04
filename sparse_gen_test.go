package main

import (
	"encoding/binary"
	"testing"
)

func TestSparseSegmentedGen(t *testing.T) {
	total := 4239360
	segPayload := maxDownloadSize - 256
	seg0 := make([]byte, segPayload)
	for i := range seg0 {
		seg0[i] = byte(i)
	}
	sparse, err := rawToSparseAtOffset(seg0, uint32(total), 0)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(sparse[0:4]) != 0xed26ff3a {
		t.Fatal("bad magic")
	}
	if binary.LittleEndian.Uint16(sparse[8:10]) != 32 {
		t.Fatalf("fhs=%d expect 32", binary.LittleEndian.Uint16(sparse[8:10]))
	}
	if binary.LittleEndian.Uint16(sparse[10:12]) != 16 {
		t.Fatalf("chs=%d expect 16", binary.LittleEndian.Uint16(sparse[10:12]))
	}
	tb := binary.LittleEndian.Uint32(sparse[16:20])
	if tb != uint32(total/4096) {
		t.Fatalf("total_blocks=%d expect %d", tb, total/4096)
	}
	ct := binary.LittleEndian.Uint16(sparse[32:34])
	if ct != 0xCAC1 {
		t.Fatalf("chunk1 type=0x%04x expect RAW", ct)
	}
	// 验证带偏移的段
	seg2 := make([]byte, segPayload)
	sparse2, _ := rawToSparseAtOffset(seg2, uint32(total), uint32(2*segPayload/4096))
	ct2 := binary.LittleEndian.Uint16(sparse2[32:34])
	if ct2 != 0xCAC3 {
		t.Fatalf("seg2 first chunk type=0x%04x expect DONT_CARE", ct2)
	}
}
