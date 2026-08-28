package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tarm/serial"
)

//go:embed static
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type FlashRequest struct {
	FirmwareDir string `json:"firmware_dir"`
	DevicePath  string `json:"device_path,omitempty"`
}

type ReadRequest struct {
	Command    string `json:"command"`
	DevicePath string `json:"device_path,omitempty"`
}

type ReadResponse struct {
	Status   string `json:"status"`
	Response string `json:"response,omitempty"`
	Command  string `json:"command"`
	Error    string `json:"error,omitempty"`
}

type FlashStatus struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type DeviceResponse struct {
	Path   string `json:"path"`
	Bus    int    `json:"bus"`
	Addr   int    `json:"addr"`
	Serial string `json:"serial"`
	Mode   string `json:"mode"`
}

type FlashProgress struct {
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Detail  string `json:"detail"`
}

type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

var (
	currentSession *Session
	flashMu        sync.Mutex
	wsClients      = make(map[*websocket.Conn]bool)
	wsMu           sync.Mutex
	atMu           sync.Mutex
)

func StartWebServer(addr string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/devices", handleDevices)
	mux.HandleFunc("/api/scan", handleScanDevices)
	mux.HandleFunc("/api/flash", handleFlash)
	mux.HandleFunc("/api/read", handleRead)
	mux.HandleFunc("/api/upload", handleUploadApi)
	mux.HandleFunc("/api/acmports", handleACMports)
	mux.HandleFunc("/api/verify-lcd", handleVerifyLCD)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/cancel", handleCancel)
	mux.HandleFunc("/api/at", handleAT)
	mux.HandleFunc("/ws", handleWebSocket)

	staticFS, _ := fs.Sub(staticFiles, "static")

	// 禁止缓存静态文件
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		http.FileServer(http.FS(staticFS)).ServeHTTP(w, r)
	})

	// 高级功能 API
	mux.HandleFunc("/api/batch", handleBatchFlash)
	mux.HandleFunc("/api/remote", handleRemoteFlash)
	mux.HandleFunc("/api/script", handleScript)
	mux.HandleFunc("/api/chips", handleChipList)
	
	log.Printf("Web 界面启动: http://localhost%s  [v4.0 对讲机固件烧录平台]", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info, err := FindQuectelDevice()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "found": "false"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"found":  "true",
		"path":   info.Path,
		"bus":    info.Bus,
		"addr":   info.Addr,
		"serial": info.Serial,
		"mode":   info.Mode,
	})
}

// handleScanDevices 扫描所有USB设备
func handleScanDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 扫描所有USB设备
	devices := scanAllUSBDevices()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"devices": devices,
		"count":   len(devices),
	})
}

// scanAllUSBDevices 扫描所有USB设备
func scanAllUSBDevices() []map[string]interface{} {
	var devices []map[string]interface{}

	// 尝试查找 Quectel/ASR 设备
	info, err := FindQuectelDevice()
	if err == nil {
		devices = append(devices, map[string]interface{}{
			"path":   info.Path,
			"type":   "ASR/Quectel",
			"bus":    info.Bus,
			"addr":   info.Addr,
			"serial": info.Serial,
			"mode":   info.Mode,
			"status": "connected",
		})
	}

	return devices
}

func handleFlash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flashMu.Lock()
	if currentSession != nil {
		flashMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "烧录正在进行中"})
		return
	}
	flashMu.Unlock()

	var req FlashRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.FirmwareDir == "" {
		http.Error(w, "firmware_dir required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})

	go runFlash(req)
}

func runFlash(req FlashRequest) {
	flashMu.Lock()
	defer flashMu.Unlock()

	var fd int
	var err error

	if req.DevicePath != "" {
		fd, err = OpenUSBDevice(req.DevicePath)
		if err != nil {
			broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("打开设备失败: %v", err)}})
			return
		}
	} else {
		broadcast(WSMessage{Type: "log", Data: "扫描设备..."})
		info, err := FindQuectelDevice()
		if err != nil {
			broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("未找到设备: %v", err)}})
			return
		}

		if info.Mode == "download" {
			broadcast(WSMessage{Type: "log", Data: fmt.Sprintf("设备已在下载模式: %s", info.Path)})
			broadcast(WSMessage{Type: "device", Data: DeviceResponse{Path: info.Path, Bus: info.Bus, Addr: info.Addr, Serial: info.Serial, Mode: info.Mode}})
			fd, err = OpenUSBDevice(info.Path)
			if err != nil {
				broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("打开设备失败: %v", err)}})
				return
			}
		} else {
			broadcast(WSMessage{Type: "log", Data: fmt.Sprintf("设备在正常模式: %s (bus=%d, addr=%d)", info.Serial, info.Bus, info.Addr)})
			broadcast(WSMessage{Type: "device", Data: DeviceResponse{Path: info.Path, Bus: info.Bus, Addr: info.Addr, Serial: info.Serial, Mode: info.Mode}})

			broadcast(WSMessage{Type: "log", Data: "发送 AT+QDownLOAD=1..."})
			err = SendATDownload(info.Bus, info.Addr)
			if err != nil {
				broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("AT 命令失败: %v", err)}})
				return
			}
			broadcast(WSMessage{Type: "log", Data: "已发送 AT+QDownLOAD=1"})

			broadcast(WSMessage{Type: "log", Data: "等待下载模式设备..."})
			dlInfo, err := WaitForDownloadMode(30)
			if err != nil {
				broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("等待超时: %v", err)}})
				return
			}
			broadcast(WSMessage{Type: "log", Data: fmt.Sprintf("下载模式设备: %s", dlInfo.Path)})
			broadcast(WSMessage{Type: "device", Data: DeviceResponse{Path: dlInfo.Path, Bus: dlInfo.Bus, Addr: dlInfo.Addr, Serial: dlInfo.Serial, Mode: dlInfo.Mode}})
			fd, err = OpenUSBDevice(dlInfo.Path)
			if err != nil {
				broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("打开设备失败: %v", err)}})
				return
			}
		}
	}

	session := NewSession(fd)
	currentSession = session

	session.OnLog = func(msg string) {
		broadcast(WSMessage{Type: "log", Data: msg})
	}
	session.OnProgress = func(current, total int, detail string) {
		broadcast(WSMessage{Type: "progress", Data: FlashProgress{Current: current, Total: total, Detail: detail}})
	}
	session.OnComplete = func(success bool, msg string) {
		state := "completed"
		if !success {
			state = "error"
		}
		broadcast(WSMessage{Type: "status", Data: FlashStatus{State: state, Message: msg}})
	}

	broadcast(WSMessage{Type: "log", Data: "设置 USB 接口..."})
	err = ClaimInterface(session.FD(), 1)
	if err != nil {
		session.Logf("声明接口失败: %v", err)
		broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("声明接口失败: %v", err)}})
		session.Close()
		currentSession = nil
		return
	}
	broadcast(WSMessage{Type: "log", Data: "已声明接口 1"})

	broadcast(WSMessage{Type: "log", Data: "SMUX 握手..."})
	err = session.SmuxHandshake()
	if err != nil {
		session.Logf("SMUX 握手失败: %v", err)
		broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("SMUX 握手失败: %v", err)}})
		ReleaseInterface(session.FD(), 1)
		session.Close()
		currentSession = nil
		return
	}
	broadcast(WSMessage{Type: "log", Data: "SMUX 握手成功!"})

	broadcast(WSMessage{Type: "log", Data: "读取固件..."})
	fwPath := filepath.Join(req.FirmwareDir, "firmware.bin")
	if _, err := os.Stat(fwPath); os.IsNotExist(err) {
		broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("找不到 %s", fwPath)}})
		ReleaseInterface(session.FD(), 1)
		session.Close()
		currentSession = nil
		return
	}

	fw, err := ParseCraneFirmware(fwPath, func(format string, args ...interface{}) {
		session.Logf(format, args...)
	})
	if err != nil {
		session.Logf("解析固件失败: %v", err)
		broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("解析固件失败: %v", err)}})
		ReleaseInterface(session.FD(), 1)
		session.Close()
		currentSession = nil
		return
	}
	session.Logf("固件命令数: %d", len(fw.Commands))

	broadcast(WSMessage{Type: "log", Data: "下载固件..."})
	err = DownloadCraneFirmware(session, fw)
	if err != nil {
		session.Logf("下载失败: %v", err)
		broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "error", Message: fmt.Sprintf("下载失败: %v", err)}})
	} else {
		broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "completed", Message: "烧录完成!"}})
	}

	ReleaseInterface(session.FD(), 1)
	session.Close()
	currentSession = nil
}

func handleRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flashMu.Lock()
	if currentSession != nil {
		flashMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(ReadResponse{Status: "error", Error: "烧录或读取正在进行中"})
		return
	}
	flashMu.Unlock()

	var req ReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Command == "" {
		http.Error(w, "command required", http.StatusBadRequest)
		return
	}

	var fd int
	var err error

	if req.DevicePath != "" {
		fd, err = OpenUSBDevice(req.DevicePath)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(ReadResponse{Status: "error", Error: fmt.Sprintf("打开设备失败: %v", err), Command: req.Command})
			return
		}
	} else {
		info, findErr := FindQuectelDevice()
		if findErr != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(ReadResponse{Status: "error", Error: fmt.Sprintf("未找到设备: %v", findErr), Command: req.Command})
			return
		}

		if info.Mode != "download" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ReadResponse{Status: "error", Error: "设备不在下载模式，请先切换到下载模式", Command: req.Command})
			return
		}
		fd, err = OpenUSBDevice(info.Path)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(ReadResponse{Status: "error", Error: fmt.Sprintf("打开设备失败: %v", err), Command: req.Command})
			return
		}
	}

	session := NewSession(fd)
	currentSession = session

	err = ClaimInterface(session.FD(), 1)
	if err != nil {
		session.Close()
		currentSession = nil
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ReadResponse{Status: "error", Error: fmt.Sprintf("声明接口失败: %v", err), Command: req.Command})
		return
	}

	err = session.SmuxHandshake()
	if err != nil {
		ReleaseInterface(session.FD(), 1)
		session.Close()
		currentSession = nil
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ReadResponse{Status: "error", Error: fmt.Sprintf("SMUX 握手失败: %v", err), Command: req.Command})
		return
	}

	rsp, err := session.SmuxSendCmd(req.Command)
	ReleaseInterface(session.FD(), 1)
	session.Close()
	currentSession = nil

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ReadResponse{Status: "error", Error: fmt.Sprintf("命令失败: %v", err), Command: req.Command})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ReadResponse{Status: "ok", Response: rsp, Command: req.Command})
}

func handleAT(w http.ResponseWriter, r *http.Request) {
	atMu.Lock()
	defer atMu.Unlock()
	
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Command  string `json:"command"`
		PortPath string `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Command == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "command required"})
		return
	}

	portPath := req.PortPath
	if portPath == "" {
		portPath = findATPort()
		if portPath == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "未找到 AT 串口 (ttyACM/ttyUSB)"})
			return
		}
	}

	broadcast(WSMessage{Type: "log", Data: fmt.Sprintf("AT → %s: %s", portPath, req.Command)})

	c := &serial.Config{
		Name:        portPath,
		Baud:        115200,
		ReadTimeout: 500 * time.Millisecond,
	}
	p, err := serial.OpenPort(c)
	if err != nil {
		errMsg := fmt.Sprintf("打开串口失败: %v", err)
		if strings.Contains(err.Error(), "busy") {
			errMsg = fmt.Sprintf("串口被占用 (可能是 ModemManager): %v\n建议: sudo systemctl stop ModemManager", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": errMsg})
		return
	}
	defer p.Close()

	cmd := req.Command
	if cmd != "AT" && len(cmd) < 3 {
		cmd = "AT" + cmd
	}
	if cmd[len(cmd)-1] != '\n' {
		cmd = cmd + "\r\n"
	}

	if _, err := p.Write([]byte(cmd)); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("发送失败: %v", err)})
		return
	}

	var response []byte
	buf := make([]byte, 4096)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, err := p.Read(buf)
		if n > 0 {
			response = append(response, buf[:n]...)
			respStr := string(response)
			if strings.Contains(respStr, "\r\nOK\r\n") || strings.Contains(respStr, "\r\nERROR\r\n") {
				break
			}
			deadline = time.Now().Add(500 * time.Millisecond)
		}
		if err != nil {
			break
		}
	}

	resp := strings.TrimSpace(string(response))
	broadcast(WSMessage{Type: "log", Data: fmt.Sprintf("AT ← %s", resp)})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "ok",
		"response": resp,
		"command":  req.Command,
	})
}

// findATPort 查找 AT 命令串口，优先 ttyUSB，其次 ttyACM
func findATPort() string {
	usbPorts, _ := filepath.Glob("/dev/ttyUSB*")
	if len(usbPorts) > 0 {
		return usbPorts[0]
	}
	acmPorts, _ := filepath.Glob("/dev/ttyACM*")
	if len(acmPorts) > 0 {
		return acmPorts[0]
	}
	return ""
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flashMu.Lock()
	s := currentSession
	flashMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if s == nil {
		json.NewEncoder(w).Encode(FlashStatus{State: "idle"})
	} else {
		json.NewEncoder(w).Encode(FlashStatus{State: "flashing"})
	}
}

func handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flashMu.Lock()
	s := currentSession
	flashMu.Unlock()

	if s == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": "没有正在进行的烧录"})
		return
	}

	s.Close()
	broadcast(WSMessage{Type: "status", Data: FlashStatus{State: "cancelled", Message: "已取消"}})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
}

func handleACMports(w http.ResponseWriter, r *http.Request) {
	var ports []string
	acmPorts, _ := filepath.Glob("/dev/ttyACM*")
	ports = append(ports, acmPorts...)
	usbPorts, _ := filepath.Glob("/dev/ttyUSB*")
	ports = append(ports, usbPorts...)
	if ports == nil { ports = []string{} }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ports": ports})
}
func handleVerifyLCD(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "method not allowed", 405); return }
	flashMu.Lock()
	if currentSession != nil { flashMu.Unlock(); w.WriteHeader(409); json.NewEncoder(w).Encode(map[string]string{"error":"任务进行中"}); return }
	flashMu.Unlock()
	// 异步执行 verify-lcd 上传
	go func(){
		port, err := FindACMPort()
		if err != nil { broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"error", Message: err.Error()}}); return }
		broadcast(WSMessage{Type:"log", Data: fmt.Sprintf("Verify-LCD: 串口 %s", port)})
		sess, err := NewUploadSession(port, func(f string, a ...interface{}){ broadcast(WSMessage{Type:"log", Data: fmt.Sprintf(f, a...)}) })
		if err != nil { broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"error", Message: err.Error()}}); return }
		defer sess.Close()
		if err := sess.EnterRawREPL(); err != nil { broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"error", Message: err.Error()}}); return }
		defer sess.ExitRawREPL()
		src := "/home/ankirin/asr-flash/verify_lcd_160_128.py"
		if _, err := os.Stat(src); err != nil { src = "/tmp/verify_lcd_160_128.py" }
		broadcast(WSMessage{Type:"log", Data: "上传 verify_lcd -> /usr/main.py"})
		if err := sess.UploadFile(src, "/usr/main.py"); err != nil { broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"error", Message: err.Error()}}); return }
		broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"completed", Message: "Verify-LCD 已上传，请复位设备后拍照 (128/160轮播)"}})
	}()
	w.Header().Set("Content-Type","application/json"); json.NewEncoder(w).Encode(map[string]string{"status":"started"})
}
func handleUploadApi(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w,"method not allowed",405); return }
	flashMu.Lock()
	if currentSession != nil { flashMu.Unlock(); w.WriteHeader(409); json.NewEncoder(w).Encode(map[string]string{"error":"任务进行中"}); return }
	flashMu.Unlock()
	if err := r.ParseMultipartForm(10<<20); err != nil { http.Error(w, err.Error(), 400); return }
	file, hdr, err := r.FormFile("file")
	if err != nil { http.Error(w, "file required", 400); return }
	defer file.Close()
	remote := r.FormValue("remote"); if remote=="" { remote="/usr/"+hdr.Filename }
	tmp := filepath.Join(os.TempDir(), hdr.Filename)
	out, _ := os.Create(tmp); defer os.Remove(tmp)
	buf := make([]byte, 32768)
	for { n, _ := file.Read(buf); if n==0 { break }; out.Write(buf[:n]) }
	out.Close()
	go func(tmpPath, remotePath string){
		port, err := FindACMPort()
		if err != nil { broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"error", Message: err.Error()}}); return }
		broadcast(WSMessage{Type:"log", Data: fmt.Sprintf("Upload: %s -> %s via %s", hdr.Filename, remotePath, port)})
		sess, err := NewUploadSession(port, func(f string,a ...interface{}){ broadcast(WSMessage{Type:"log", Data: fmt.Sprintf(f,a...)}) })
		if err != nil { broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"error", Message: err.Error()}}); return }
		defer sess.Close()
		if err:=sess.EnterRawREPL(); err!=nil { broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"error", Message: err.Error()}}); return }
		defer sess.ExitRawREPL()
		if err:=sess.UploadFile(tmpPath, remotePath); err!=nil { broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"error", Message: err.Error()}}); return }
		broadcast(WSMessage{Type:"status", Data: FlashStatus{State:"completed", Message: "上传完成: "+remotePath}})
	}(tmp, remote)
	w.Header().Set("Content-Type","application/json"); json.NewEncoder(w).Encode(map[string]string{"status":"started", "remote": remote})
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}

	wsMu.Lock()
	wsClients[conn] = true
	wsMu.Unlock()

	defer func() {
		wsMu.Lock()
		delete(wsClients, conn)
		wsMu.Unlock()
		conn.Close()
	}()

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func broadcast(msg WSMessage) {
	wsMu.Lock()
	defer wsMu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	for conn := range wsClients {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			conn.Close()
			delete(wsClients, conn)
		}
	}
}

// handleChipList 获取芯片配置列表
func handleChipList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	chipsDir := filepath.Join(filepath.Dir(os.Args[0]), "chips")
	mgr, err := NewChipManager(chipsDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error(), "chips": []interface{}{}})
		return
	}
	
	chips := mgr.ListChips()
	result := make([]map[string]interface{}, 0, len(chips))
	for _, chip := range chips {
		methods := make([]string, 0)
		for _, m := range chip.FlashMethods {
			methods = append(methods, m.Name)
		}
		result = append(result, map[string]interface{}{
			"name":         chip.Name,
			"family":       chip.Family,
			"description":  chip.Description,
			"flash_methods": methods,
		})
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"chips": result})
}

// handleBatchFlash 批量烧录
func handleBatchFlash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req struct {
		Devices  []string `json:"devices"`
		Firmware string   `json:"firmware"`
		Parallel int      `json:"parallel"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	
	if len(req.Devices) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "devices required"})
		return
	}
	
	batch := NewBatchFlash(req.Devices, req.Firmware, req.Parallel)
	result, err := batch.Start()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleRemoteFlash 远程烧录
func handleRemoteFlash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		KeyFile  string `json:"key_file"`
		Device   string `json:"device"`
		Firmware string `json:"firmware"`
		Action   string `json:"action"` // discover, flash
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	
	remote := NewRemoteFlash(req.Host, req.Port, req.Username, req.Password, req.KeyFile)
	
	switch req.Action {
	case "discover":
		devices, err := remote.DiscoverDevices()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"devices": devices})
		
	case "flash":
		err := remote.FlashDevice(req.Device, req.Firmware)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown action"})
	}
}

// handleScript 脚本执行
func handleScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req struct {
		Action string `json:"action"` // list, run, create
		Name   string `json:"name"`
		Script *Script `json:"script"`
		Device string `json:"device"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	
	runner := NewScriptRunner()
	
	switch req.Action {
	case "list":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"scripts": []string{}})
		
	case "create":
		if req.Script == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "script required"})
			return
		}
		runner.LoadScript(req.Name, req.Script)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		
	case "run":
		if err := runner.RunScript(req.Name, req.Device); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown action"})
	}
}