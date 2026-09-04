package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxDownloadSize 单次 download 的最大字节数（原厂 flasher = 0x1c0000 = 1.75MB）
// 超过此限制的数据需要分段下载
const maxDownloadSize = 0x1c0000

func main() {
	if len(os.Args) < 2 {
		webMode(":8080")
		return
	}

	switch os.Args[1] {
	case "read":
		readMode()
	case "read-cmd":
		if len(os.Args) < 3 {
			fmt.Println("用法: asr-flash read-cmd <command>")
			fmt.Println("示例: asr-flash read-cmd getvar:product")
			os.Exit(1)
		}
		readCmdMode(os.Args[2])
	case "read-partition":
		if len(os.Args) < 3 {
			fmt.Println("用法: asr-flash read-partition <partition> [size_hex] [outfile]")
			fmt.Println("示例: asr-flash read-partition bootloader 24000 /tmp/boot.bin")
			fmt.Println("      asr-flash read-partition bootloader /tmp/boot.bin")
			os.Exit(1)
		}
		outFile := ""
		sizeHex := ""
		for _, a := range os.Args[3:] {
			if strings.HasSuffix(a, ".bin") || strings.HasSuffix(a, ".img") || strings.HasPrefix(a, "/") {
				outFile = a
			} else {
				sizeHex = a
			}
		}
		readPartitionMode(os.Args[2], sizeHex, outFile)
	case "upload":
		uploadMode(os.Args[2:])
	case "verify-lcd":
		verifyLCDMode()
	case "flash-quecpython":
		if len(os.Args) < 3 {
			fmt.Println("用法: asr-flash flash-quecpython <firmware_zip>")
			fmt.Println("示例: asr-flash flash-quecpython QPY_OCPU_V0002_EC600N_CNLF_FW.bin")
			os.Exit(1)
		}
		flashQuecPython(os.Args[2])
	case "flash-logicrom":
		if len(os.Args) < 3 {
			fmt.Println("用法: asr-flash flash-logicrom <firmware_zip> [--app-only] [--no-sparse]")
			fmt.Println("示例: asr-flash flash-logicrom heyptt-logicrom.zip")
			fmt.Println("      asr-flash flash-logicrom heyptt-logicrom.zip --app-only  仅烧录 app 分区（增量更新）")
			fmt.Println("      asr-flash flash-logicrom logicrom_core.zip --no-sparse   禁用 sparse 自动转换（调试用）")
			os.Exit(1)
		}
		appOnly := false
		autoSparse := true
		for _, arg := range os.Args[3:] {
			switch arg {
			case "--app-only":
				appOnly = true
			case "--no-sparse":
				autoSparse = false
			}
		}
		flashLogicrom(os.Args[2], appOnly, autoSparse)
	case "flash-watch":
		// 内置监控模式：轮询检测设备，一旦出现立即刷写，处理稍纵即逝的接口窗口
		if len(os.Args) < 3 {
			fmt.Println("用法: asr-flash flash-watch <firmware_zip> [--retry] [--interval-ms <ms>] [--no-sparse] [--timeout <秒>]")
			fmt.Println("示例: asr-flash flash-watch heyptt-nvm-only.zip")
			fmt.Println("      asr-flash flash-watch heyptt-nvm-only.zip --retry         刷写失败后持续重试")
			fmt.Println("      asr-flash flash-watch heyptt-nvm-only.zip --interval-ms 50  轮询间隔（默认50ms）")
			fmt.Println("      asr-flash flash-watch heyptt-nvm-only.zip --timeout 180  监控超时(默认300秒)")
			fmt.Println("      asr-flash flash-watch heyptt-full-cp.zip --no-sparse      用 raw 分段下载（cp.bin 等大文件）")
			os.Exit(1)
		}
		retry := false
		intervalMs := 50
		autoSparse := true
		timeoutSec := 300 // 默认5分钟
		for i := 2; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--retry":
				retry = true
			case "--no-sparse":
				autoSparse = false
			case "--interval-ms":
				if i+1 < len(os.Args) {
					fmt.Sscanf(os.Args[i+1], "%d", &intervalMs)
					i++
				}
			case "--timeout":
				if i+1 < len(os.Args) {
					fmt.Sscanf(os.Args[i+1], "%d", &timeoutSec)
					i++
				}
			}
		}
		flashWatchMode(os.Args[2], retry, intervalMs, autoSparse, timeoutSec)
	case "flash-app":
		if len(os.Args) < 3 {
			fmt.Println("用法: asr-flash flash-app <app.bin>")
			fmt.Println("示例: asr-flash flash-app build/app.bin  仅烧录单个 app 镜像到 user_app 分区")
			os.Exit(1)
		}
		flashAppOnly(os.Args[2])
	case "version", "--version", "-v":
		fmt.Println("ASR Flash Tool v4.1 - 对讲机固件烧录平台")
		fmt.Println("  支持芯片: ASR CRANE, NXP Kinetis, GD32, ESP32")
		fmt.Println("  烧录方式: SMUX USB, UART, SWD, JTAG")
		fmt.Println("  高级功能: 批量烧录, 远程烧录, 脚本扩展")
	case "help", "--help", "-h":
		fmt.Println("用法:")
		fmt.Println("  asr-flash                          Web模式 :8080 (烧录+读取+上传+调试)")
		fmt.Println("  asr-flash <firmware_dir>           CLI烧录 (CRANE 格式)")
		fmt.Println("  asr-flash flash-quecpython <zip>   烧录 QuecPython 固件包")
		fmt.Println("  asr-flash flash-logicrom <zip>     烧录 Logicrom 固件包 (全量)")
		fmt.Println("  asr-flash flash-logicrom <zip> --app-only  仅烧录 app 分区 (增量)")
		fmt.Println("  asr-flash flash-watch <zip> [--retry]  内置监控：检测到设备即刷 (处理稍纵即逝接口)")
		fmt.Println("  asr-flash flash-app <app.bin>      仅烧录单个 app 镜像到 user_app 分区")
		fmt.Println("  asr-flash upload <file> [remote]   上传文件")
		fmt.Println("  asr-flash verify-lcd               一键验证LCD分辨率")
		fmt.Println("  asr-flash read / read-cmd <cmd>    读取模式")
	default:
		cliMode()
	}
}

func webMode(addr string) {
	fmt.Println("=== ASR Flash Tool (Web Mode) ===")
	fmt.Println()
	StartWebServer(addr)
}

func readMode() {
	fmt.Println("=== ASR Flash Read Mode ===")
	fmt.Println()

	fmt.Println("步骤 1: 扫描设备...")
	info, err := FindQuectelDevice()
	if err != nil {
		fmt.Printf("未找到设备: %v\n", err)
		os.Exit(1)
	}

	if info.Mode != "download" {
		fmt.Printf("设备不在下载模式: %s\n", info.Mode)
		fmt.Println("请先让设备进入下载模式")
		os.Exit(1)
	}

	fmt.Printf("设备已在下载模式: %s\n", info.Path)
	fd, err := OpenUSBDevice(info.Path)
	if err != nil {
		fmt.Printf("打开设备失败: %v\n", err)
		os.Exit(1)
	}

	session := NewSession(fd)
	session.OnLog = func(msg string) {
		fmt.Println(msg)
	}

	defer session.Close()

	fmt.Println()
	fmt.Println("步骤 2: 设置 USB 接口...")
	err = ClaimInterface(session.FD(), 1)
	if err != nil {
		fmt.Printf("声明接口失败: %v\n", err)
		os.Exit(1)
	}
	defer ReleaseInterface(session.FD(), 1)
	fmt.Println("已声明接口 1")

	fmt.Println()
	fmt.Println("步骤 3: SMUX 握手...")
	err = session.SmuxHandshake()
	if err != nil {
		fmt.Printf("SMUX 握手失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("SMUX 握手成功!")

	fmt.Println()
	fmt.Println("步骤 4: 获取设备信息...")
	
	// Get all variables
	rsp, err := session.SmuxSendCmd("getvar:all")
	if err != nil {
		fmt.Printf("getvar:all 失败: %v\n", err)
	} else {
		fmt.Printf("getvar:all 响应:\n%s\n", rsp)
	}
	
	// Get specific variables
	vars := []string{"product", "version", "serialno", "max-download-size", "battery-voltage", "battery-pct"}
	for _, v := range vars {
		rsp, err := session.SmuxSendCmd("getvar:" + v)
		if err != nil {
			fmt.Printf("getvar:%s: 错误 %v\n", v, err)
		} else {
			fmt.Printf("getvar:%s: %s\n", v, rsp)
		}
	}

	fmt.Println()
	fmt.Println("步骤 5: 尝试读取命令...")
	
	// Try various read-related commands
	commands := []string{
		// Standard fastboot read commands
		"flash:read:boot",
		"flash:read:system",
		"oem flash_read:boot",
		"oem flash_read:system",
		"oem read:boot",
		"read:boot",
		"dump:boot",
		"oem dump:boot",
		// ASR-specific commands
		"oem getvar:flash_size",
		"oem getvar:flash_type",
		"oem flashinfo",
		"oem partition_list",
		"oem partinfo",
		"oem mrdump",
		"oem boot_mode",
		"oem version",
		"oem product",
		"oem hw",
		"oem lcd",
		"oem display",
		"oem gpio",
		"oem spi",
		"oem pin",
		// Try download with size
		"download:0",
		"download:1",
	}
	
	for _, cmd := range commands {
		fmt.Printf("\n  尝试 %s...\n", cmd)
		rsp, err := session.SmuxSendCmd(cmd)
		if err != nil {
			fmt.Printf("    错误: %v\n", err)
		} else {
			fmt.Printf("    响应: %s", rsp)
			if len(rsp) > 100 {
				fmt.Printf("... (%d bytes)", len(rsp))
			}
			fmt.Println()
		}
	}

	fmt.Println()
	fmt.Println("=== 测试完成 ===")
}

func readCmdMode(cmd string) {
	fmt.Printf("=== ASR Flash Read Command: %s ===\n", cmd)
	fmt.Println()

	fmt.Println("扫描设备...")
	info, err := FindQuectelDevice()
	if err != nil {
		fmt.Printf("未找到设备: %v\n", err)
		os.Exit(1)
	}

	if info.Mode != "download" {
		fmt.Printf("设备不在下载模式: %s\n", info.Mode)
		os.Exit(1)
	}

	fmt.Printf("设备: %s\n", info.Path)
	fd, err := OpenUSBDevice(info.Path)
	if err != nil {
		fmt.Printf("打开设备失败: %v\n", err)
		os.Exit(1)
	}

	session := NewSession(fd)
	session.OnLog = func(msg string) {
		fmt.Println(msg)
	}
	defer session.Close()

	err = ClaimInterface(session.FD(), 1)
	if err != nil {
		fmt.Printf("声明接口失败: %v\n", err)
		os.Exit(1)
	}
	defer ReleaseInterface(session.FD(), 1)

	err = session.SmuxHandshake()
	if err != nil {
		fmt.Printf("SMUX 握手失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n执行: %s\n", cmd)
	rsp, err := session.SmuxSendCmd(cmd)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n响应:\n%s\n", rsp)
}

// readPartitionMode 读回指定分区并保存到文件
func readPartitionMode(partition, sizeHex, outFile string) {
	fmt.Printf("=== 读回分区: %s ===\n", partition)
	fmt.Println()

	// 扫描设备
	info, err := FindQuectelDevice()
	if err != nil {
		fmt.Printf("未找到设备: %v\n", err)
		os.Exit(1)
	}
	if info.Mode != "download" {
		fmt.Printf("设备不在下载模式: %s\n", info.Mode)
		os.Exit(1)
	}
	fmt.Printf("设备: %s\n", info.Path)

	fd, err := OpenUSBDevice(info.Path)
	if err != nil {
		fmt.Printf("打开设备失败: %v\n", err)
		os.Exit(1)
	}
	session := NewSession(fd)
	session.OnLog = func(msg string) {
		fmt.Println(msg)
	}
	defer session.Close()

	if err := ClaimInterface(session.FD(), 1); err != nil {
		fmt.Printf("声明接口失败: %v\n", err)
		os.Exit(1)
	}
	defer ReleaseInterface(session.FD(), 1)

	// SMUX 握手
	fmt.Println("SMUX 握手...")
	if err := session.SmuxHandshake(); err != nil {
		fmt.Printf("SMUX 握手失败: %v\n", err)
		os.Exit(1)
	}
	time.Sleep(500 * time.Millisecond)

	// 先加载 preboot + flasher 进入 flasher 阶段（读回需要 flasher 环境）
	fmt.Println("\n进入 flasher 阶段...")
	prebootData, err := os.ReadFile("/tmp/qpy_fw/extracted/preboot.img")
	if err != nil {
		fmt.Printf("读取 preboot.img 失败: %v\n", err)
		os.Exit(1)
	}
	if err := abootDownload(session, prebootData); err != nil {
		fmt.Printf("下载 preboot 失败: %v\n", err)
		os.Exit(1)
	}
	rsp, err := session.SmuxSendCmd("verify")
	if err != nil {
		fmt.Printf("verify 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("verify: %s\n", rsp)
	rsp, err = session.SmuxSendCmd("call")
	if err != nil {
		fmt.Printf("call preboot 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("call: %s\n", rsp)
	time.Sleep(3 * time.Second)

	// 重新握手（preboot 启动后）
	if err := session.SmuxHandshake(); err != nil {
		fmt.Printf("重握手失败: %v\n", err)
		os.Exit(1)
	}
	time.Sleep(500 * time.Millisecond)

	flasherData, err := os.ReadFile("/tmp/qpy_fw/extracted/flasher.img")
	if err != nil {
		fmt.Printf("读取 flasher.img 失败: %v\n", err)
		os.Exit(1)
	}
	if err := abootDownload(session, flasherData); err != nil {
		fmt.Printf("下载 flasher 失败: %v\n", err)
		os.Exit(1)
	}
	rsp, err = session.SmuxSendCmd("verify")
	if err != nil {
		fmt.Printf("verify flasher 失败: %v\n", err)
		os.Exit(1)
	}
	rsp, err = session.SmuxSendCmd("call")
	if err != nil {
		fmt.Printf("call flasher 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("call flasher: %s\n", rsp)
	time.Sleep(3 * time.Second)
	if err := session.SmuxHandshake(); err != nil {
		fmt.Printf("重握手失败: %v\n", err)
		os.Exit(1)
	}
	time.Sleep(500 * time.Millisecond)

	// 发送 flash:read:<partition> 读回命令
	readCmd := fmt.Sprintf("flash:read:%s", partition)
	fmt.Printf("\n发送: %s\n", readCmd)
	session.SmuxSendCmd(readCmd)

	// 等待 DATA 响应（含大小）
	err = session.waitForResponse(10000, func() bool {
		return strings.HasPrefix(session.cmdResponse, "DATA") ||
			strings.HasPrefix(session.cmdResponse, "OKAY") ||
			strings.HasPrefix(session.cmdResponse, "FAIL")
	})
	if err != nil {
		fmt.Printf("等待读回响应失败: %v\n", err)
		os.Exit(1)
	}

	session.mu.Lock()
	rsp = session.cmdResponse
	session.mu.Unlock()
	fmt.Printf("读回响应: %s\n", rsp)

	if strings.HasPrefix(rsp, "DATA") {
		sizeStr := strings.TrimPrefix(rsp, "DATA")
		var size int
		fmt.Sscanf(sizeStr, "%x", &size)
		fmt.Printf("数据大小: %d bytes (0x%x)\n", size, size)

		// 接收数据
		session.BeginDataExpect(size)
		data, err := session.ReceiveData(60000)
		if err != nil {
			fmt.Printf("接收数据失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("收到 %d bytes\n", len(data))

		// 等待 OKAY 完成
		session.waitForResponse(10000, func() bool {
			return strings.HasPrefix(session.cmdResponse, "OKAY") ||
				strings.HasPrefix(session.cmdResponse, "FAIL")
		})

		if outFile == "" {
			outFile = fmt.Sprintf("/tmp/%s_readback.bin", partition)
		}
		if err := os.WriteFile(outFile, data, 0644); err != nil {
			fmt.Printf("保存文件失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ 分区已保存到: %s (%d bytes)\n", outFile, len(data))
	} else if strings.HasPrefix(rsp, "OKAY") {
		// OKAY 后接收数据（ASR 可能直接返回数据帧）
		fmt.Println("收到 OKAY，尝试接收数据帧...")

		// 诊断模式：先不设大小，直接接收所有数据帧直到超时，看设备是否发送数据
		session.BeginDataExpect(0xFFFFFF) // 大上限
		diagData, diagErr := session.ReceiveData(5000)
		if diagErr != nil {
			fmt.Printf("  [诊断] 5秒内未收到数据帧: %v\n", diagErr)
		} else {
			fmt.Printf("  [诊断] 收到 %d bytes 数据!\n", len(diagData))
		}

		var size int
		if sizeHex != "" {
			fmt.Sscanf(sizeHex, "%x", &size)
		} else {
			// 尝试查询分区大小
			fmt.Println("未指定大小，尝试 getvar 查询分区大小...")
			// 常见分区大小（字节）
			sizes := map[string]int{
				"bootloader": 147456,
				"dsp":        2220032,
				"fwcerts":    16384,
				"rd":         131072,
				"apn":        49152,
				"rfbin":      65536,
				"logo":       237568,
				"nvm":        917504,
				"updater":    245760,
			}
			if s, ok := sizes[partition]; ok {
				size = s
				fmt.Printf("使用已知分区大小: %d bytes (0x%x)\n", size, size)
			} else {
				fmt.Printf("未知分区大小: %s，请指定 size_hex\n", partition)
				os.Exit(1)
			}
		}

		session.BeginDataExpect(size)
		data, err := session.ReceiveData(90000)
		if err != nil {
			fmt.Printf("接收数据失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("收到 %d bytes\n", len(data))

		if outFile == "" {
			outFile = fmt.Sprintf("/tmp/%s_readback.bin", partition)
		}
		if err := os.WriteFile(outFile, data, 0644); err != nil {
			fmt.Printf("保存文件失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ 分区已保存到: %s (%d bytes)\n", outFile, len(data))
	} else if strings.HasPrefix(rsp, "FAIL") {
		fmt.Printf("读回失败: %s\n", rsp)
		os.Exit(1)
	} else {
		fmt.Printf("分区为空或读回不支持: %s\n", rsp)
	}
}

func cliMode() {
	fwDir := os.Args[1]
	var manualPath string
	if len(os.Args) >= 3 {
		manualPath = os.Args[2]
	}

	fmt.Println("=== ASR Flash Tool (CLI Mode) ===")
	fmt.Println()

	var fd int
	var err error

	if manualPath != "" {
		fmt.Printf("使用手动设备: %s\n", manualPath)
		fd, err = OpenUSBDevice(manualPath)
		if err != nil {
			fmt.Printf("打开设备失败: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("步骤 1: 扫描设备...")
		info, err := FindQuectelDevice()
		if err != nil {
			fmt.Printf("未找到设备: %v\n", err)
			os.Exit(1)
		}

		if info.Mode == "download" {
			fmt.Printf("设备已在下载模式: %s\n", info.Path)
			fd, err = OpenUSBDevice(info.Path)
			if err != nil {
				fmt.Printf("打开设备失败: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Printf("设备在正常模式: %s (bus=%d, addr=%d)\n", info.Serial, info.Bus, info.Addr)
			fmt.Println()
			fmt.Println("步骤 2: 发送 AT+QDownLOAD=1...")
			err = SendATDownload(info.Bus, info.Addr)
			if err != nil {
				fmt.Printf("AT 命令失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("已发送 AT+QDownLOAD=1")
			fmt.Println()
			fmt.Println("步骤 3: 等待下载模式设备...")
			dlInfo, err := WaitForDownloadMode(30)
			if err != nil {
				fmt.Printf("等待超时: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("下载模式设备: %s\n", dlInfo.Path)
			fd, err = OpenUSBDevice(dlInfo.Path)
			if err != nil {
				fmt.Printf("打开设备失败: %v\n", err)
				os.Exit(1)
			}
		}
	}

	session := NewSession(fd)
	session.OnLog = func(msg string) {
		fmt.Println(msg)
	}

	defer session.Close()

	fmt.Println()
	fmt.Println("步骤 4: 设置 USB 接口...")
	err = ClaimInterface(session.FD(), 1)
	if err != nil {
		fmt.Printf("声明接口失败: %v\n", err)
		os.Exit(1)
	}
	defer ReleaseInterface(session.FD(), 1)
	fmt.Println("已声明接口 1")

	fmt.Println()
	fmt.Println("步骤 5: SMUX 握手...")
	err = session.SmuxHandshake()
	if err != nil {
		fmt.Printf("SMUX 握手失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("SMUX 握手成功!")

	fmt.Println()
	fmt.Println("步骤 6: 读取固件...")
	fwPath := filepath.Join(fwDir, "firmware.bin")
	if _, err := os.Stat(fwPath); os.IsNotExist(err) {
		fmt.Printf("错误: 找不到 %s\n", fwPath)
		fmt.Println("请确保固件目录包含 firmware.bin 文件")
		os.Exit(1)
	}

	fw, err := ParseCraneFirmware(fwPath, func(format string, args ...interface{}) {
		fmt.Printf("  "+format+"\n", args...)
	})
	if err != nil {
		fmt.Printf("解析固件失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("固件命令数: %d\n", len(fw.Commands))

	fmt.Println()
	fmt.Println("步骤 7: 下载固件...")
	err = DownloadCraneFirmware(session, fw)
	if err != nil {
		fmt.Printf("下载失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("=== 烧录完成! ===")
}

type DownloadCommand struct {
	Command        string      `json:"command"`
	Group          string      `json:"group"`
	Name           string      `json:"name,omitempty"`
	Value          interface{} `json:"value,omitempty"`
	Image          string      `json:"image,omitempty"`
	Partition      string      `json:"partition,omitempty"`
	Weight         int         `json:"weight,omitempty"`
	ProductionOnly bool        `json:"productionOnly,omitempty"`
}

func parseFirmwareZip(zipPath string) (map[string][]byte, []DownloadCommand, error) {
	return parseFirmwareZipWithSparse(zipPath, true)
}

func parseFirmwareZipWithSparse(zipPath string, autoSparse bool) (map[string][]byte, []DownloadCommand, error) {
	extractDir := "/tmp/flash_fw_" + filepath.Base(zipPath)
	os.RemoveAll(extractDir)
	os.MkdirAll(extractDir, 0755)

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, fmt.Errorf("打开固件包失败: %v", err)
	}
	defer reader.Close()

	var commands []DownloadCommand

	files := make(map[string][]byte)

	fmt.Printf("解压固件包: %s\n", zipPath)
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		rc, err := file.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("打开文件 %s 失败: %v", file.Name, err)
		}

		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("读取文件 %s 失败: %v", file.Name, err)
		}

		if file.Name == "download.json" {
			if err := json.Unmarshal(data, &commands); err != nil {
				return nil, nil, fmt.Errorf("解析 download.json 失败: %v", err)
			}
		}

		// 存储文件数据
		filePath := filepath.Join(extractDir, file.Name)
		os.MkdirAll(filepath.Dir(filePath), 0755)
		os.WriteFile(filePath, data, 0644)
		files[file.Name] = data
	}

	fmt.Printf("解压完成，共 %d 个文件\n", len(files))

	// 自动转换 raw 镜像为 sparse 格式（flasher 只接受 sparse）
	if autoSparse {
		files, commands = convertZipToSparse(files, commands)
	} else {
		fmt.Println("    [sparse] 已禁用自动转换 (--no-sparse)")
	}

	return files, commands, nil
}

func executeFlashCommands(session *Session, files map[string][]byte, commands []DownloadCommand) error {
	// 按组处理命令
	groups := make(map[string][]DownloadCommand)
	for _, cmd := range commands {
		groups[cmd.Group] = append(groups[cmd.Group], cmd)
	}

	// 扫描设备
	fmt.Println("步骤 1: 扫描设备...")
	info, err := FindQuectelDevice()
	if err != nil {
		return fmt.Errorf("未找到设备: %v", err)
	}

	if info.Mode != "download" {
		return fmt.Errorf("设备不在下载模式: %s，请先让设备进入下载模式", info.Mode)
	}

	fmt.Printf("设备已在下载模式: %s\n", info.Path)
	fd, err := OpenUSBDevice(info.Path)
	if err != nil {
		return fmt.Errorf("打开设备失败: %v", err)
	}

	session = NewSession(fd)
	session.OnLog = func(msg string) {
		fmt.Println(msg)
	}
	defer session.Close()

	// 设置 USB 接口
	fmt.Println("步骤 2: 设置 USB 接口...")
	err = ClaimInterface(session.FD(), 1)
	if err != nil {
		return fmt.Errorf("声明接口失败: %v", err)
	}
	defer ReleaseInterface(session.FD(), 1)
	fmt.Println("已声明接口 1")

	// SMUX 握手
	fmt.Println("步骤 3: SMUX 握手...")
	err = session.SmuxHandshake()
	if err != nil {
		return fmt.Errorf("SMUX 握手失败: %v", err)
	}
	fmt.Println("SMUX 握手成功!")

	// 按组处理命令
	for i := 0; ; i++ {
		groupKey := fmt.Sprintf("%d", i)
		cmds, ok := groups[groupKey]
		if !ok {
			break
		}

		fmt.Printf("\n=== Group %d (%d commands) ===\n", i, len(cmds))

		for _, cmd := range cmds {
			switch cmd.Command {
			case "require":
				fmt.Printf("  Require: %s=%s\n", cmd.Name, cmd.Value)

			case "progress":
				fmt.Printf("  Progress weight: %d\n", cmd.Weight)

			case "call":
				// ABOOT 协议: download + data + verify + call
				imageData, ok := files[cmd.Image]
				if !ok {
					return fmt.Errorf("找不到镜像文件: %s", cmd.Image)
				}
				fmt.Printf("  Call: %s (%d bytes)\n", cmd.Image, len(imageData))

				// Step 1: upload image data
				if err := abootDownload(session, imageData); err != nil {
					return fmt.Errorf("下载 %s 失败: %v", cmd.Image, err)
				}

				// Step 2: verify image
				fmt.Printf("    verify...\n")
				rsp, err := session.SmuxSendCmd("verify")
				if err != nil {
					return fmt.Errorf("verify %s 失败: %v", cmd.Image, err)
				}
				fmt.Printf("    verify 响应: %s\n", rsp)

				// Step 3: call (execute)
				fmt.Printf("    call...\n")
				rsp, err = session.SmuxSendCmd("call")
				if err != nil {
					return fmt.Errorf("call %s 失败: %v", cmd.Image, err)
				}
				fmt.Printf("    call 响应: %s\n", rsp)

				// 检测 call 失败（ERR/Exception）
				if strings.Contains(rsp, "ERR") || strings.Contains(rsp, "Exception") ||
					strings.Contains(rsp, "FAIL") || strings.Contains(rsp, "error") {
					fmt.Printf("    [警告] call %s 返回异常: %q\n", cmd.Image, rsp)
					// 尝试恢复：等待 + 重新握手，看设备是否进入可用状态
					fmt.Printf("    等待设备恢复...\n")
					time.Sleep(2 * time.Second)
					if err := session.SmuxHandshake(); err != nil {
						fmt.Printf("    重握手失败: %v（设备可能已断开）\n", err)
					} else {
						fmt.Printf("    重握手成功\n")
						time.Sleep(500 * time.Millisecond)
					}
					// preboot call 失败，无法进入 flasher 阶段，返回错误让上层重试
					return fmt.Errorf("call %s 执行异常: %v", cmd.Image, rsp)
				}

				// Step 4: wait for device to re-initialize after call
				fmt.Printf("    等待设备重新初始化...\n")
				time.Sleep(200 * time.Millisecond)

				// Step 5: re-handshake after call
				fmt.Printf("    重新 SMUX 握手...\n")
				err = session.SmuxHandshake()
				if err != nil {
					fmt.Printf("    警告: 重握手失败: %v，继续...\n", err)
				} else {
					fmt.Printf("    重握手成功\n")
				}

				// Step 6: query max-download-size to verify stage transition
				rsp, err = session.SmuxSendCmd("getvar:max-download-size")
				if err != nil {
					fmt.Printf("    警告: getvar:max-download-size 失败: %v\n", err)
				} else {
					fmt.Printf("    max-download-size: %s\n", rsp)
				}

			case "partition":
				// ABOOT 协议: download + data + partition
				imageData, ok := files[cmd.Image]
				if !ok {
					return fmt.Errorf("找不到镜像文件: %s", cmd.Image)
				}
				fmt.Printf("  Partition: %s (%d bytes)\n", cmd.Image, len(imageData))

				// Step 1: upload image data
				if err := abootDownload(session, imageData); err != nil {
					fmt.Printf("    警告: 下载 %s 失败: %v，重握手恢复并跳过...\n", cmd.Image, err)
					rehandshake(session)
					continue
				}

				// Step 2: send partition command
				fmt.Printf("    partition...\n")
				rsp, err := session.SmuxSendCmd("partition")
				if err != nil {
					fmt.Printf("    警告: partition %s 失败: %v，重握手恢复并跳过...\n", cmd.Image, err)
					rehandshake(session)
					continue
				}
				fmt.Printf("    partition 响应: %s\n", rsp)
				if strings.HasPrefix(rsp, "FAIL") {
					fmt.Printf("    警告: partition 失败，重握手恢复并跳过...\n")
					rehandshake(session)
					continue
				}

			case "erase":
				fmt.Printf("  Erase: %s (weight: %d)\n", cmd.Partition, cmd.Weight)
				if err := erasePartition(session, cmd.Partition); err != nil {
					fmt.Printf("    警告: 擦除 %s 失败: %v\n", cmd.Partition, err)
					rehandshake(session)
				}

			case "flash":
				// ABOOT 协议: download + data + flash:<partition>
				imageData, ok := files[cmd.Image]
				if !ok {
					return fmt.Errorf("找不到镜像文件: %s", cmd.Image)
				}

				// partition.bin 在 partition 命令用 raw，但在 flash:ptable 需要 sparse
				// （flasher 的 flash: 命令要求 sparse 格式）
				if cmd.Image == "partition.bin" {
					if !isSparseFormat(imageData) {
						sparseData, err := rawToSparse(imageData, 0)
						if err != nil {
							return fmt.Errorf("partition.bin sparse 转换失败: %v", err)
						}
						fmt.Printf("  [sparse] partition.bin -> %d bytes (flash:ptable 需要 sparse)\n", len(sparseData))
						imageData = sparseData
					}
				}

				fmt.Printf("  Flash: %s -> %s (%d bytes)\n", cmd.Image, cmd.Partition, len(imageData))

				// 如果数据超过 max-download-size，使用分段下载
				if len(imageData) > maxDownloadSize {
					fmt.Printf("    [分段] 数据 %d bytes 超过 max-download-size %d，使用分段下载...\n",
						len(imageData), maxDownloadSize)
					if err := flashSegmented(session, imageData, cmd.Partition); err != nil {
						fmt.Printf("    警告: 分段下载 %s 失败: %v，重握手恢复并跳过...\n", cmd.Image, err)
						rehandshake(session)
						continue
					}
					continue
				}

				// Step 1: upload image data
				if err := abootDownload(session, imageData); err != nil {
					fmt.Printf("    警告: 下载 %s 失败: %v，重握手恢复并跳过...\n", cmd.Image, err)
					rehandshake(session)
					continue
				}

				// Step 2: send flash command
				flashCmd := fmt.Sprintf("flash:%s", cmd.Partition)
				fmt.Printf("    %s...\n", flashCmd)
				rsp, err := session.SmuxSendCmd(flashCmd)
				if err != nil {
					fmt.Printf("    警告: %s 失败: %v，重握手恢复并跳过...\n", flashCmd, err)
					rehandshake(session)
					continue
				}
				fmt.Printf("    %s 响应: %s\n", flashCmd, rsp)
				if strings.HasPrefix(rsp, "FAIL") {
					fmt.Printf("    警告: %s 失败，重握手恢复并跳过...\n", flashCmd)
					rehandshake(session)
					// 跳过此条命令，继续下一条
					continue
				}

			default:
				fmt.Printf("  Unknown command: %s\n", cmd.Command)
			}
		}
	}

	fmt.Println("\n=== 烧录完成! ===")
	return nil
}

func flashAppOnly(appPath string) {
	fmt.Printf("=== 仅烧录 App 分区: %s ===\n", appPath)
	fmt.Println()

	data, err := os.ReadFile(appPath)
	if err != nil {
		fmt.Printf("读取 app.bin 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("App 大小: %d bytes\n", len(data))

	fmt.Println("步骤 1: 扫描设备...")
	info, err := FindQuectelDevice()
	if err != nil {
		fmt.Printf("未找到设备: %v\n", err)
		os.Exit(1)
	}

	if info.Mode != "download" {
		fmt.Printf("设备不在下载模式: %s\n", info.Mode)
		os.Exit(1)
	}

	fmt.Printf("设备已在下载模式: %s\n", info.Path)
	fd, err := OpenUSBDevice(info.Path)
	if err != nil {
		fmt.Printf("打开设备失败: %v\n", err)
		os.Exit(1)
	}

	session := NewSession(fd)
	session.OnLog = func(msg string) {
		fmt.Println(msg)
	}
	defer session.Close()

	fmt.Println("步骤 2: 设置 USB 接口...")
	err = ClaimInterface(session.FD(), 1)
	if err != nil {
		fmt.Printf("声明接口失败: %v\n", err)
		os.Exit(1)
	}
	defer ReleaseInterface(session.FD(), 1)
	fmt.Println("已声明接口 1")

	fmt.Println("步骤 3: SMUX 握手...")
	err = session.SmuxHandshake()
	if err != nil {
		fmt.Printf("SMUX 握手失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("SMUX 握手成功!")
	// 握手后等待设备就绪
	time.Sleep(500 * time.Millisecond)

	fmt.Println("步骤 4: 发送 app.bin 到 user_app 分区...")
	if err := sendImageDataWithRetry(session, data, 3); err != nil {
		fmt.Printf("发送 app.bin 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n=== App 烧录完成! ===")
}

func flashQuecPython(zipPath string) {
	fmt.Printf("=== 烧录 QuecPython 固件: %s ===\n", zipPath)
	fmt.Println()

	// 原厂 QPY 固件含 raw cp.bin，需分段下载，禁用 sparse（flasher 接受 raw）
	files, commands, err := parseFirmwareZipWithSparse(zipPath, false)
	if err != nil {
		fmt.Printf("解析固件包失败: %v\n", err)
		os.Exit(1)
	}

	if len(commands) == 0 {
		fmt.Println("download.json 为空或解析失败")
		os.Exit(1)
	}

	if err := executeFlashCommands(nil, files, commands); err != nil {
		fmt.Printf("烧录失败: %v\n", err)
		os.Exit(1)
	}
}

func flashLogicrom(zipPath string, appOnly bool, autoSparse bool) {
	if appOnly {
		fmt.Printf("=== 烧录 Logicrom 固件 (仅 app 分区): %s ===\n", zipPath)
	} else {
		fmt.Printf("=== 烧录 Logicrom 固件: %s ===\n", zipPath)
	}
	fmt.Println()

	files, commands, err := parseFirmwareZipWithSparse(zipPath, autoSparse)
	if err != nil {
		fmt.Printf("解析固件包失败: %v\n", err)
		os.Exit(1)
	}

	if len(commands) == 0 {
		fmt.Println("download.json 为空或解析失败")
		os.Exit(1)
	}

	// 增量模式：仅保留 app.bin 相关的 flash 命令 + preboot/flasher 升级 max-download-size
	if appOnly {
		var filtered []DownloadCommand
		bootFound := false
		flasherFound := false
		for _, c := range commands {
			// 保留 preboot 和 flasher 的 call 命令以提升 max-download-size 到 1MB
			if c.Command == "call" {
				if c.Image == "preboot.img" && !bootFound {
					filtered = append(filtered, c)
					bootFound = true
					continue
				}
				if c.Image == "flasher.img" && !flasherFound {
					filtered = append(filtered, c)
					flasherFound = true
					continue
				}
			}
			if c.Command == "flash" && c.Image == "app.bin" {
				filtered = append(filtered, c)
			} else if c.Command == "require" || c.Command == "progress" {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 {
			// 如果 zip 中直接是 app.bin，没有 download.json，走 flashAppOnly 逻辑
			fmt.Println("未找到 app.bin 条目，尝试直接烧录 app.bin")
		} else {
			commands = filtered
			fmt.Printf("增量模式：仅烧录 %d 条命令（preboot+flasher+app.bin）\n", len(commands))
		}
	}

	if err := executeFlashCommands(nil, files, commands); err != nil {
		fmt.Printf("烧录失败: %v\n", err)
		os.Exit(1)
	}
}

// flashWatchMode 内置监控模式：轮询检测设备，设备一出现立即刷写固件包
func flashWatchMode(zipPath string, retry bool, intervalMs int, autoSparse bool, timeoutSec int) {
	fmt.Printf("=== ASR Flash Watch Mode ===\n")
	fmt.Printf("固件包: %s\n", zipPath)
	fmt.Printf("轮询间隔: %d ms\n", intervalMs)
	fmt.Printf("监控超时: %d 秒\n", timeoutSec)
	if retry {
		fmt.Printf("模式: 持续监控，刷写失败自动重试\n")
	}
	if !autoSparse {
		fmt.Printf("sparse 自动转换: 已禁用 (--no-sparse)\n")
	}
	fmt.Println()

	// 预解析固件包（含 sparse 转换），后续每次刷写直接复用
	files, commands, err := parseFirmwareZipWithSparse(zipPath, autoSparse)
	if err != nil {
		fmt.Printf("解析固件包失败: %v\n", err)
		os.Exit(1)
	}
	if len(commands) == 0 {
		fmt.Println("download.json 为空或解析失败")
		os.Exit(1)
	}
	fmt.Printf("已解析固件包: %d 个文件, %d 条命令\n", len(files), len(commands))
	fmt.Println()

	fmt.Println("等待设备进入下载模式 (2ecc:3004)...")
	fmt.Println("按 Ctrl+C 停止监控")
	fmt.Println()

	attempt := 0
	lastStatus := time.Now()
	startTime := time.Now()
	for {
		// 检查超时
		if timeoutSec > 0 && time.Since(startTime) > time.Duration(timeoutSec)*time.Second {
			fmt.Printf("\n[超时] 监控已运行 %d 秒，未检测到设备，退出。\n", timeoutSec)
			os.Exit(1)
		}
		// 扫描设备
		info, err := FindQuectelDevice()
		if err == nil && info != nil && info.Mode == "download" {
			attempt++
			ts := time.Now().Format("15:04:05")
			fmt.Printf("\n[%s] 检测到下载模式设备 (%s)，开始刷写 (attempt %d)...\n", ts, info.Path, attempt)

			// 立即执行刷写
			flashErr := executeFlashCommands(nil, files, commands)

			if flashErr == nil {
				fmt.Printf("\n[%s] === 刷写成功! (attempt %d) ===\n", time.Now().Format("15:04:05"), attempt)
				if !retry {
					fmt.Println("监控完成。设备应已刷入固件。")
					return
				}
				// retry 模式下，成功后继续监控（等待下次设备）
				fmt.Println("持续监控中，等待设备再次出现...")
				time.Sleep(3 * time.Second)
			} else {
				fmt.Printf("\n[%s] 刷写失败: %v\n", time.Now().Format("15:04:05"), flashErr)
				if !retry {
					fmt.Printf("刷写失败，退出。如需自动重试请使用 --retry。\n")
					os.Exit(1)
				}
				// 尝试 USB 复位设备，强制其重新枚举进入干净 bootrom，
				// 避免在脏设备上反复刷写失败。
				if strings.Contains(flashErr.Error(), "device abort") ||
					strings.Contains(flashErr.Error(), "timeout") {
					fmt.Println("检测到设备脏状态，尝试 USB 复位重新枚举...")
					if info != nil && info.Path != "" {
						if err := ResetUSBDevice(info.Path); err != nil {
							fmt.Printf("  USB 复位失败: %v（可能需要手动重插）\n", err)
						} else {
							fmt.Println("  USB 复位已发送，等待设备重新枚举...")
						}
					}
					time.Sleep(5 * time.Second)
				} else {
					fmt.Println("等待设备重新出现，继续监控...")
					time.Sleep(1 * time.Second)
				}
			}
		} else {
			// 设备不在下载模式，等待
			if time.Since(lastStatus) > 10*time.Second {
				lastStatus = time.Now()
				fmt.Printf("[%s] 等待设备进入下载模式...\n", time.Now().Format("15:04:05"))
			}
			sleepMs(intervalMs)
		}
	}
}

// sleepMs 按毫秒休眠
func sleepMs(ms int) {
	if ms <= 0 {
		ms = 50
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

func sendImageData(session *Session, data []byte) error {
	return sendImageDataWithRetry(session, data, 3)
}

func sendImageDataWithRetry(session *Session, data []byte, retries int) error {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		rsp, err := session.SmuxSendCmd(fmt.Sprintf("download:%x", len(data)))
		if err != nil {
			lastErr = fmt.Errorf("download command failed (attempt %d/%d): %v", attempt, retries, err)
			// 尝试重握手
			if attempt < retries {
				fmt.Printf("    重试 %d/%d: 等待 3s 后重握手...\n", attempt, retries)
				time.Sleep(3 * time.Second)
				if err := tryResync(session); err != nil {
					fmt.Printf("    重握手失败: %v\n", err)
				} else {
					fmt.Printf("    重握手成功，重试中...\n")
				}
				continue
			}
			return lastErr
		}

		if !strings.HasPrefix(rsp, "DATA") {
			return fmt.Errorf("download command failed: %s", rsp)
		}

		// Give device a moment to prepare for data
		time.Sleep(200 * time.Millisecond)

		_, err = session.SmuxSendData(data)
		if err != nil {
			return fmt.Errorf("data send failed: %v", err)
		}

		rsp, err = session.SmuxWaitResponse(300000)
		if err != nil {
			lastErr = fmt.Errorf("wait response failed (attempt %d/%d): %v", attempt, retries, err)
			if attempt < retries {
				fmt.Printf("    重试 %d/%d: %v\n", attempt, retries, err)
				time.Sleep(2 * time.Second)
				continue
			}
			return lastErr
		}

		if !strings.HasPrefix(rsp, "OKAY") {
			return fmt.Errorf("download failed: %s", rsp)
		}

		// preboot.img 是可执行代码，执行后设备会发送 HELLO_REPLY 重新握手
		// 需要处理这个重握手，然后才能发送 flasher.img
		if len(data) < 50000 {
			fmt.Printf("    引导镜像上传成功，等待设备重握手...\n")
			// 等待设备发送 HELLO_REPLY（预引导程序启动后会重新握手）
			err = session.waitForDeviceRehandshake(15000)
			if err != nil {
				fmt.Printf("    等待重握手超时: %v，继续尝试...\n", err)
			} else {
				fmt.Printf("    设备已重新握手，发送 HELLO 回应...\n")
				// 发送 HELLO 回应
				err = session.SmuxHandshake()
				if err != nil {
					fmt.Printf("    重握手失败: %v\n", err)
				} else {
					fmt.Printf("    重握手成功！\n")
				}
			}
		} else {
			time.Sleep(500 * time.Millisecond)
		}

		return nil
	}
	return lastErr
}

func waitForDeviceReconnect(timeoutSec int) {
	for i := 0; i < timeoutSec; i++ {
		dev, err := FindQuectelDevice()
		if dev == nil || err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		fmt.Printf("    设备重新连接: %s (耗时 %ds)\n", dev.Path, i)
		return
	}
	fmt.Println("    警告: 等待设备重连超时，继续尝试...")
}

func tryResync(session *Session) error {
	// 尝试重新 SMUX 握手，不重建 USB 连接
	return session.SmuxHandshake()
}

func flashPartition(session *Session, data []byte) error {
	return sendImageData(session, data)
}

func erasePartition(session *Session, partition string) error {
	cmd := fmt.Sprintf("erase:%s", partition)
	rsp, err := session.SmuxSendCmd(cmd)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(rsp, "OKAY") {
		return fmt.Errorf("erase failed: %s", rsp)
	}
	return nil
}

// abootDownload 执行 ABOOT 下载协议: download:<size> + data chunks
func abootDownload(session *Session, data []byte) error {
	// Step 1: send download command
	cmd := fmt.Sprintf("download:%x", len(data))
	fmt.Printf("    %s\n", cmd)
	rsp, err := session.SmuxSendCmd(cmd)
	if err != nil {
		return fmt.Errorf("download command failed: %v", err)
	}
	if !strings.HasPrefix(rsp, "DATA") {
		return fmt.Errorf("download command failed: %s", rsp)
	}
	fmt.Printf("    设备准备就绪，开始传输数据...\n")

	// Step 2: send data in chunks
	_, err = session.SmuxSendData(data)
	if err != nil {
		return fmt.Errorf("data send failed: %v", err)
	}

	// Step 3: wait for OKAY
	rsp, err = session.SmuxWaitResponse(300000)
	if err != nil {
		return fmt.Errorf("wait OKAY failed: %v", err)
	}
	if !strings.HasPrefix(rsp, "OKAY") {
		return fmt.Errorf("download failed: %s", rsp)
	}
	fmt.Printf("    数据传输完成 (OKAY)\n")

	return nil
}

// flashSegmented 分段下载大数据并烧录到同一分区
// ABOOT 协议支持多次 flash:<partition> 写同一分区，flasher 内部维护写入偏移量
// 原厂固件即用此方式刷写 4.2MB cp.bin（分 5 段，每段 255 块 = 1MB）
// 每段 sparse 结构: DONT_CARE(跳偏移) + RAW(255块) + [DONT_CARE尾部] + CRC32(该段数据crc32)
func flashSegmented(session *Session, data []byte, partition string) error {
	total := len(data)
	// 原厂 QFlash 工具把 cp.bin 转成 sparse 分段下载（每段 255 块 = 1MB），
	// 每段 sparse 结构经字节级验证与原厂完全一致：
	//   分段1: RAW(255) + DONT_CARE(剩余) + CRC32
	//   分段2+: DONT_CARE(偏移) + RAW(255) + DONT_CARE(尾部) + CRC32
	// flasher 内部维护写入偏移，多个 flash:partition 命令写同一分区。
	const segBlocks = 255
	const maxSegPayload = segBlocks * BlockSize // 1044480 bytes
	segCount := (total + maxSegPayload - 1) / maxSegPayload
	fmt.Printf("    [分段] 总数据 %d bytes，分 %d 段（每段 %d 块 = %d bytes），sparse 模式\n", total, segCount, segBlocks, maxSegPayload)

	for seg := 0; seg < segCount; seg++ {
		start := seg * maxSegPayload
		end := start + maxSegPayload
		if end > total {
			end = total
		}
		segment := data[start:end]
		offsetBlocks := uint32(start / BlockSize)
		fmt.Printf("    [分段 %d/%d] 偏移 0x%08x，数据 %d bytes\n", seg+1, segCount, start, len(segment))

		// 生成带偏移的 sparse 镜像（与原厂字节级一致）
		sparse, err := rawToSparseAtOffset(segment, uint32(total), offsetBlocks)
		if err != nil {
			return fmt.Errorf("第 %d 段 sparse 转换失败: %v", seg+1, err)
		}
		fmt.Printf("    [分段 %d/%d] sparse 大小 %d bytes\n", seg+1, segCount, len(sparse))

		// 1. download 本段 sparse 数据
		if err := abootDownload(session, sparse); err != nil {
			return fmt.Errorf("第 %d 段下载失败: %v", seg+1, err)
		}

		// 2. 烧录到分区
		flashCmd := fmt.Sprintf("flash:%s", partition)
		fmt.Printf("    [分段 %d/%d] %s...\n", seg+1, segCount, flashCmd)
		rsp, err := session.SmuxSendCmd(flashCmd)
		if err != nil {
			return fmt.Errorf("第 %d 段 %s 失败: %v", seg+1, flashCmd, err)
		}
		fmt.Printf("    [分段 %d/%d] %s 响应: %s\n", seg+1, segCount, flashCmd, rsp)
		if strings.HasPrefix(rsp, "FAIL") {
			return fmt.Errorf("第 %d 段 %s 返回 FAIL: %s", seg+1, flashCmd, rsp)
		}

		// 段与段之间稍作缓冲，让 flasher 写完闪存
		if seg < segCount-1 {
			time.Sleep(300 * time.Millisecond)
		}
	}

	fmt.Printf("    [分段] 全部 %d 段烧录完成\n", segCount)
	return nil
}

// rehandshake 在命令失败后重握手恢复会话
func rehandshake(session *Session) {
	fmt.Println("    重握手恢复会话...")
	time.Sleep(2 * time.Second) // 等待 flasher 从失败状态恢复
	err := session.SmuxHandshake()
	if err != nil {
		fmt.Printf("    重握手失败: %v\n", err)
	} else {
		fmt.Printf("    重握手成功\n")
	}
	time.Sleep(1 * time.Second) // 稳定后给 flasher 时间
}