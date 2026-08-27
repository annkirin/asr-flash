# HeyPTT MQTT+Opus 方案 — 新加坡服务器架构（无 CF 依赖）

**定版：** MQTT+Opus | **服务器：** 新加坡裸 IP/域名 + Nginx | **端：** EC600N QuecOpen C | **翻译：** EN↔CN 可选 | **日期：** 2026-08-27

## 1. 为什么 MQTT+Opus 而不是 BND/其他

- **BND 闭源** 仅支持 EC600M/EC800M/EC600U 白名单，你的 EC600N-CN (SPEC: `QuecLocator® + Wi-Fi Scan` 无GNSS, `EC600N-CN V1.3`) 不在列，无法走 `BND/ZZD/SL` 商用平台。
- **MQTT+Opus 最轻**：EC600N 原生 `ql_mqtt`/`ql_socket` + `ql_pcm`，Opus 8-16kbps 在 CAT1 10Mbps/5Mbps 下余量巨大，50ms 级抢麦，1核1G 可撑千组，与你现有 `asr-flash` 的 SMUX/透传同构。
- **迁移成本**：`sprintjim/mypoc` 的 `iot_app` 架构验证过 EC600，但需 JDK+ZLMediaKit+WVP；MQTT 自定义 TLV 更接近你 Go 栈，1周可跑通。

## 2. 新加坡服务器可行性

**可以用，且推荐。**

- **延迟**：SG → 境内 `80-220ms` (CN2/GIA 优化线更低)，PTT 半双工“抢麦-释放”对 300ms 内均可接受；语音走 Opus 20ms 帧，JitterBuffer 60-120ms 可抹平。
- **运营商无法访问 CF 的影响**：你说“运营商无法访问CF搭建的对方方式”——若指 Cloudflare Tunnel/Workers，正好规避。**本方案零 CF 依赖**：裸 `A记录 域名 → SG公网IP` + `Nginx TCP 443/8883`，设备直连 `mqtts://ptt.yourdomain:8883` 或 `wss://ptt.yourdomain/mqtt`，不经过 Any CF IP。
- **建议**：
  - SG 轻量机 `1C1G` + `Debian` + `Docker` + `EMQX 5` (或自写 Go Broker `mochi-mqtt` 更轻) + `Go Relay` + `Nginx`
  - 双端口：`1883` (明文测试，内网) / `8883` (TLS，生产) / `8083` (WebSocket 供 Web 调试)
  - 域名 TLS：`Let's Encrypt` + `Nginx stream` 透传；不要用 Cloudflare 橙云代理（灰云即可）。
  - 若延迟敏感，可后期在境内加一台北京/上海 Relay 做边缘，但 SG 做主控已足够。

## 3. 整体拓扑

```
[EC600N IP-Q8 C固件] --LTE CAT1--\
[EC600N IP-Q8 C固件] --MQTT TLS--> [Nginx 8883] --> [EMQX Broker SG] --> [Go Relay Service]
[Web Debug (asr-flash Web)] --WSS-->            [Go Translate Service] --> LLM(DeepSeek/Qwen) EN↔CN
                                 \--> [Go Group/Presence Service] --> Redis/MQTT 推送
```

- **信令**：MQTT Topic `heyptt/group/{gid}/signal` (抢麦/释放/心跳/语言配置) JSON。
- **媒体**：MQTT Topic `heyptt/group/{gid}/opus` (Binary Opus 20ms*50帧=1s 包，最长8s 一条 PTT)，或走独立 `UDP 5001` 更省流量（第一版先 MQTT，便于穿透，再切 UDP）。
- **流量**：Opus 16kbps ≈ 2KB/s，1分钟 PTT 120KB，CAT1 套餐充足。

## 4. 多语言与你提的 4 项定制

| 需求 | C 端实现 | 服务端配合 |
|---|---|---|
| **1. 多语言** | `U:/lang/{zh,en}.json` + `font8x16.py` 扩展到 `GBK 16x16` (需补“戴”等字模)，`ql_fs` 持久化 `lang.json`，启动加载 | 无 |
| **2. 开机屏显“戴”** | `boot.c: show_boot()` 居中 `32x32` 放大“戴” 2s → `IP-Q8 READY`，借用 `render_bytes` 2倍缩放 | 无 |
| **3. 设置内选语言** | `Menu → 设置 → 语言` 列表，UP/DOWN 选，中键 OK 写 `lang.json` 并即时 `render` 重载 | MQTT 下发 `{"lang":"en"}` 可远程切换 |
| **4. 长按 ⏹⏹ 翻译** | `GPIO4` 长按 800ms 触发录音 1.5s PCM 8k → Opus → `heyptt/{uid}/translate` Publish，收到回推在屏弹窗+TTS播报 | `Go Translate Service` `ASR(Whisper本地或云) → LLM 翻译 EN↔CN` → Publish `heyptt/{uid}/translate_rsp` `{orig, trans, lang}`；设置项 `翻译开关` 控制是否启用 |

- **按键映射**：`GPIO5 PTT` (上键) | `GPIO4 ⏹⏹翻译` (PTT下第二键) | `GPIO3 ⏹⏹取消` | `GPIO10 UP` | `GPIO21 DOWN`，`GPIO4` 短按取消/长按翻译在 C 中同键复用（短按<500ms 忽略，长按触发）。
- **翻译链路时延**：录1.5s + 上传0.2s + ASR0.6s + LLM0.8s + 回推0.2s ≈ 3.3s，屏显气泡+喇叭 TTS。

## 5. EC600N C 固件模块（QuecOpen）

```
app/
 ├─ lcd_st7567.c (Pavel 0xE2/A3/81_1F/23/2F/A0/C8, show/clear/render, 戴字放大)
 ├─ audio_opus.c (ql_pcm + libopus 8k/16k, pa GPIO1, gain, TTS via ql_tts)
 ├─ key_irq.c (GPIO5/4/3/10/21 IRQ 5ms 去抖, 长按800ms 翻译区分)
 ├─ lang.c (ql_fs JSON, font GBK, i18n t("READY"))
 ├─ mqtt_ptt.c (ql_mqtt, TLS, Topic 组/信令/Opus, 心跳 30s, 抢麦状态机)
 ├─ translate.c (录→Opus→MQTT→等回包→UI/TTS)
 └─ main.c (启动→戴→READY→3声蜂鸣盲提示, 同 verify_lcd 逻辑)
```

- **烧录**：`asr-flash` Web/CLI 经 `2ecc:3004` 烧 `firmware.bin` (CRANE)，或 `asr-flash upload` 推 Python 验证脚本 (当前 Python QPY 模式)。
- **持久化**：`U:/heyptt_cfg.json` `{lang, vol, group, translate_en}`

## 6. SG 服务端 Go 组成

```
heyptt-server/
 ├─ broker/ (EMQX 5 Docker, acl.conf 组隔离)
 ├─ relay/ (Go, 订阅 group/# , 1上N下分发, 去重, 录音落盘)
 ├─ translate/ (Go, 订阅 translate/#, Whisper+DeepSeek, 回推)
 └─ api/ (Go, 设备注册/组管理/语言远程配置, 供 asr-flash Web 调用)
```

- **部署**：`docker compose up -d` 在 SG，`Nginx stream { 8883 -> emqx }`，`systemd` 守护。
- **asr-flash 集成**：Web 新增 `PTT调试 Tab` → 订阅 `heyptt/#` 实时看抢麦/Opus 流/翻译回包。

## 7. 走通步骤（下一步）

1. 烧 `verify_lcd_160_128.py` 定 `W=128/160`，拍照定夺。
2. SG 开机+域，部署 EMQX+relay 空壳（可先用公共 `broker.hivemq.com` 自测）。
3. 打通 C 最小闭环：EC600N 上 `抢麦publish→SG relay→同组另一台下行→放音`。
4. 加“戴”+多语言+设置+长按翻译，四改合入。
5. `asr-flash` v3 升级：flash+debug+PTT调试 三合一，发社区版。

## 8. 风险

- SG 抖动偶发 300ms，已留 Jitter；若用户主要在境内，后期加边缘 Relay。
- Opus 库在 QuecOpen 编译需裁剪 (仅 8k NB，关浮点)。
- GBK 字模需补“戴”等常用字，先内置 200 字高频。
