# LCD 分辨率验证 — 128 vs 160 盲拍法

**设备** IP-Q8 ST7567 64高 宽待定

## 傻瓜式操作

### Web 一键
1. `./asr-flash` 打开 http://localhost:8080
2. 切到 **上传** Tab → 点击 **一键LCD验证 (128/160轮播)**
3. 设备复位（或重上电），ST7567 将轮播：
   - 0-3s: `128x64 BORDER TEST W=128` 居中显示（两侧留白可见）
   - 3-6s: `160x64 BORDER TEST W=160` 满幅
   - 6-9s: `160 X` 十字+边框
   - 循环 + 同步 3 声蜂鸣
4. 拍 **1 张正面照** 发来，我定 `W`

### CLI 一键
```bash
./asr-flash verify-lcd
# 或手动
./asr-flash upload /tmp/verify_lcd_160_128.py /usr/main.py
```

## 判定

- `128` 帧恰好贴边、`160` 帧右裁 ~28px → 真 `128`
- `160` 帧恰好贴边、`128` 帧两侧各空 16px 白边 → 真 `160`

定版后 `C` 的 `W` 常量与 `font` 预渲染即冻结。

