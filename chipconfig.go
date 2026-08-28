package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"
)

// ChipConfig 芯片配置
type ChipConfig struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Family      string         `yaml:"family"`
	Version     string         `yaml:"version"`
	USB         *USBConfig     `yaml:"usb"`
	FlashMethods []FlashMethod `yaml:"flash_methods"`
	Partitions  map[string]Partition `yaml:"partitions"`
	FlashParams *FlashParams   `yaml:"flash_params"`
	Firmware    *FirmwareConfig `yaml:"firmware"`
	ATCommands  []ATCommand    `yaml:"at_commands"`
	SMUXCommands []SMUXCommand `yaml:"smux_commands"`
}

// USBConfig USB识别配置
type USBConfig struct {
	VID   string `yaml:"vid"`
	PID   string `yaml:"pid"`
	Class int    `yaml:"class"`
}

// FlashMethod 烧录方式
type FlashMethod struct {
	Type        string `yaml:"type"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Protocol    string `yaml:"protocol"`
	RequiresDownloadMode bool `yaml:"requires_download_mode"`
	Baudrate    int    `yaml:"baudrate"`
	SWDInterface string `yaml:"swd_interface"`
}

// Partition 分区配置
type Partition struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Offset      uint32 `yaml:"offset"`
	Size        uint32 `yaml:"size"`
}

// FlashParams Flash参数
type FlashParams struct {
	Type         string `yaml:"type"`
	Size         uint32 `yaml:"size"`
	PageSize     int    `yaml:"page_size"`
	SectorSize   int    `yaml:"sector_size"`
	EraseCommand int    `yaml:"erase_command"`
}

// FirmwareConfig 固件格式配置
type FirmwareConfig struct {
	Format      string `yaml:"format"`
	Extension   string `yaml:"extension"`
	Description string `yaml:"description"`
}

// ATCommand AT命令定义
type ATCommand struct {
	Command     string `yaml:"command"`
	Description string `yaml:"description"`
}

// SMUXCommand SMUX命令定义
type SMUXCommand struct {
	Command     string `yaml:"command"`
	Description string `yaml:"description"`
}

// ChipManager 芯片管理器
type ChipManager struct {
	chips    map[string]*ChipConfig
	chipsDir string
}

// NewChipManager 创建芯片管理器
func NewChipManager(chipsDir string) (*ChipManager, error) {
	mgr := &ChipManager{
		chips:    make(map[string]*ChipConfig),
		chipsDir: chipsDir,
	}
	
	if err := mgr.LoadAll(); err != nil {
		return nil, err
	}
	
	return mgr, nil
}

// LoadAll 加载所有芯片配置
func (m *ChipManager) LoadAll() error {
	m.chips = make(map[string]*ChipConfig)
	
	// 确保目录存在
	if err := os.MkdirAll(m.chipsDir, 0755); err != nil {
		return err
	}
	
	// 读取所有YAML文件
	pattern := filepath.Join(m.chipsDir, "*.yaml")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	
	for _, file := range files {
		chip, err := m.LoadChip(file)
		if err != nil {
			fmt.Printf("警告: 加载芯片配置失败 %s: %v\n", file, err)
			continue
		}
		m.chips[chip.Family] = chip
	}
	
	// 也加载.yml扩展名
	pattern2 := filepath.Join(m.chipsDir, "*.yml")
	files2, _ := filepath.Glob(pattern2)
	for _, file := range files2 {
		chip, err := m.LoadChip(file)
		if err != nil {
			continue
		}
		m.chips[chip.Family] = chip
	}
	
	return nil
}

// LoadChip 加载单个芯片配置
func (m *ChipManager) LoadChip(file string) (*ChipConfig, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	
	chip := &ChipConfig{}
	if err := yaml.Unmarshal(data, chip); err != nil {
		return nil, err
	}
	
	return chip, nil
}

// GetChip 获取芯片配置
func (m *ChipManager) GetChip(family string) *ChipConfig {
	return m.chips[family]
}

// ListChips 列出所有芯片
func (m *ChipManager) ListChips() []*ChipConfig {
	var chips []*ChipConfig
	for _, chip := range m.chips {
		chips = append(chips, chip)
	}
	return chips
}

// DetectChipByUSB 根据USB VID/PID检测芯片
func (m *ChipManager) DetectChipByUSB(vid, pid string) *ChipConfig {
	vid = strings.ToUpper(vid)
	pid = strings.ToUpper(pid)
	
	for _, chip := range m.chips {
		if chip.USB == nil {
			continue
		}
		if strings.ToUpper(chip.USB.VID) == vid && strings.ToUpper(chip.USB.PID) == pid {
			return chip
		}
	}
	return nil
}

// DetectChipByPath 根据设备路径猜测芯片类型
func (m *ChipManager) DetectChipByPath(path string) *ChipConfig {
	// 如果是 ASR/Quectel 设备，返回 ASR CRANE
	if strings.Contains(path, "ttyACM") {
		if chip, ok := m.chips["asr-crane"]; ok {
			return chip
		}
	}
	// 默认返回 ASR CRANE
	return m.chips["asr-crane"]
}

// GetFlashMethod 获取指定烧录方式
func (c *ChipConfig) GetFlashMethod(methodType string) *FlashMethod {
	for _, m := range c.FlashMethods {
		if m.Type == methodType {
			return &m
		}
	}
	return nil
}