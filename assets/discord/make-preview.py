#!/usr/bin/env python3
import math
import os
import shutil
import subprocess
import sys
import tempfile

W, H = 640, 360
FPS = 24
SECONDS = 10
FRAMES = FPS * SECONDS

WIN_X, WIN_Y, WIN_W, WIN_H = 60, 34, 520, 292
BAR_H = 44
MEDIA = (90, 96, 460, 180)
PROG_X, PROG_Y, PROG_W = 90, 288, 460

PLAY_AT = 1.9
CLICKS = [("green", PLAY_AT), ("coral", 5.4), ("gold", 7.7)]
COLORS = {"green": "#3fd77a", "coral": "#ff7a66", "gold": "#ffc44d"}


def smooth(a, b, x):
    if x <= a:
        return 0.0
    if x >= b:
        return 1.0
    t = (x - a) / (b - a)
    return t * t * (3 - 2 * t)


def mix(p, q, k):
    return (p[0] + (q[0] - p[0]) * k, p[1] + (q[1] - p[1]) * k)


def drift(t, cx, cy, ax, ay, sx, sy, phase):
    return (cx + ax * math.sin(sx * t + phase), cy + ay * math.cos(sy * t + phase * 1.7))


def cursor_pos(name, t):
    if name == "green":
        home = drift(t, 430, 150, 26, 16, 0.8, 1.1, 0.0)
        target = (318, 196)
        k = smooth(1.1, PLAY_AT, t) - smooth(2.6, 4.0, t)
        return mix(home, target, k)
    if name == "coral":
        home = drift(t, 200, 240, 30, 20, 0.7, 0.9, 2.1)
        target = (PROG_X + 250, PROG_Y - 2)
        k = smooth(4.5, 5.4, t) - smooth(6.1, 7.2, t)
        return mix(home, target, k)
    home = drift(t, 470, 250, 22, 24, 0.95, 0.75, 4.2)
    target = (330, 60)
    k = smooth(6.9, 7.7, t) - smooth(8.6, 9.6, t)
    return mix(home, target, k)


def cursor_svg(name, t):
    x, y = cursor_pos(name, t)
    return (
        f'<g transform="translate({x:.1f} {y:.1f}) scale(0.62)" filter="url(#cur)">'
        f'<path d="M0 0 L0 62 L17 47 L28 72 L41 66 L30 42 L50 42 Z" fill="{COLORS[name]}" '
        f'stroke="#0f1428" stroke-width="6" stroke-linejoin="round"/></g>'
    )


def pulses(t):
    out = []
    for name, at in CLICKS:
        age = t - at
        if 0 <= age <= 0.45:
            x, y = cursor_pos(name, at)
            k = age / 0.45
            r = 5 + 26 * k
            op = 0.55 * (1 - k)
            out.append(
                f'<circle cx="{x:.1f}" cy="{y:.1f}" r="{r:.1f}" fill="none" '
                f'stroke="{COLORS[name]}" stroke-width="3" opacity="{op:.3f}"/>'
            )
    return "".join(out)


def frame_svg(i):
    t = i / FPS
    playing = t >= PLAY_AT

    if playing:
        pct = min(0.82, (t - PLAY_AT) / (SECONDS - PLAY_AT) * 0.92)
    else:
        pct = 0.0
    fill_w = PROG_W * pct

    mx, my, mw, mh = MEDIA
    ccx, ccy = mx + mw / 2, my + mh / 2

    if playing:
        glyph = (
            f'<rect x="{ccx - 26}" y="{ccy - 30}" width="17" height="60" rx="5" fill="#8b95ff"/>'
            f'<rect x="{ccx + 9}" y="{ccy - 30}" width="17" height="60" rx="5" fill="#8b95ff"/>'
        )
    else:
        glyph = (
            f'<path d="M{ccx - 20} {ccy - 32} L{ccx - 20} {ccy + 32} L{ccx + 30} {ccy} Z" '
            f'fill="#8b95ff" stroke="#8b95ff" stroke-width="14" stroke-linejoin="round"/>'
        )

    bars = []
    for n in range(2):
        by = 304 + n * 13
        bw = [300, 214][n]
        bars.append(
            f'<rect x="{mx}" y="{by}" width="{bw}" height="7" rx="3.5" fill="#c9d2e4" opacity="0.85"/>'
        )

    pips = []
    for n, col in enumerate(["#3fd77a", "#ff7a66", "#ffc44d"]):
        pips.append(
            f'<circle cx="{WIN_X + WIN_W - 28 - n * 20}" cy="{WIN_Y + 22}" r="7.5" '
            f'fill="{col}" stroke="#ffffff" stroke-width="2"/>'
        )

    wm = ""
    if t > 8.3:
        op = smooth(8.3, 9.1, t) * 0.96
        wm = (
            f'<text x="{W/2}" y="352" text-anchor="middle" font-family="Inter Display, Inter, DejaVu Sans" '
            f'font-weight="800" font-size="24" fill="#ffffff" opacity="{op:.3f}" letter-spacing="-0.5">browcord</text>'
        )

    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}">
<defs>
<linearGradient id="bg" x1="0" y1="0" x2="1" y2="1">
<stop offset="0" stop-color="#6d79f7"/><stop offset="0.55" stop-color="#4650d8"/><stop offset="1" stop-color="#242a8c"/>
</linearGradient>
<linearGradient id="glass" x1="0" y1="0" x2="0" y2="1">
<stop offset="0" stop-color="#ffffff"/><stop offset="1" stop-color="#e4eaf6"/>
</linearGradient>
<filter id="lift" x="-25%" y="-25%" width="150%" height="160%">
<feDropShadow dx="0" dy="10" stdDeviation="14" flood-color="#0b0f2e" flood-opacity="0.42"/>
</filter>
<filter id="cur" x="-60%" y="-60%" width="240%" height="240%">
<feDropShadow dx="0" dy="3" stdDeviation="4" flood-color="#0b0f2e" flood-opacity="0.55"/>
</filter>
</defs>
<rect width="{W}" height="{H}" fill="url(#bg)"/>
<g opacity="0.1" stroke="#ffffff" stroke-width="1.2">
<path d="M0 80 H{W} M0 180 H{W} M0 280 H{W}"/><path d="M120 0 V{H} M300 0 V{H} M480 0 V{H}"/>
</g>
<g filter="url(#lift)">
<rect x="{WIN_X}" y="{WIN_Y}" width="{WIN_W}" height="{WIN_H}" rx="20" fill="url(#glass)"/>
<path d="M{WIN_X} {WIN_Y+20} A20 20 0 0 1 {WIN_X+20} {WIN_Y} H{WIN_X+WIN_W-20} A20 20 0 0 1 {WIN_X+WIN_W} {WIN_Y+20} V{WIN_Y+BAR_H} H{WIN_X} Z" fill="#d2dae9"/>
<circle cx="{WIN_X+24}" cy="{WIN_Y+22}" r="6" fill="#ff7a66"/>
<circle cx="{WIN_X+44}" cy="{WIN_Y+22}" r="6" fill="#ffc44d"/>
<circle cx="{WIN_X+64}" cy="{WIN_Y+22}" r="6" fill="#3fd77a"/>
<rect x="{WIN_X+84}" y="{WIN_Y+11}" width="330" height="22" rx="11" fill="#b6c2da"/>
{''.join(pips)}
<rect x="{mx}" y="{my}" width="{mw}" height="{mh}" rx="10" fill="#1b2140"/>
{glyph}
<rect x="{PROG_X}" y="{PROG_Y}" width="{PROG_W}" height="7" rx="3.5" fill="#cfd6e6"/>
<rect x="{PROG_X}" y="{PROG_Y}" width="{fill_w:.1f}" height="7" rx="3.5" fill="#5b66ee"/>
{''.join(bars)}
</g>
{pulses(t)}
{cursor_svg('coral', t)}
{cursor_svg('gold', t)}
{cursor_svg('green', t)}
{wm}
</svg>"""


def main():
    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "preview.mp4")
    work = tempfile.mkdtemp(prefix="browcord-preview-")
    try:
        for i in range(FRAMES):
            svg = os.path.join(work, f"f{i:04d}.svg")
            png = os.path.join(work, f"f{i:04d}.png")
            with open(svg, "w") as fh:
                fh.write(frame_svg(i))
            subprocess.run(
                ["rsvg-convert", "-w", str(W), "-h", str(H), svg, "-o", png],
                check=True,
            )

        subprocess.run(
            [
                "ffmpeg", "-y", "-loglevel", "error",
                "-framerate", str(FPS),
                "-i", os.path.join(work, "f%04d.png"),
                "-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "yuv420p",
                "-crf", "30", "-preset", "veryslow", "-g", str(FPS * 2),
                "-movflags", "+faststart", "-an",
                out,
            ],
            check=True,
        )
        size = os.path.getsize(out)
        print(f"{out}  {size} bytes  ({size/1024/1024:.2f} MB)")
        if size > 500_000:
            print("WARNING: over Discord's 0.5 MB limit, raise -crf", file=sys.stderr)
    finally:
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    main()
