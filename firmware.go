package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type FirmwareCommand struct {
	Type     string
	CmdStr   string
	Response string
	DataSize uint32
	Data     []byte
}

type Firmware struct {
	Commands []FirmwareCommand
}

func ParseCraneFirmware(path string, logf func(string, ...interface{})) (*Firmware, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)

	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("读取固件头失败: %v", err)
	}
	header = strings.TrimRight(header, "\r\n")
	if !strings.HasPrefix(header, "!CRANE!") {
		return nil, fmt.Errorf("不是 !CRANE! 格式: %s", header[:minInt(20, len(header))])
	}

	sizeStr := strings.TrimPrefix(header, "!CRANE!")
	sizeStr = strings.TrimSpace(sizeStr)
	fwSize, err := strconv.ParseUint(sizeStr, 16, 32)
	if err != nil {
		return nil, fmt.Errorf("解析固件大小失败: %v", err)
	}
	if logf != nil {
		logf("固件大小: %d bytes (0x%x)", fwSize, fwSize)
	}

	fw := &Firmware{}

	for {
		var cmdLine string
		for {
			cmdLine, err = reader.ReadString('\n')
			if err != nil {
				break
			}
			cmdLine = strings.TrimRight(cmdLine, "\r\n")
			if cmdLine != "" {
				break
			}
		}
		if err != nil || cmdLine == "" {
			break
		}

		cmd := FirmwareCommand{
			Type:   getCmdType(cmdLine),
			CmdStr: cmdLine,
		}

		if cmd.Type == "download" {
			rspLine, err := reader.ReadString('\n')
			if err != nil {
				return nil, fmt.Errorf("download 命令缺少响应: %v", err)
			}
			rspLine = strings.TrimRight(rspLine, "\r\n")
			cmd.Response = rspLine

			sizeStr := strings.TrimPrefix(cmdLine, "download:")
			size, err := strconv.ParseUint(sizeStr, 16, 32)
			if err != nil {
				return nil, fmt.Errorf("解析 download 大小失败: %v", err)
			}

			cmd.DataSize = uint32(size)
			cmd.Data = make([]byte, size)
			_, err = io.ReadFull(reader, cmd.Data)
			if err != nil {
				return nil, fmt.Errorf("读取 download 数据失败: %v", err)
			}

			reader.ReadString('\n')

			okayLine, err := reader.ReadString('\n')
			if err == nil {
				okayLine = strings.TrimRight(okayLine, "\r\n")
				if okayLine != "" {
					cmd.Response = okayLine
				}
			}
		} else {
			rspLine, err := reader.ReadString('\n')
			if err != nil {
				return nil, fmt.Errorf("命令缺少响应: %s", cmdLine)
			}
			rspLine = strings.TrimRight(rspLine, "\r\n")
			cmd.Response = rspLine
		}

		fw.Commands = append(fw.Commands, cmd)
	}

	if logf != nil {
		logf("命令数: %d", len(fw.Commands))
		for i, cmd := range fw.Commands {
			rspPreview := cmd.Response
			if len(rspPreview) > 20 {
				rspPreview = rspPreview[:20] + "..."
			}
			if cmd.Type == "download" {
				logf("  [%d] %s -> %s (数据: %d bytes)", i, cmd.CmdStr, rspPreview, cmd.DataSize)
			} else {
				logf("  [%d] %s -> %s", i, cmd.CmdStr, rspPreview)
			}
		}
	}

	return fw, nil
}

func getCmdType(cmd string) string {
	switch {
	case strings.HasPrefix(cmd, "getvar:"):
		return "getvar"
	case strings.HasPrefix(cmd, "download:"):
		return "download"
	case strings.HasPrefix(cmd, "call"):
		return "call"
	case strings.HasPrefix(cmd, "nop"):
		return "nop"
	case strings.HasPrefix(cmd, "reboot"):
		return "reboot"
	case strings.HasPrefix(cmd, "complete"):
		return "complete"
	case strings.HasPrefix(cmd, "disconnect"):
		return "disconnect"
	default:
		return "other"
	}
}

func DownloadCraneFirmware(s *Session, fw *Firmware) error {
	total := len(fw.Commands)

	for i, cmd := range fw.Commands {
		s.Logf("[%d/%d] %s", i+1, total, cmd.CmdStr)
		s.Progress(i+1, total, cmd.CmdStr)

		rsp, err := s.SmuxSendCmd(cmd.CmdStr)
		if err != nil {
			s.Logf("  -> 错误: %v", err)
			if cmd.Type == "nop" {
				continue
			}
			return fmt.Errorf("命令失败: %v", err)
		}

		if cmd.Type == "download" {
			if !strings.HasPrefix(rsp, "DATA") {
				s.Logf("  -> FAIL: %s", rsp)
				return fmt.Errorf("download 命令失败: %s", rsp)
			}
			s.Logf("  -> %s", rsp[:minInt(20, len(rsp))])

			s.Logf("  发送 %d bytes 数据...", cmd.DataSize)
			_, err = s.SmuxSendData(cmd.Data)
			if err != nil {
				return fmt.Errorf("数据发送失败: %v", err)
			}

			finalRsp, err := s.SmuxWaitResponse(10000)
			if err != nil {
				return fmt.Errorf("等待下载完成响应失败: %v", err)
			}
			if !strings.HasPrefix(finalRsp, "OKAY") {
				return fmt.Errorf("下载完成响应异常: %s", finalRsp)
			}
			s.Logf("  下载完成: OKAY")
		} else {
			if rsp != cmd.Response && !strings.HasPrefix(rsp, "OKAY") {
				s.Logf("  -> WARN (期望 %s): %s", cmd.Response[:minInt(10, len(cmd.Response))], rsp)
				if cmd.Type == "nop" {
					continue
				}
			} else {
				s.Logf("  -> OKAY")
			}
		}
	}

	s.Logf("发送 reboot...")
	rsp, err := s.SmuxSendCmd("reboot")
	if err != nil {
		s.Logf("reboot 失败: %v", err)
	} else {
		s.Logf("reboot: %s", rsp)
	}

	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
