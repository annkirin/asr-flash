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
	"sync"

	"github.com/gorilla/websocket"
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
)

func StartWebServer(addr string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/devices", handleDevices)
	mux.HandleFunc("/api/flash", handleFlash)
	mux.HandleFunc("/api/read", handleRead)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/cancel", handleCancel)
	mux.HandleFunc("/ws", handleWebSocket)

	staticFS, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	log.Printf("Web 界面启动: http://localhost%s", addr)
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
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DeviceResponse{
		Path:   info.Path,
		Bus:    info.Bus,
		Addr:   info.Addr,
		Serial: info.Serial,
		Mode:   info.Mode,
	})
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
