# ASR Flash Tool

ASR/Quectel USB 模块固件烧录工具，支持 CLI 和 Web 界面。

## 功能特性

- **SMUX 协议**：实现 UABT 握手 + HELLO 协商 + ABOOT 命令/数据传输
- **自动模式切换**：正常模式 → `AT+QDownLOAD=1` → 下载模式
- **!CRANE! 固件解析**：支持 getvar/download/call/nop/reboot 命令
- **Web 界面**：Bootstrap 5 暗色主题，WebSocket 实时日志/进度推送
- **CLI 模式**：原有命令行用法，完全兼容

## 设备信息

| 参数 | 值 |
|------|-----|
| 设备名称 | ASR Microelectronics USB Modem (Quectel 合作) |
| 正常模式 VID:PID | `2c7c:6001` |
| 下载模式 VID:PID | `2ecc:3004` |
| USB 接口 | Interface 1 (SMUX/ABOOT), Interface 3 (AT 命令) |
| Bulk OUT EP | `0x02` |
| Bulk IN EP | `0x81` |
| 通信协议 | SMUX (Serial MUX) over USB Bulk |
| 固件格式 | `!CRANE!` 文本头 + 命令-响应-数据二进制流 |
| SMUX 帧定界 | `0x7E`，转义字符 `0x7D` |
| MTU | 1024 bytes (协商) |
| 数据分块 | 512 bytes |

## SMUX 帧类型

| 类型 | 值 | 说明 |
|------|-----|------|
| STDIO | `0x00` | 标准 I/O 数据 |
| HELLO | `0x01` | 握手请求 |
| HELLO_REPLY | `0x02` | 握手响应 |
| ABOOT_CMD | `0x03` | ABOOT 命令 |
| ABOOT_DATA | `0x04` | ABOOT 数据 |
| HEART_BEAT | `0x05` | 心跳 |

## 固件格式

```
!CRANE! XXXXXXXX
download:XXXXXXXX
<二进制数据>
OKAY
getvar:version
OKAY0100
...
reboot
OKAY
```

- 头部：`!CRANE!` + 固件大小（十六进制）
- download 命令：`download:` + 数据大小，后面紧跟二进制数据
- 其他命令：命令字符串 + 预期响应

## 使用方法

### Web 模式

```bash
./asr-flash
# 浏览器打开 http://localhost:8080
```

### CLI 模式

```bash
# 自动扫描设备并烧录
./asr-flash /path/to/firmware_dir

# 手动指定设备
./asr-flash /path/to/firmware_dir /dev/bus/usb/001/026
```

### Web API

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/devices` | 扫描并返回设备信息 |
| POST | `/api/flash` | 启动烧录 `{"firmware_dir":"..."}` |
| GET | `/api/status` | 查询烧录状态 |
| POST | `/api/cancel` | 取消正在进行的烧录 |
| GET | `/ws` | WebSocket 实时日志/进度推送 |

## 构建

```bash
go mod tidy
go build -o asr-flash .
```

## 项目结构

```
asr-flash/
├── main.go          # 入口，CLI/Web 模式分发
├── session.go       # SMUX 会话封装（设备状态、日志回调、并发安全）
├── smux.go          # SMUX 帧协议实现
├── usbdev.go        # Linux USBDEVFS 底层操作
├── firmware.go      # !CRANE! 固件解析与下载
├── web.go           # HTTP API + WebSocket 服务器
├── static/
│   └── index.html   # Web 前端界面
├── go.mod
└── go.sum
```

## 依赖

- Go 1.26+
- `github.com/gorilla/websocket` (WebSocket 支持)

## 许可

MIT License
