# verify_lcd_160_128.py - IP-Q8 ST7567 分辨率盲拍验证固件
# 烧录方式: asr-flash upload /tmp/verify_lcd_160_128.py /usr/main.py  后复位
# 或 Web 界面 Upload Tab
# 逻辑: 160x64 优化参数启动, 轮播 128x64边框/160x64边框/160x64十字, 每帧3s, 循环 + 3声蜂鸣提示启动
from machine import Pin, SPI
import time, math
from audio import Audio

# --- ST7567 blind driver (Pavel params) ---
class ST7567:
    def __init__(self, spi, dc, cs, rst, contrast=0x1F, flipX=False, flipY=True):
        dc.init(Pin.OUT, value=0)
        self.dc=dc
        self.cs=cs
        if cs: cs.init(Pin.OUT, value=1)
        if rst:
            rst.init(Pin.OUT, value=0)
            time.sleep_ms(1); rst.write(1); time.sleep_ms(1)
        self.spi=spi
        cmds=[0xE2,0xA3,0x81,contrast&0x3f,0x20|0x03,0x28|0x07,0xA0|(0x01 if flipX else 0),0xC0|(0x08 if flipY else 0),0x40|0x00,0xAE|0x01]
        self.writeCommands(cmds)
    def writeCommands(self,cmd):
        if self.cs: self.cs.write(0)
        self.dc.write(0); self.spi.write(bytearray(cmd),len(cmd))
        if self.cs: self.cs.write(1)
    def writeData(self,data):
        if self.cs: self.cs.write(0)
        self.dc.write(1); self.spi.write(data,len(data))
        if self.cs: self.cs.write(1)
    def show(self,buf,w,h):
        pages=h//8
        self.writeCommands([0x40|0x00])
        for p in range(pages):
            self.writeCommands([0xB0|p,0x10|0x00,0x00|0x00])
            self.writeData(buf[p*w:p*w+w])
    def clear(self,val=0x00,w=160,h=64):
        for p in range(h//8):
            self.writeCommands([0xB0|p,0x10|0x00,0x00|0x00])
            self.writeData(bytearray([val]*w))
        # 清掉可能残留的128高区域 (page 8-15)
        for p in range(8,16):
            try:
                self.writeCommands([0xB0|p,0x10|0x00,0x00|0x00])
                self.writeData(bytearray([val]*w))
            except: pass

# --- LCD init ---
bl=Pin(Pin.GPIO15,Pin.OUT,Pin.PULL_DISABLE,1); bl.write(1)
spi=SPI(1,0,1)
lcd=ST7567(spi,dc=Pin(Pin.GPIO16,Pin.OUT),cs=Pin(Pin.GPIO32,Pin.OUT),rst=Pin(Pin.GPIO34,Pin.OUT),contrast=0x1F,flipX=False,flipY=True)

# --- Font ---
try: exec(open("/usr/font8x16.py").read())
except: exec(open("/tmp/font8x16.py").read())
try: exec(open("/tmp/font8x16.py").read())
except: pass

def render_bytes(w,h,texts,invert=False):
    pages=h//8
    buf=bytearray([0xFF if invert else 0x00]*(w*pages))
    for x,y,t in texts:
        for idx,ch in enumerate(t):
            code=ord(ch)
            if code not in FONT: continue
            g=FONT[code]
            for row in range(16):
                bits=g[row]
                for col in range(8):
                    if bits & (0x80>>col):
                        px=x+idx*8+col; py=y+row
                        if 0<=px<w and 0<=py<h:
                            page=py//8; bit=py%8
                            if invert: buf[page*w+px] &= ~(1<<bit)
                            else: buf[page*w+px] |= (1<<bit)
    return buf

def draw_border(buf,w,h):
    pages=h//8
    # top/bottom
    for x in range(w):
        buf[0*w+x] |= 0x01
        buf[(pages-1)*w+x] |= (1<<7)
    # left/right
    for y in range(h):
        buf[y//8*w+0] |= (1<<(y%8))
        buf[y//8*w+w-1] |= (1<<(y%8))

def draw_x(buf,w,h):
    # bresenham X
    def line(x0,y0,x1,y1):
        dx=abs(x1-x0); dy=abs(y1-y0); sx=1 if x0<x1 else -1; sy=1 if y0<y1 else -1; err=dx-dy
        while True:
            if 0<=x0<w and 0<=y0<h: buf[y0//8*w+x0] |= (1<<(y0%8))
            if x0==x1 and y0==y1: break
            e2=2*err
            if e2>-dy: err-=dy; x0+=sx
            if e2<dx: err+=dx; y0+=sy
    line(0,0,w-1,h-1); line(w-1,0,0,h-1)

# --- Audio beep ---
def pa(on,g=4):
    p=Pin(Pin.GPIO1,Pin.OUT,Pin.PULL_DISABLE,0)
    if on:
        p.write(0); time.sleep_ms(600); p.write(1); time.sleep_us(2)
        for _ in range(g-1): p.write(0); time.sleep_us(2); p.write(1); time.sleep_us(2)
    else: p.write(0); time.sleep_ms(600)
try:
    a=Audio(0); a.setSpeakerpaCallback(lambda s: pa(s,4)); a.setVolume(7)
except: a=None
def beep(vol=7):
    try:
        p=Audio.PCM(0); p.setVolume(7); sr=8000; f=600+vol*80; d=0.22; n=int(sr*d); t=bytearray(n*2)
        for i in range(n):
            v=int(12000*math.sin(2*math.pi*f*i/sr)); t[2*i]=v&0xff; t[2*i+1]=(v>>8)&0xff
        pa(1,4); p.write(t); time.sleep(d+0.05); pa(0)
        try: p.close()
        except: pass
    except: pass

# --- 帧生成 ---
def frame_128_border():
    w,h=128,64
    buf=render_bytes(w,h,[(2,2,"128x64"),(2,22,"BORDER TEST"),(2,42,"W=128")])
    draw_border(buf,w,h)
    return buf,w,h

def frame_160_border():
    w,h=160,64
    buf=render_bytes(w,h,[(2,2,"160x64"),(2,22,"BORDER TEST"),(2,42,"W=160")])
    draw_border(buf,w,h)
    return buf,w,h

def frame_160_x():
    w,h=160,64
    buf=render_bytes(w,h,[(32,26,"160 X")])
    draw_border(buf,w,h); draw_x(buf,w,h)
    return buf,w,h

# 启动清屏 + 3声
lcd.clear(0x00,160,64); time.sleep_ms(120)
for i in range(3):
    beep(7+i); time.sleep(0.35)

# 轮播: 128边框 3s -> 160边框 3s -> 160X 3s -> 循环
frames=[frame_128_border,frame_160_border,frame_160_x]
idx=0
while True:
    buf,w,h=frames[idx]()
    # 显示到物理屏: 若w=128则居中显示在160屏上可观察白边
    if w==128:
        # 扩展到160宽, 左右补白, 便于拍照对比
        pages=64//8
        full=bytearray([0x00]*(160*pages))
        xoff=(160-128)//2
        for p in range(pages):
            for x in range(128):
                full[p*160+x+xoff]=buf[p*128+x]
        lcd.show(full,160,64)
    else:
        lcd.show(buf,w,h)
    time.sleep(3)
    idx=(idx+1)%len(frames)
