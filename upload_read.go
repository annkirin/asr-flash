package main

// upload_read.go — 专用 upload（读回）工具
// 正确处理 call 后设备 USB 重枚举的完整状态机

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// uploadReadMode 读回设备 flash 的所有分区（upload package）
// 流程：call preboot → 重枚举 → call flasher → 重枚举 → partition → upload
func uploadReadMode() {
	// upload package 路径
	zipPath := "/tmp/upload_pkg.zip"
	if len(os.Args) > 2 {
		zipPath = os.Args[2]
	}

	fmt.Println("=== Upload 读回模式 ===")

	// 解析 upload package
	files, commands, err := parseFirmwareZipWithSparse(zipPath, false)
	if err != nil {
		fmt.Printf("解析失败: %v\n", err)
		os.Exit(1)
	}

	// 按组排序命令
	groups := make(map[string][]DownloadCommand)
	for _, cmd := range commands {
		groups[cmd.Group] = append(groups[cmd.Group], cmd)
	}

	// 初始扫描 + 打开 + 握手
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
	fmt.Printf("设备: %s\n", info.Path)

	session := openAndHandshake(info.Path)
	if session == nil {
		os.Exit(1)
	}

	// 逐个组处理命令
	groupKeys := sortedGroupKeys(groups)
	for _, gk := range groupKeys {
		cmds := groups[gk]
		fmt.Printf("\n=== Group %s (%d commands) ===\n", gk, len(cmds))
		for _, cmd := range cmds {
			switch cmd.Command {
			case "require":
				fmt.Printf("  Require: %s=%v\n", cmd.Name, cmd.Value)

			case "progress":
				fmt.Printf("  Progress weight: %d\n", cmd.Weight)

			case "call":
				fmt.Printf("  Call: %s (%d bytes)", cmd.Image, len(files[cmd.Image]))
				if cmd.VersionBootrom != "" {
					fmt.Printf(" [version-bootrom=%s]", cmd.VersionBootrom)
				}
				fmt.Println()

				// 有 version-bootrom 的用 <version>/<image> 路径，否则用根路径
				imageData, ok := files[cmd.Image]
				if cmd.VersionBootrom != "" {
					alt := cmd.VersionBootrom + "/" + cmd.Image
					if d, ok2 := files[alt]; ok2 {
						imageData = d
						ok = true
						fmt.Printf("    (使用 %s，%d bytes)\n", alt, len(d))
					}
				}
				if !ok {
					fmt.Printf("    找不到镜像: %s\n", cmd.Image)
					os.Exit(1)
				}

				// 1. download
				if err := session.SmuxDownload(imageData); err != nil {
					fmt.Printf("    download 失败: %v\n", err)
					os.Exit(1)
				}
				// 2. verify
				rsp, _ := session.SmuxSendCmd("verify")
				fmt.Printf("    verify: %s\n", rsp)
				// 3. call
				rsp, _ = session.SmuxSendCmd("call")
				fmt.Printf("    call: %.80s...\n", rsp)

				// 4. call 后设备重新初始化：先在同一 fd 上重握手（正常引导不重枚举）
				fmt.Println("    等待设备重新初始化...")
				time.Sleep(500 * time.Millisecond)
				if err := session.SmuxHandshake(); err != nil {
					// 重握手失败，说明设备真的重枚举了，重新扫描
					fmt.Printf("    重握手失败(%v)，重新扫描设备...\n", err)
					session.Close()
					newInfo, err := WaitForDownloadMode(15)
					if err != nil {
						fmt.Printf("    等待重枚举失败: %v\n", err)
						os.Exit(1)
					}
					fmt.Printf("    设备重枚举: %s\n", newInfo.Path)
					session = openAndHandshake(newInfo.Path)
					if session == nil {
						os.Exit(1)
					}
				} else {
					fmt.Println("    重握手成功")
				}

			case "partition":
				fmt.Printf("  Partition: %s (%d bytes)\n", cmd.Image, len(files[cmd.Image]))
				imageData := files[cmd.Image]
				if err := session.SmuxDownload(imageData); err != nil {
					fmt.Printf("    download 失败: %v\n", err)
					os.Exit(1)
				}
				rsp, _ := session.SmuxSendCmd("partition")
				fmt.Printf("    partition: %s\n", rsp)

			case "upload":
				fmt.Printf("  Upload(读回): %s\n", cmd.Partition)

				// 正确协议：useflash → ulstage → upload → DATA数据
				useCmd := fmt.Sprintf("useflash:%s", cmd.Partition)
				rsp, err := session.SmuxSendCmd(useCmd)
				if err != nil {
					fmt.Printf("    %s 失败: %v\n", useCmd, err)
					continue
				}
				fmt.Printf("    %s: %s\n", useCmd, rsp)

				start := "0"
				size := "800000"
				if cmd.Partition != "all" {
					pmap := map[string][2]string{
						"customer_app": {"58e000", "c6000"},
						"customer_fs":  {"744000", "10000"},
						"nvm":          {"764000", "60000"},
						"factory":      {"7f0000", "10000"},
						"cp":           {"22000", "515000"},
						"fwcerts":      {"17000", "3000"},
						"bootloader":   {"00000", "16000"},
					}
					if p, ok := pmap[cmd.Partition]; ok {
						start, size = p[0], p[1]
					}
				}
				stageCmd := fmt.Sprintf("ulstage:%s:%s", start, size)
				rsp, err = session.SmuxSendCmd(stageCmd)
				fmt.Printf("    %s: %s\n", stageCmd, rsp)

				// 关键：upload 命令触发实际读回，返回 DATA<size>
				// 先 BeginDataExpect（避免竞态：设备在返回 DATA 后立即发数据帧）
				session.BeginDataExpect(0xFFFFFF)
				upCmd := fmt.Sprintf("upload:%s", cmd.Partition)
				rsp, err = session.SmuxSendCmd(upCmd)
				fmt.Printf("    %s: %s\n", upCmd, rsp)

				if strings.HasPrefix(rsp, "DATA") {
					sizeStr := strings.TrimPrefix(rsp, "DATA")
					var dsize int
					fmt.Sscanf(sizeStr, "%x", &dsize)
					fmt.Printf("    数据大小: %d bytes (0x%x)\n", dsize, dsize)
					// 重新精确设置期望大小并接收
					session.mu.Lock()
					session.expectSize = dsize
					session.mu.Unlock()
					data, err := session.ReceiveData(180000)
					if err != nil {
						fmt.Printf("    接收失败: %v\n", err)
					} else {
						outFile := fmt.Sprintf("/tmp/upload_%s.bin", cmd.Partition)
						os.WriteFile(outFile, data, 0644)
						fmt.Printf("    ✓ 已保存: %s (%d bytes)\n", outFile, len(data))
					}
				} else {
					fmt.Printf("    upload 未返回 DATA: %s\n", rsp)
				}

			default:
				fmt.Printf("  忽略命令: %s\n", cmd.Command)
			}
		}
	}

	fmt.Println("\n=== 读回完成 ===")
	session.Close()
}

func sortedGroupKeys(groups map[string][]DownloadCommand) []string {
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	// 简单排序
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func openAndHandshake(path string) *Session {
	fd, err := OpenUSBDevice(path)
	if err != nil {
		fmt.Printf("打开设备失败: %v\n", err)
		return nil
	}
	session := NewSession(fd)
	session.OnLog = func(msg string) { fmt.Println(msg) }

	if err := ClaimInterface(session.FD(), 1); err != nil {
		fmt.Printf("声明接口失败: %v\n", err)
		return nil
	}

	if err := session.SmuxHandshake(); err != nil {
		fmt.Printf("SMUX 握手失败: %v\n", err)
		return nil
	}
	fmt.Println("SMUX 握手成功")
	return session
}