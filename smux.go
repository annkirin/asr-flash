package main

import (
	"fmt"
	"strings"
	"time"
)

const (
	SMUX_FRAME_DELIMITER byte = 0x7E
	SMUX_ESC_CHAR        byte = 0x7D

	SMUX_FRAME_TYPE_STDIO      byte = 0x00
	SMUX_FRAME_TYPE_HELLO      byte = 0x01
	SMUX_FRAME_TYPE_HELLO_REPLY byte = 0x02
	SMUX_FRAME_TYPE_ABOOT_CMD  byte = 0x03
	SMUX_FRAME_TYPE_ABOOT_DATA byte = 0x04
	SMUX_FRAME_TYPE_HEART_BEAT byte = 0x05

	SMUX_PREAMBLE_UABT = "UABT"

	EP_OUT byte = 0x02
	EP_IN  byte = 0x81
)

func smuxEscape(data byte) []byte {
	switch data {
	case SMUX_FRAME_DELIMITER:
		return []byte{SMUX_ESC_CHAR, SMUX_FRAME_DELIMITER ^ 0x20}
	case SMUX_ESC_CHAR:
		return []byte{SMUX_ESC_CHAR, SMUX_ESC_CHAR ^ 0x20}
	default:
		return []byte{data}
	}
}

func smuxBuildFrame(frameType byte, payload []byte) []byte {
	var frame []byte
	frame = append(frame, SMUX_FRAME_DELIMITER)

	if frameType != SMUX_FRAME_TYPE_STDIO {
		frame = append(frame, SMUX_ESC_CHAR)
		frame = append(frame, frameType^0x20)
	}

	for _, b := range payload {
		frame = append(frame, smuxEscape(b)...)
	}

	frame = append(frame, SMUX_FRAME_DELIMITER)
	return frame
}

type smuxParser struct {
	state     int
	frametype byte
	framesize int
	rxbuf     []byte
}

const (
	SMUX_WAIT_START = 0
	SMUX_IN_FRAME   = 1
	SMUX_IN_ESCAPE  = 2
)

func newSmuxParser() *smuxParser {
	return &smuxParser{
		state: SMUX_WAIT_START,
		rxbuf: make([]byte, 4096),
	}
}

func (p *smuxParser) processByte(c byte) (frameType byte, payload []byte, ok bool) {
	switch p.state {
	case SMUX_WAIT_START:
		if c == SMUX_FRAME_DELIMITER {
			p.state = SMUX_IN_FRAME
			p.frametype = SMUX_FRAME_TYPE_STDIO
			p.framesize = 0
		}
	case SMUX_IN_FRAME:
		if c == SMUX_ESC_CHAR {
			p.state = SMUX_IN_ESCAPE
		} else if c == SMUX_FRAME_DELIMITER {
			if p.framesize > 0 {
				result := make([]byte, p.framesize)
				copy(result, p.rxbuf[:p.framesize])
				ft := p.frametype
				p.state = SMUX_WAIT_START
				p.framesize = 0
				return ft, result, true
			}
			p.state = SMUX_WAIT_START
		} else {
			if p.framesize < len(p.rxbuf) {
				p.rxbuf[p.framesize] = c
				p.framesize++
			}
		}
	case SMUX_IN_ESCAPE:
		switch c {
		case SMUX_FRAME_DELIMITER ^ 0x20:
			p.state = SMUX_IN_FRAME
			if p.framesize < len(p.rxbuf) {
				p.rxbuf[p.framesize] = SMUX_FRAME_DELIMITER
				p.framesize++
			}
		case SMUX_ESC_CHAR ^ 0x20:
			p.state = SMUX_IN_FRAME
			if p.framesize < len(p.rxbuf) {
				p.rxbuf[p.framesize] = SMUX_ESC_CHAR
				p.framesize++
			}
		case SMUX_FRAME_TYPE_STDIO ^ 0x20:
			p.frametype = SMUX_FRAME_TYPE_STDIO
			p.state = SMUX_IN_FRAME
		case SMUX_FRAME_TYPE_HELLO ^ 0x20:
			p.frametype = SMUX_FRAME_TYPE_HELLO
			p.state = SMUX_IN_FRAME
		case SMUX_FRAME_TYPE_HELLO_REPLY ^ 0x20:
			p.frametype = SMUX_FRAME_TYPE_HELLO_REPLY
			p.state = SMUX_IN_FRAME
		case SMUX_FRAME_TYPE_ABOOT_CMD ^ 0x20:
			p.frametype = SMUX_FRAME_TYPE_ABOOT_CMD
			p.state = SMUX_IN_FRAME
		case SMUX_FRAME_TYPE_ABOOT_DATA ^ 0x20:
			p.frametype = SMUX_FRAME_TYPE_ABOOT_DATA
			p.state = SMUX_IN_FRAME
		case SMUX_FRAME_TYPE_HEART_BEAT ^ 0x20:
			p.frametype = SMUX_FRAME_TYPE_HEART_BEAT
			p.state = SMUX_IN_FRAME
		default:
			p.state = SMUX_IN_FRAME
			p.frametype = SMUX_FRAME_TYPE_STDIO
		}
	}
	return 0, nil, false
}

func (s *Session) SmuxHandshake() error {
	s.startRxLoop()

	s.Logf("发送 UABT 前导码...")
	uabtFrame := smuxBuildFrame(SMUX_FRAME_TYPE_STDIO, []byte(SMUX_PREAMBLE_UABT))
	_, err := BulkWrite(s.FD(), EP_OUT, uabtFrame, 2000)
	if err != nil {
		return fmt.Errorf("send UABT: %w", err)
	}

	s.Logf("等待设备响应...")
	err = s.waitForResponse(5000, func() bool {
		return s.cmdResponse == "UABT"
	})
	if err != nil {
		return fmt.Errorf("wait UABT response: %w", err)
	}
	s.Logf("设备确认 UABT!")

	s.mu.Lock()
	s.parser = newSmuxParser()
	s.mu.Unlock()
	time.Sleep(50 * time.Millisecond)

	s.Logf("发送 HELLO (MTU=1024)...")
	mtu := uint16(1024)
	mtuBytes := []byte{byte(mtu >> 8), byte(mtu & 0xFF)}
	helloFrame := smuxBuildFrame(SMUX_FRAME_TYPE_HELLO, mtuBytes)
	_, err = BulkWrite(s.FD(), EP_OUT, helloFrame, 2000)
	if err != nil {
		return fmt.Errorf("send HELLO: %w", err)
	}

	s.Logf("等待 HELLO_REPLY...")
	err = s.waitForResponse(5000, func() bool {
		return strings.HasPrefix(s.cmdResponse, "HELLO_REPLY:")
	})
	if err != nil {
		return fmt.Errorf("wait HELLO_REPLY: %w", err)
	}

	s.mu.Lock()
	rsp := s.cmdResponse
	s.mu.Unlock()
	s.Logf("HELLO_REPLY 收到: %s", rsp)

	return nil
}

func (s *Session) SmuxSendCmd(cmd string) (string, error) {
	s.Logf("SmuxSendCmd: %s", cmd)
	s.mu.Lock()
	s.cmdResponse = ""
	s.mu.Unlock()

	frame := smuxBuildFrame(SMUX_FRAME_TYPE_ABOOT_CMD, []byte(cmd))
	_, err := BulkWrite(s.FD(), EP_OUT, frame, 30000)
	if err != nil {
		return "", fmt.Errorf("send cmd: %w", err)
	}

	err = s.waitForResponse(300000, func() bool {
		return strings.HasPrefix(s.cmdResponse, "OKAY") ||
			strings.HasPrefix(s.cmdResponse, "DATA") ||
			strings.HasPrefix(s.cmdResponse, "FAIL")
	})
	if err != nil {
		return "", fmt.Errorf("wait response for cmd '%s': %w", cmd, err)
	}

	s.mu.Lock()
	rsp := s.cmdResponse
	s.mu.Unlock()

	return rsp, nil
}

func (s *Session) SmuxSendData(data []byte) (string, error) {
	chunkSize := 512
	offset := 0
	total := len(data)

	for offset < total {
		end := offset + chunkSize
		if end > total {
			end = total
		}
		chunk := data[offset:end]

		frame := smuxBuildFrame(SMUX_FRAME_TYPE_ABOOT_DATA, chunk)
		_, err := BulkWrite(s.FD(), EP_OUT, frame, 30000)
		if err != nil {
			return "", fmt.Errorf("send data chunk at offset %d: %w", offset, err)
		}

		offset = end
		if offset < total {
			time.Sleep(10 * time.Millisecond)
		}
	}
	s.Logf("SmuxSendData: sent %d bytes, waiting for response...", total)

	s.mu.Lock()
	s.cmdResponse = ""
	s.mu.Unlock()

	err := s.waitForResponse(300000, func() bool {
		return strings.HasPrefix(s.cmdResponse, "OKAY") ||
			strings.HasPrefix(s.cmdResponse, "DATA") ||
			strings.HasPrefix(s.cmdResponse, "FAIL")
	})
	if err != nil {
		return "", fmt.Errorf("wait data response (sent %d bytes): %w", total, err)
	}

	s.mu.Lock()
	rsp := s.cmdResponse
	s.mu.Unlock()

	return rsp, nil
}

func (s *Session) SmuxWaitResponse(timeoutMs int) (string, error) {
	err := s.waitForResponse(timeoutMs, func() bool {
		return strings.HasPrefix(s.cmdResponse, "OKAY") ||
			strings.HasPrefix(s.cmdResponse, "DATA") ||
			strings.HasPrefix(s.cmdResponse, "FAIL")
	})
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	rsp := s.cmdResponse
	s.mu.Unlock()

	return rsp, nil
}

// waitForDeviceRehandshake 等待设备发送 HELLO_REPLY（预引导程序启动后重新握手）
func (s *Session) waitForDeviceRehandshake(timeoutMs int) error {
	return s.waitForResponse(timeoutMs, func() bool {
		return strings.HasPrefix(s.cmdResponse, "HELLO_REPLY:")
	})
}
