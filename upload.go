package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tarm/serial"
)

type UploadSession struct {
	port *serial.Port
	logf func(string, ...interface{})
}

func NewUploadSession(portPath string, logf func(string, ...interface{})) (*UploadSession, error) {
	c := &serial.Config{
		Name:        portPath,
		Baud:        115200,
		ReadTimeout: 2 * time.Second,
	}
	p, err := serial.OpenPort(c)
	if err != nil {
		return nil, fmt.Errorf("open serial port: %w", err)
	}
	return &UploadSession{port: p, logf: logf}, nil
}

func (u *UploadSession) Close() {
	if u.port != nil {
		u.port.Close()
	}
}

func (u *UploadSession) write(data []byte) error {
	_, err := u.port.Write(data)
	return err
}

func (u *UploadSession) read(timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var result []byte
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		n, err := u.port.Read(buf)
		if n > 0 {
			result = append(result, buf[:n]...)
		}
		if err != nil {
			break
		}
		if n > 0 {
			deadline = time.Now().Add(500 * time.Millisecond)
		}
	}
	return result, nil
}

func (u *UploadSession) exec(cmd string, timeout time.Duration) (string, error) {
	// Enter raw REPL
	u.write([]byte{0x01})
	time.Sleep(300 * time.Millisecond)
	u.read(500 * time.Millisecond)

	// Send command
	u.write([]byte(cmd + "\r\n"))
	time.Sleep(100 * time.Millisecond)

	// Execute with Ctrl+D
	u.write([]byte{0x04})
	time.Sleep(timeout)

	resp, _ := u.read(2 * time.Second)
	text := string(resp)

	// Strip raw REPL artifacts
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == ">" || line == "raw REPL; CTRL-B to exit" || line == ">>> " {
			continue
		}
		return line, nil
	}
	return text, nil
}

func (u *UploadSession) rawExec(cmd string) (string, error) {
	u.write([]byte{0x01})
	time.Sleep(300 * time.Millisecond)
	u.read(500 * time.Millisecond)

	u.write([]byte(cmd + "\r\n"))
	time.Sleep(100 * time.Millisecond)
	u.write([]byte{0x04})
	time.Sleep(500 * time.Millisecond)

	resp, _ := u.read(3 * time.Second)
	return string(resp), nil
}

func (u *UploadSession) EnterRawREPL() error {
	// Ctrl+C twice to stop any running program
	u.write([]byte{0x03, 0x03})
	time.Sleep(500 * time.Millisecond)
	u.read(500 * time.Millisecond)

	// Ctrl+A to enter raw REPL
	u.write([]byte{0x01})
	time.Sleep(500 * time.Millisecond)
	resp, _ := u.read(1 * time.Second)
	if !strings.Contains(string(resp), "raw REPL") {
		return fmt.Errorf("failed to enter raw REPL: %s", string(resp))
	}
	return nil
}

func (u *UploadSession) ExitRawREPL() {
	u.write([]byte{0x02})
	time.Sleep(300 * time.Millisecond)
}

func (u *UploadSession) UploadFile(localPath, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read local file: %w", err)
	}

	u.logf("上传 %s -> %s (%d bytes)", filepath.Base(localPath), remotePath, len(data))

	// Encode to base64
	b64 := base64.StdEncoding.EncodeToString(data)

	// Open file for writing on device
	_, err = u.rawExec(fmt.Sprintf("import ubinascii; f=open('%s','wb')", remotePath))
	if err != nil {
		return fmt.Errorf("open file on device: %w", err)
	}

	// Upload in chunks
	chunkSize := 512
	numChunks := (len(b64) + chunkSize - 1) / chunkSize

	for i := 0; i < numChunks; i++ {
		end := (i + 1) * chunkSize
		if end > len(b64) {
			end = len(b64)
		}
		chunk := b64[i*chunkSize : end]

		cmd := fmt.Sprintf("f.write(ubinascii.a2b_base64('%s'))", chunk)
		u.write([]byte{0x01})
		time.Sleep(200 * time.Millisecond)
		u.read(300 * time.Millisecond)
		u.write([]byte(cmd + "\r\n"))
		time.Sleep(50 * time.Millisecond)
		u.write([]byte{0x04})
		time.Sleep(200 * time.Millisecond)
		u.read(300 * time.Millisecond)

		if u.logf != nil {
			progress := (i + 1) * 100 / numChunks
			u.logf("  进度: %d%%", progress)
		}
	}

	// Close file
	u.write([]byte{0x01})
	time.Sleep(200 * time.Millisecond)
	u.read(300 * time.Millisecond)
	u.write([]byte("f.close()\r\n"))
	time.Sleep(50 * time.Millisecond)
	u.write([]byte{0x04})
	time.Sleep(500 * time.Millisecond)
	u.read(500 * time.Millisecond)

	// Verify by reading file back
	resp, err := u.rawExec(fmt.Sprintf("f=open('%s','rb'); d=f.read(); f.close(); print(len(d))", remotePath))
	if err != nil {
		u.logf("  验证失败: %v", err)
	} else {
		sizeStr := strings.TrimSpace(resp)
		// Extract just the number from response like "OK123\r\n\x04\x04>"
		for _, ch := range sizeStr {
			if ch >= '0' && ch <= '9' {
				u.logf("  设备上文件大小: %s", sizeStr)
				break
			}
		}
	}

	return nil
}

func FindACMPort() (string, error) {
	matches, _ := filepath.Glob("/dev/ttyACM*")
	if len(matches) == 0 {
		return "", fmt.Errorf("未找到 ttyACM 串口")
	}
	return matches[0], nil
}

func verifyLCDMode() {
	// 一键验证 LCD 分辨率：上传 verify_lcd_160_128.py -> /usr/main.py 并重启
	candidates := []string{"/tmp/verify_lcd_160_128.py", "./verify_lcd_160_128.py", "/home/ankirin/asr-flash/verify_lcd_160_128.py"}
	var src string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			src = p
			break
		}
	}
	if src == "" {
		fmt.Println("未找到 verify_lcd_160_128.py，请先生成 /tmp/verify_lcd_160_128.py")
		os.Exit(1)
	}
	fmt.Printf("=== 一键LCD验证 ===\n源: %s -> /usr/main.py\n", src)
	uploadMode([]string{src, "/usr/main.py"})
	fmt.Println("提示：设备复位后将轮播 128边框/160边框/160X 各3秒，请拍照发来定夺")
}

func uploadMode(args []string) {
	if len(args) < 1 {
		fmt.Println("用法: asr-flash upload <local_file> [remote_path]")
		fmt.Println("示例: asr-flash upload poc_main.py /poc_main.py")
		fmt.Println("      asr-flash upload *.py")
		fmt.Println("      asr-flash upload --dir /path/to/files")
		fmt.Println("      asr-flash verify-lcd   # 一键LCD验证")
		os.Exit(1)
	}

	logf := func(format string, args ...interface{}) {
		fmt.Printf("  "+format+"\n", args...)
	}

	// Find ACM port
	portPath, err := FindACMPort()
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("找到串口: %s\n", portPath)

	// Open serial connection
	sess, err := NewUploadSession(portPath, logf)
	if err != nil {
		fmt.Printf("打开串口失败: %v\n", err)
		os.Exit(1)
	}
	defer sess.Close()

	// Enter raw REPL
	fmt.Println("进入 Raw REPL...")
	err = sess.EnterRawREPL()
	if err != nil {
		fmt.Printf("进入 Raw REPL 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("已进入 Raw REPL")

	// Upload files
	if args[0] == "--dir" {
		// Upload all .py files from directory
		dir := args[1]
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".py") {
				localPath := filepath.Join(dir, entry.Name())
				remotePath := "/usr/" + entry.Name()
				err := sess.UploadFile(localPath, remotePath)
				if err != nil {
					fmt.Printf("上传 %s 失败: %v\n", entry.Name(), err)
				}
			}
		}
	} else {
		// Upload individual files
		for i := 0; i < len(args); i += 2 {
			localPath := args[i]
			remotePath := ""
			if i+1 < len(args) {
				remotePath = args[i+1]
			} else {
				remotePath = "/usr/" + filepath.Base(localPath)
			}
			err := sess.UploadFile(localPath, remotePath)
			if err != nil {
				fmt.Printf("上传 %s 失败: %v\n", localPath, err)
			}
		}
	}

	// Exit raw REPL
	sess.ExitRawREPL()
	fmt.Println("上传完成!")
}
