package main

import (
	"fmt"
	"os"
	"path/filepath"
)

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
