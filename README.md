# ASR Flash Tool

ASR/Quectel USB 模块固件烧录工具，支持 CLI 和 Web 界面。

## 功能特性

- **SMUX 协议**：实现 UABT 握手 + HELLO 协商 + ABOOT 命令/数据传输
- **自动模式切换**：正常模式 → `AT+QDownLOAD=1` → 下载模式
- **!CRANE! 固件解析**：支持 getvar/download/call/nop/reboot 命令
- **读取设备信息**：支持 getvar 查询、OEM 命令执行（实验性）
- **Web 界面**：Bootstrap 5 暗色主题，WebSocket 实时日志/进度推送
- **CLI 模式**：原有命令行用法，完全兼容
- **稀疏分段下载**：cp.bin/dsp.bin 大镜像自动分段 sparse 刷写（原厂格式字节级一致）
- **内置监控**：`flash-watch` 持续检测设备，稍纵即逝接口窗口自动刷写
- **快速失败**：捕获 `[WARN: Aboot]`/`[ERR : Exception]` 立即返回，避免长时间等待

## 救砖实战案例：EC600N IP-Q8 (ASR1603)

**现象**：设备被刷坏，进入退避循环——每次在线 0.37-0.6 秒即断开，退避间隔递增至 498s 后完全停止，必须物理拔插唤醒。

**救砖关键点**：
1. **高频 USB 线**（手机数据线/原厂线）稳定接口，解决退避循环不稳定
2. **C 语言工具时序精确**（Python pyusb 时序不准导致失败）
3. **cp.bin/dsp.bin 稀疏分段格式修正**：与原厂 `firmware.bin` 字节级一致
4. **内置监控 `flash-watch`**：5ms 轮询，稍纵即逝接口窗口自动抓取刷写

**刷写结果**：
- cp.bin (4.2MB) 分 5 段 × 255块/段 sparse 刷写成功
- dsp.bin (1.1MB) 分 2 段 sparse 刷写成功
- 所有分区一次性刷写成功，设备恢复正常模式 (2c7c:6001)，AT 命令正常

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

## 稀疏分段格式（cp.bin/dsp.bin 等大镜像）

ASR flasher 单次 download 限制 1.75MB (0x1c0000)，大镜像需分段。每段 255 块 = 1MB：

**分段1 (offset=0)**：
```
[RAW(255块, 1044480B)] + [DONT_CARE(剩余块)] + [CRC32(段数据)]
```

**分段2+ (offset>0)**：
```
[DONT_CARE(偏移块)] + [RAW(255块)] + [DONT_CARE(尾部块)] + [CRC32(段数据)]
```

- CRC32 = 该段原始数据的 crc32（IEEE）
- 与原厂 `firmware.bin` CRANE 脚本字节级一致

## 使用方法

### Web 模式

```bash
./asr-flash
# 浏览器打开 http://localhost:8080
```

Web 界面支持两个 Tab：
- **固件烧录**：选择固件目录，点击"开始烧录"
- **读取信息**：快捷命令按钮或自定义命令执行

### CLI 模式

```bash
# 自动扫描设备并烧录 (CRANE 格式目录)
./asr-flash /path/to/firmware_dir

# 手动指定设备
./asr-flash /path/to/firmware_dir /dev/bus/usb/001/026

# 读取设备信息（交互式）
./asr-flash read

# 执行单个读取命令
./asr-flash read-cmd getvar:product
./asr-flash read-cmd getvar:all
./asr-flash read-cmd "oem flashinfo"
```

### 专用刷写命令

```bash
# 1. 一键烧录 QuecPython 固件包
./asr-flash flash-quecpython QPY_OCPU_V0002_EC600N_CNLF_FW.bin

# 2. 烧录 Logicrom 固件包（全量）
./asr-flash flash-logicrom heyptt-logicrom.zip

# 3. 仅烧录 app 分区（增量更新，不碰基带/分区表）
./asr-flash flash-logicrom heyptt-logicrom.zip --app-only

# 4. 禁用 sparse 自动转换（调试用）
./asr-flash flash-logicrom logicrom_core.zip --no-sparse

# 5. 仅烧录单个 app.bin 到 user_app 分区
./asr-flash flash-app build/app.bin

# 6. 读回指定分区
./asr-flash read-partition bootloader [size_hex] [outfile]
```

### 监控模式（救砖神器）

```bash
# 持续监控：检测到设备即刷写
sudo ./asr-flash flash-watch /tmp/heyptt-full-cp.zip --retry --interval-ms 5 --timeout 600

# 仅用 raw 分段下载（cp.bin 等大文件不转 sparse）
./asr-flash flash-watch heyptt-full-cp.zip --no-sparse
```

参数说明：
- `--retry`：刷写失败后持续重试，直到成功或超时
- `--interval-ms <ms>`：轮询间隔（默认 5ms）
- `--timeout <秒>`：监控总超时（默认 300 秒）
- `--no-sparse`：大文件用 raw 分段下载（不转 sparse）

### Web API

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/devices` | 扫描并返回设备信息 |
| POST | `/api/flash` | 启动烧录 `{"firmware_dir":"..."}` |
| POST | `/api/read` | 执行读取命令 `{"command":"getvar:product"}` |
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
├── session.go       # SMUX 会话封装（快速失败、数据接收、并发安全）
├── smux.go          # SMUX 帧协议（优化超时、心跳、重握手等待）
├── usbdev.go        # Linux USBDEVFS 底层操作（USB 复位）
├── firmware.go      # !CRANE! 固件解析与下载
├── sparse.go        # ASR 稀疏格式生成（32B头+16B chunk，分段生成）
├── web.go           # HTTP API + WebSocket 服务器
├── static/
│   └── index.html   # Web 前端界面
├── go.mod
├── go.sum
├── sparse_gen_test.go  # 稀疏分段单元测试
└── test_flash.sh       # 测试脚本
```

## 关键优化（v4.1）

1. **SMUX 优化**：握手后 sleep 500ms→50ms，call 后 3000ms→200ms，节省 ~4s
2. **超时扩大**：命令/数据写入 5s→30s，响应等待 120s→300s，避免大数据超时
3. **快速失败**：捕获 `[WARN: Aboot]`/`[ERR : Exception]` 标记 `abortReason` 立即返回
4. **USB 复位**：`ResetUSBDevice()` 强制设备重新枚举，处理脏状态
5. **二进制数据接收**：支持 `flash:read` 等读回操作

## 依赖

- Go 1.26+
- `github.com/gorilla/websocket` (WebSocket 支持)

## 许可

MIT License