#!/bin/bash
set -e
cd "$(dirname "$0")"

echo "=== ASR Flash Tool - 设备测试 ==="
echo ""

# 检查设备
echo "1. 检查 USB 设备..."
if lsusb | grep -qi quectel; then
    echo "   找到 Quectel 设备:"
    lsusb | grep -i quectel
else
    echo "   未找到 Quectel 设备，请连接设备"
    exit 1
fi

echo ""
echo "2. 检查下载模式..."
if lsusb | grep -q "2ecc:3004"; then
    echo "   设备在下载模式"
elif lsusb | grep -q "2c7c:6001"; then
    echo "   设备在正常模式，发送 AT+QDownLOAD=1..."
    # 尝试发送 AT 命令进入下载模式
    python3 -c "
import usb.core, usb.util, time
dev = usb.core.find(idVendor=0x2c7c, idProduct=0x6001)
if dev:
    dev.set_configuration()
    cfg = dev.get_active_configuration()
    intf = cfg[(1,0)]
    ep_out = usb.util.find_descriptor(intf, custom_match=lambda e: usb.util.endpoint_direction(e.bEndpointAddress) == usb.util.ENDPOINT_OUT)
    ep_in = usb.util.find_descriptor(intf, custom_match=lambda e: usb.util.endpoint_direction(e.bEndpointAddress) == usb.util.ENDPOINT_IN)
    if ep_out and ep_in:
        ep_out.write(b'AT+QDownLOAD=1\r')
        time.sleep(0.5)
        try:
            data = ep_in.read(64, timeout=1000)
            print('   AT 响应:', data)
        except:
            print('   AT 命令已发送，等待设备重启...')
"
    echo "   等待设备进入下载模式..."
    sleep 5
else
    echo "   未知设备"
fi

echo ""
echo "3. 运行 flash-logicrom..."
if [ -f "heyptt-logicrom.zip" ]; then
    ./asr-flash flash-logicrom heyptt-logicrom.zip
elif [ -f "../heyptt-logicrom/build/heyptt-logicrom.zip" ]; then
    ./asr-flash flash-logicrom ../heyptt-logicrom/build/heyptt-logicrom.zip
else
    echo "   未找到 heyptt-logicrom.zip"
    exit 1
fi
