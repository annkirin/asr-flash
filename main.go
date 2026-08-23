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

	cliMode()
}

func webMode(addr string) {
	fmt.Println("=== ASR Flash Tool (Web Mode) ===")
	fmt.Println()
	StartWebServer(addr)
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
