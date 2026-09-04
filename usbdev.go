package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	USB_DIR_OUT         = 0
	USB_DIR_IN          = 0x80
	USB_TYPE_STANDARD   = 0
	USB_TYPE_CLASS      = 0x20
	USB_RECIP_DEVICE    = 0
	USB_RECIP_INTERFACE = 1
	USB_REQ_SET_LINE_STATE = 0x22
	USB_DIR_OUT_TYPE_INTERFACE = USB_DIR_OUT | USB_TYPE_CLASS | USB_RECIP_INTERFACE
)

type usbdevfs_ioctl struct {
	ifno int32
	ioctl_code int32
	data unsafe.Pointer
}

type usbdevfs_bulktransfer struct {
	Ep      uint32
	Len     uint32
	Timeout uint32
	Data    unsafe.Pointer
}

type usbdevfs_getdriver struct {
	Interface uint32
	Driver    [256]byte
}

type usbdevfs_disconnect_claim struct {
	Interface uint32
	Flags     uint32
	Driver    [256]byte
}

var (
	usbdevfsIoctl         uintptr = 0x5401
	usbdevfsClaimInterface uintptr = 0x8004550F
	usbdevfsReleaseInterface uintptr = 0x80045510
	usbdevfsBulk          uintptr = 0xC0185502
	usbdevfsGetDriver     uintptr = 0x41045508
	usbdevfsDisconnect    uintptr = 0x40005516
)

func OpenUSBDevice(path string) (int, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", path, err)
	}
	return fd, nil
}

func CloseUSBDevice(fd int) {
	if fd >= 0 {
		syscall.Close(fd)
	}
}

// USBDEVFS_RESET 复位 USB 设备，强制其重新枚举
const usbdevfsReset = 21780 // _IO('U', 20)

func ResetUSBDevice(path string) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer syscall.Close(fd)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		usbdevfsReset, 0)
	if errno != 0 {
		return fmt.Errorf("usb reset %s: %w", path, errno)
	}
	return nil
}

func ClaimInterface(fd, ifNum int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		usbdevfsClaimInterface, uintptr(unsafe.Pointer(&ifNum)))
	if errno != 0 {
		return fmt.Errorf("claim interface %d: %w", ifNum, errno)
	}
	return nil
}

func ReleaseInterface(fd, ifNum int) {
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		usbdevfsReleaseInterface, uintptr(unsafe.Pointer(&ifNum)))
}

func SetLineState(fd int, dtr, rts bool) error {
	var val uint32
	if dtr { val |= 1 }
	if rts { val |= 2 }
	ctrl := uint32(USB_DIR_OUT_TYPE_INTERFACE | (USB_REQ_SET_LINE_STATE << 8))
	return usbControlMsg(fd, 0, ctrl, val, 1, nil)
}

func usbControlMsg(fd int, iface int, controlType uint32, value, index uint32, data []byte) error {
	type usbdevfs_ctrltransfer struct {
		RequestType uint8
		Request     uint8
		Value       uint16
		Index       uint16
		Length      uint16
		Timeout     uint32
		Data        unsafe.Pointer
	}
	ctrl := usbdevfs_ctrltransfer{
		RequestType: uint8(controlType >> 8),
		Request:     uint8(controlType),
		Value:       uint16(value),
		Index:       uint16(index),
		Length:      uint16(len(data)),
		Timeout:     5000,
	}
	if len(data) > 0 {
		ctrl.Data = unsafe.Pointer(&data[0])
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		usbdevfsIoctl, uintptr(unsafe.Pointer(&ctrl)))
	if errno != 0 {
		return fmt.Errorf("control transfer: %w", errno)
	}
	return nil
}

func BulkWrite(fd int, ep byte, data []byte, timeoutMs uint32) (int, error) {
	buf := make([]byte, 24)
	buf[0] = ep
	buf[4] = byte(len(data))
	buf[5] = byte(len(data) >> 8)
	buf[6] = byte(len(data) >> 16)
	buf[7] = byte(len(data) >> 24)
	buf[8] = byte(timeoutMs)
	buf[9] = byte(timeoutMs >> 8)
	buf[10] = byte(timeoutMs >> 16)
	buf[11] = byte(timeoutMs >> 24)
	dataPtr := uintptr(unsafe.Pointer(&data[0]))
	buf[16] = byte(dataPtr)
	buf[17] = byte(dataPtr >> 8)
	buf[18] = byte(dataPtr >> 16)
	buf[19] = byte(dataPtr >> 24)
	buf[20] = byte(dataPtr >> 32)
	buf[21] = byte(dataPtr >> 40)
	buf[22] = byte(dataPtr >> 48)
	buf[23] = byte(dataPtr >> 56)

	ret, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		usbdevfsBulk, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return 0, fmt.Errorf("bulk write ep=0x%02x: %w", ep, errno)
	}
	return int(ret), nil
}

func BulkRead(fd int, ep byte, buf []byte, timeoutMs uint32) (int, error) {
	ioctlBuf := make([]byte, 24)
	ioctlBuf[0] = ep
	ioctlBuf[4] = byte(len(buf))
	ioctlBuf[5] = byte(len(buf) >> 8)
	ioctlBuf[6] = byte(len(buf) >> 16)
	ioctlBuf[7] = byte(len(buf) >> 24)
	ioctlBuf[8] = byte(timeoutMs)
	ioctlBuf[9] = byte(timeoutMs >> 8)
	ioctlBuf[10] = byte(timeoutMs >> 16)
	ioctlBuf[11] = byte(timeoutMs >> 24)
	dataPtr := uintptr(unsafe.Pointer(&buf[0]))
	ioctlBuf[16] = byte(dataPtr)
	ioctlBuf[17] = byte(dataPtr >> 8)
	ioctlBuf[18] = byte(dataPtr >> 16)
	ioctlBuf[19] = byte(dataPtr >> 24)
	ioctlBuf[20] = byte(dataPtr >> 32)
	ioctlBuf[21] = byte(dataPtr >> 40)
	ioctlBuf[22] = byte(dataPtr >> 48)
	ioctlBuf[23] = byte(dataPtr >> 56)

	ret, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		usbdevfsBulk, uintptr(unsafe.Pointer(&ioctlBuf[0])))
	if errno != 0 {
		return 0, fmt.Errorf("bulk read ep=0x%02x: %w", ep, errno)
	}
	return int(ret), nil
}

func DisconnectKernelDriver(fd, ifNum int) error {
	var dc usbdevfs_disconnect_claim
	dc.Interface = uint32(ifNum)
	copy(dc.Driver[:], "usbfs")
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		usbdevfsDisconnect, uintptr(unsafe.Pointer(&dc)))
	if errno != 0 {
		return fmt.Errorf("disconnect claim: %w", errno)
	}
	return nil
}

type DeviceInfo struct {
	Path   string
	Bus    int
	Addr   int
	Serial string
	Mode   string
}

func FindQuectelDevice() (*DeviceInfo, error) {
	busDir := "/sys/bus/usb/devices"
	entries, err := os.ReadDir(busDir)
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		ueventPath := busDir + "/" + e.Name() + "/uevent"
		data, err := os.ReadFile(ueventPath)
		if err != nil {
			continue
		}
		content := string(data)

		productField := extractField(content, "PRODUCT")
		vendorField := extractField(content, "ID_VENDOR_ID")

		isQuectel := strings.HasPrefix(productField, "2c7c/") || strings.HasPrefix(productField, "2ecc/") ||
			vendorField == "2c7c" || vendorField == "2ecc"
		if !isQuectel {
			continue
		}

		busStr := extractField(content, "BUSNUM")
		addrStr := extractField(content, "DEVNUM")

		productStr := ""
		if strings.HasPrefix(productField, "2ecc/") {
			productStr = "3004"
		} else if strings.HasPrefix(productField, "2c7c/") {
			parts := strings.Split(productField, "/")
			if len(parts) >= 2 {
				productStr = parts[1]
			}
		}
		if vendorField == "2ecc" {
			productStr = "3004"
		}

		if busStr == "" || addrStr == "" {
			continue
		}

		bus, _ := strconv.Atoi(busStr)
		addr, _ := strconv.Atoi(addrStr)

		path := fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, addr)
		mode := "normal"
		if productStr == "3004" {
			mode = "download"
		}

		serialPath := busDir + "/" + e.Name() + "/serial"
		serial, _ := os.ReadFile(serialPath)
		serialStr := strings.TrimSpace(string(serial))

		return &DeviceInfo{
			Path:   path,
			Bus:    bus,
			Addr:   addr,
			Serial: serialStr,
			Mode:   mode,
		}, nil
	}

	return nil, fmt.Errorf("no quectel device found")
}

func extractField(content, field string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, field+"=") {
			return strings.TrimPrefix(line, field+"=")
		}
	}
	return ""
}

func SendATDownload(bus, addr int) error {
	path := fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, addr)
	fd, err := OpenUSBDevice(path)
	if err != nil {
		return err
	}
	defer CloseUSBDevice(fd)

	unbindOptionDriver(bus, addr)

	ifNum := 3
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		usbdevfsClaimInterface, uintptr(unsafe.Pointer(&ifNum)))
	if errno != 0 {
		return fmt.Errorf("claim interface 3: %w", errno)
	}
	defer ReleaseInterface(fd, ifNum)

	atCmd := []byte("AT+QDownLOAD=1\r\n")
	_, err = BulkWrite(fd, 0x0f, atCmd, 2000)
	if err != nil {
		return fmt.Errorf("write AT: %w", err)
	}

	buf := make([]byte, 256)
	n, _ := BulkRead(fd, 0x86, buf, 3000)
	if n > 0 {
		fmt.Printf("  AT 响应: %s", string(buf[:n]))
	}

	return nil
}

func unbindOptionDriver(bus, addr int) {
	busStr := fmt.Sprintf("%d-%d", bus, addr)
	driversDir := "/sys/bus/usb/drivers/option/"
	entries, err := os.ReadDir(driversDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), busStr+":") {
			unbindPath := driversDir + "unbind"
			f, err := os.OpenFile(unbindPath, os.O_WRONLY, 0)
			if err != nil {
				continue
			}
			f.WriteString(e.Name())
			f.Close()
		}
	}
}

func WaitForDownloadMode(timeoutSec int) (*DeviceInfo, error) {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)

	for time.Now().Before(deadline) {
		info, err := FindQuectelDevice()
		if err == nil && info.Mode == "download" {
			return info, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return nil, fmt.Errorf("timeout after %ds", timeoutSec)
}
