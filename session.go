package main

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Session struct {
	fd     int
	mu     sync.Mutex
	done   chan struct{}
	closed bool

	cmdResponse string
	cmdCond     *sync.Cond
	parser      *smuxParser
	rxRunning   bool

	// abortReason 记录设备异常信号（如 "[WARN: Aboot" / "[ERR : Exception"），
	// 用于快速失败避免长时间等待超时
	abortReason string

	// 二进制数据接收（用于 flash:read 等读回操作）
	dataBuffer []byte
	dataCond   *sync.Cond
	expectData bool
	expectSize int

	OnLog      func(msg string)
	OnProgress func(current, total int, detail string)
	OnComplete func(success bool, msg string)
}

func NewSession(fd int) *Session {
	s := &Session{
		fd:   fd,
		done: make(chan struct{}),
	}
	s.cmdCond = sync.NewCond(&s.mu)
	s.dataCond = sync.NewCond(&s.mu)
	s.parser = newSmuxParser()
	return s
}

// BeginDataExpect 标记开始接收二进制数据，期望 totalBytes 字节
func (s *Session) BeginDataExpect(totalBytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expectData = true
	s.expectSize = totalBytes
	s.dataBuffer = make([]byte, 0, totalBytes)
	s.dataCond.Broadcast()
}

// ReceiveData 等待接收 expectSize 字节的二进制数据（含超时）
func (s *Session) ReceiveData(timeoutMs int) ([]byte, error) {
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for {
		s.mu.Lock()
		if !s.expectData {
			s.mu.Unlock()
			return nil, fmt.Errorf("not expecting data")
		}
		if len(s.dataBuffer) >= s.expectSize {
			data := s.dataBuffer
			s.dataBuffer = nil
			s.expectData = false
			s.mu.Unlock()
			return data, nil
		}
		s.mu.Unlock()

		if time.Now().After(deadline) {
			s.mu.Lock()
			got := len(s.dataBuffer)
			s.mu.Unlock()
			return nil, fmt.Errorf("timeout waiting data: got %d/%d bytes", got, s.expectSize)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// appendData 追加接收到的数据块（由 rxLoop 调用）
func (s *Session) appendData(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.expectData {
		return
	}
	remaining := s.expectSize - len(s.dataBuffer)
	if len(data) > remaining {
		data = data[:remaining]
	}
	s.dataBuffer = append(s.dataBuffer, data...)
	if len(s.dataBuffer) >= s.expectSize {
		s.dataCond.Broadcast()
	}
}

func (s *Session) Logf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if s.OnLog != nil {
		s.OnLog(msg)
	}
}

func (s *Session) Progress(current, total int, detail string) {
	if s.OnProgress != nil {
		s.OnProgress(current, total, detail)
	}
}

func (s *Session) Complete(success bool, msg string) {
	if s.OnComplete != nil {
		s.OnComplete(success, msg)
	}
}

func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
	if s.fd >= 0 {
		syscall.Close(s.fd)
		s.fd = -1
	}
}

func (s *Session) FD() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fd
}

func (s *Session) Done() <-chan struct{} {
	return s.done
}

func (s *Session) sendCmdResponse(text string) {
	s.mu.Lock()
	s.cmdResponse = text
	s.cmdCond.Broadcast()
	s.mu.Unlock()
}

func (s *Session) waitForResponse(timeoutMs int, check func() bool) error {
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)

	for {
		s.mu.Lock()
		if check() {
			s.mu.Unlock()
			return nil
		}
		if s.abortReason != "" {
			reason := s.abortReason
			s.abortReason = ""
			s.mu.Unlock()
			return fmt.Errorf("device abort: %s", reason)
		}
		s.mu.Unlock()

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %dms", timeoutMs)
		}

		select {
		case <-s.done:
			return fmt.Errorf("session closed")
		default:
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (s *Session) startRxLoop() {
	s.mu.Lock()
	if s.rxRunning {
		s.mu.Unlock()
		return
	}
	s.rxRunning = true
	s.mu.Unlock()

	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-s.done:
				return
			default:
			}

			fd := s.FD()
			if fd < 0 {
				return
			}
			n, err := BulkRead(fd, EP_IN, buf, 5000)
			if err != nil {
				continue
			}
			if n > 0 {
				s.Logf("[RX %d bytes] %x", n, buf[:min(n, 16)])
			}
			for i := 0; i < n; i++ {
				ft, payload, ok := s.parser.processByte(buf[i])
				if !ok {
					continue
				}

				switch ft {
case SMUX_FRAME_TYPE_STDIO:
				text := string(payload)
				if text == SMUX_PREAMBLE_UABT {
					s.sendCmdResponse("UABT")
				} else if isExpectedResponse(text) {
					s.sendCmdResponse(text)
				} else if strings.HasPrefix(text, "[WARN") || strings.HasPrefix(text, "[ERR") {
					// 设备 bootloader 异常信号（如 "[WARN: Aboot"），标记快速失败
					s.mu.Lock()
					if s.abortReason == "" {
						s.abortReason = text
					}
					s.mu.Unlock()
				}

				case SMUX_FRAME_TYPE_HELLO_REPLY:
					mtu := uint16(0)
					if len(payload) >= 2 {
						mtu = uint16(payload[0])<<8 | uint16(payload[1])
					}
					s.sendCmdResponse(fmt.Sprintf("HELLO_REPLY:%d", mtu))

				case SMUX_FRAME_TYPE_ABOOT_CMD:
					text := string(payload)
					if isExpectedResponse(text) {
						s.sendCmdResponse(text)
					}

				case SMUX_FRAME_TYPE_ABOOT_DATA:
					// 二进制数据（读回分区时）
					s.appendData(payload)

				case SMUX_FRAME_TYPE_HEART_BEAT:
				}
			}
		}
	}()
}

func isExpectedResponse(text string) bool {
	return len(text) >= 4 && (text[:4] == "OKAY" || text[:4] == "DATA" || text[:4] == "FAIL")
}

func (s *Session) SmuxRecvData(timeoutMs int) ([]byte, error) {
	var received []byte

	err := s.waitForResponse(timeoutMs, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.cmdResponse) > 0 && (strings.HasPrefix(s.cmdResponse, "OKAY") ||
			strings.HasPrefix(s.cmdResponse, "DATA") ||
			strings.HasPrefix(s.cmdResponse, "FAIL"))
	})
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	rsp := s.cmdResponse
	s.mu.Unlock()

	if strings.HasPrefix(rsp, "DATA") {
		sizeStr := strings.TrimPrefix(rsp, "DATA")
		size := 0
		fmt.Sscanf(sizeStr, "%x", &size)
		if size > 0 {
			received = make([]byte, 0, size)
		}
	}

	return received, nil
}
