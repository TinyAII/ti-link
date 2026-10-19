"""给安装向导生成品牌图（纯标准库，不依赖 PIL）。

生成两类图，每个都要 5 个 DPI 档（Inno 按系统缩放自动挑）：
  WizardImageFile       左侧大图   164x314 起（100/125/150/175/200%）
  WizardSmallImageFile  右上小图   55x55   起

为什么不用 AI 生成图：带文字的图 AI 画必糊（这条是血泪教训），
而这图里的 IT 标记本来就是**几个矩形** —— 直接画，任意缩放都锐利 ✓

用法：python scripts/inno/mkimages.py
产物：scripts/inno/img/wizard-<scale>.png / small-<scale>.png
"""
import os
import struct
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, 'img')

SS = 4          # 超采样倍数：先把图按 4 倍画，再盒式降采样 ⇒ 边缘平滑
BRAND = (0x1E, 0xA0, 0x7A)      # 跟 app.ico 一个青绿
DEEP = (0x10, 0x6E, 0x54)       # 渐变到稍深的绿
WHITE = (255, 255, 255)


def png(path, w, h, rows):
    """rows: 每行是 bytes(bytearray) 的 RGBA。"""
    raw = b''.join(b'\x00' + bytes(r) for r in rows)

    def chunk(tag, data):
        c = tag + data
        return struct.pack('>I', len(data)) + c + struct.pack('>I', zlib.crc32(c) & 0xFFFFFFFF)

    hdr = struct.pack('>IIBBBBB', w, h, 8, 6, 0, 0, 0)
    with open(path, 'wb') as f:
        f.write(b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', hdr)
                + chunk(b'IDAT', zlib.compress(raw, 9)) + chunk(b'IEND', b''))


def render(w, h, painter):
    """按 SS 倍画，再降到 w×h。painter(px, W, H) 往 px 里写 RGBA。"""
    W, H = w * SS, h * SS
    px = bytearray(W * H * 4)          # 全透明
    painter(px, W, H)

    out = bytearray(w * h * 4)
    area = SS * SS
    for y in range(h):
        for x in range(w):
            r = g = b = a = 0
            for dy in range(SS):
                base = ((y * SS + dy) * W + x * SS) * 4
                for dx in range(SS):
                    i = base + dx * 4
                    r += px[i]; g += px[i + 1]; b += px[i + 2]; a += px[i + 3]
            o = (y * w + x) * 4
            out[o]     = r // area
            out[o + 1] = g // area
            out[o + 2] = b // area
            out[o + 3] = a // area
    return [out[y * w * 4:(y + 1) * w * 4] for y in range(h)]


def put(px, W, H, x, y, color):
    if 0 <= x < W and 0 <= y < H:
        i = (y * W + x) * 4
        px[i:i + 4] = bytes(color)


def rect(px, W, H, x0, y0, x1, y1, color):
    for y in range(max(0, int(y0)), min(H, int(y1))):
        for x in range(max(0, int(x0)), min(W, int(x1))):
            put(px, W, H, x, y, color)


def roundrect(px, W, H, x0, y0, x1, y1, rad, color):
    r = rad
    for y in range(max(0, int(y0)), min(H, int(y1))):
        for x in range(max(0, int(x0)), min(W, int(x1))):
            # 只对四个角做圆角判断
            cx = x0 + r if x < x0 + r else (x1 - r if x > x1 - r else None)
            cy = y0 + r if y < y0 + r else (y1 - r if y > y1 - r else None)
            if cx is not None and cy is not None:
                if (x - cx) ** 2 + (y - cy) ** 2 > r * r:
                    continue
            put(px, W, H, x, y, color)


def ti_mark(px, W, H, cx, cy, height, color):
    """画 TI 标记。比例跟 index.html 里那个 SVG 一致（横杠 9.6 / 竖笔 3 / 间隔 1.4）。"""
    u = height / 11.0                  # 11 = SVG 里字形的高度
    left = cx - (14.0 * u) / 2         # 字形总宽 14
    top = cy - height / 2
    rect(px, W, H, left,               top,           left + 9.6 * u, top + 3 * u, color)
    rect(px, W, H, left + 3.3 * u,     top,           left + 6.3 * u, top + 11 * u, color)
    rect(px, W, H, left + 11.0 * u,    top,           left + 14.0 * u, top + 11 * u, color)


def painter_big(px, W, H):
    """左侧大图：竖向渐变 + 淡淡的圆 + 居中白 TI。"""
    for y in range(H):
        t = y / max(1, H - 1)
        r = int(BRAND[0] + (DEEP[0] - BRAND[0]) * t)
        g = int(BRAND[1] + (DEEP[1] - BRAND[1]) * t)
        b = int(BRAND[2] + (DEEP[2] - BRAND[2]) * t)
        for x in range(W):
            put(px, W, H, x, y, (r, g, b, 255))

    # 两个很淡的光斑，让纯色不那么呆（alpha 低到几乎看不出）
    for (fx, fy, fr) in ((0.18, 0.16, 0.34), (0.95, 0.92, 0.42)):
        ccx, ccy, rr = fx * W, fy * H, fr * max(W, H)
        for y in range(int(ccy - rr), int(ccy + rr)):
            for x in range(int(ccx - rr), int(ccx + rr)):
                if not (0 <= x < W and 0 <= y < H):
                    continue
                d = ((x - ccx) ** 2 + (y - ccy) ** 2) ** 0.5
                if d < rr:
                    k = (1 - d / rr) ** 2 * 0.10
                    i = (y * W + x) * 4
                    px[i]     = min(255, int(px[i] + (255 - px[i]) * k))
                    px[i + 1] = min(255, int(px[i + 1] + (255 - px[i + 1]) * k))
                    px[i + 2] = min(255, int(px[i + 2] + (255 - px[i + 2]) * k))

    ti_mark(px, W, H, W * 0.5, H * 0.42, H * 0.19, WHITE + (255,))


def painter_small(px, W, H):
    """右上小图：青绿圆角方块 + 白 TI（就是 app.ico 的缩小版，但四角透明）。"""
    pad = W * 0.02
    roundrect(px, W, H, pad, pad, W - pad, H - pad, W * 0.22, BRAND + (255,))
    ti_mark(px, W, H, W * 0.5, H * 0.5, H * 0.50, WHITE + (255,))


def main():
    os.makedirs(OUT, exist_ok=True)
    scales = [100, 125, 150, 175, 200]
    # Inno 的 DPI 档：100% 时 164x314 / 55x55，按比例放大后取整
    for s in scales:
        k = s / 100.0
        bw, bh = round(164 * k), round(314 * k)
        p = os.path.join(OUT, 'wizard-%d.png' % s)
        png(p, bw, bh, render(bw, bh, painter_big))
        print('  %-34s %3dx%-3d %6d 字节' % (p, bw, bh, os.path.getsize(p)))

        sw = round(55 * k)
        p = os.path.join(OUT, 'small-%d.png' % s)
        png(p, sw, sw, render(sw, sw, painter_small))
        print('  %-34s %3dx%-3d %6d 字节' % (p, sw, sw, os.path.getsize(p)))


if __name__ == '__main__':
    main()
