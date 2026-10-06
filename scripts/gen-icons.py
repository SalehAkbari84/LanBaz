#!/usr/bin/env python3
"""Generate the placeholder LanBaz application icons.

Tauri requires icons/32x32.png, icons/128x128.png and icons/icon.ico for the
tray and the Windows installer. This script renders a simple deterministic mark
(a LAN-style node graph on a dark rounded square) so a fresh checkout builds
without binary assets committed by hand.

Replace the output with real artwork before shipping; the shell does not depend
on the specific design.
"""
from __future__ import annotations

import struct
import zlib
from pathlib import Path

OUT_DIR = Path(__file__).resolve().parent.parent / "app" / "src-tauri" / "icons"

# LanBaz palette, matching app/tailwind.config.js.
BACKGROUND = (15, 17, 21, 255)
BORDER = (37, 42, 52, 255)
ACCENT = (79, 140, 255, 255)
NODE = (226, 232, 240, 255)

SIZES = (32, 128, 256, 512)
ICO_SIZES = (16, 32, 48, 64, 128, 256)


def blend(dst: tuple[int, ...], src: tuple[int, ...], alpha: int) -> tuple[int, ...]:
    """Alpha-blends src over dst with alpha in 0..255."""
    if alpha >= 255:
        return src
    if alpha <= 0:
        return dst
    out = []
    for i in range(3):
        out.append((src[i] * alpha + dst[i] * (255 - alpha)) // 255)
    out.append(max(dst[3], src[3]))
    return tuple(out)  # type: ignore[return-value]


def render(size: int) -> bytes:
    """Renders one square icon and returns raw RGBA rows."""
    rows: list[list[tuple[int, int, int, int]]] = []
    radius = max(2, size // 8)
    # Node positions in unit coordinates: a hub with four spokes.
    hub = (0.5, 0.52)
    spokes = [(0.26, 0.28), (0.74, 0.28), (0.26, 0.76), (0.74, 0.76)]
    node_r = max(1.4, size * 0.075)
    line_w = max(1.0, size * 0.035)

    for y in range(size):
        row: list[tuple[int, int, int, int]] = []
        for x in range(size):
            px = BACKGROUND
            # Rounded-square border.
            if not inside_rounded(x, y, size, radius, inset=0):
                px = (0, 0, 0, 0)
            elif not inside_rounded(x, y, size, radius, inset=1):
                px = BORDER

            for sx, sy in spokes:
                if dist_to_segment(x, y, hub[0] * size, hub[1] * size, sx * size, sy * size) <= line_w:
                    px = blend(px, ACCENT, 255)

            if dist_to_point(x, y, hub[0] * size, hub[1] * size) <= node_r * 1.25:
                px = blend(px, ACCENT, 255)
            for sx, sy in spokes:
                if dist_to_point(x, y, sx * size, sy * size) <= node_r:
                    px = blend(px, NODE, 255)
            row.append(px)
        rows.append(row)
    return rows


def dist_to_point(x: float, y: float, cx: float, cy: float) -> float:
    return ((x - cx) ** 2 + (y - cy) ** 2) ** 0.5


def dist_to_segment(px: float, py: float, ax: float, ay: float, bx: float, by: float) -> float:
    dx, dy = bx - ax, by - ay
    length_sq = dx * dx + dy * dy
    if length_sq == 0:
        return dist_to_point(px, py, ax, ay)
    t = max(0.0, min(1.0, ((px - ax) * dx + (py - ay) * dy) / length_sq))
    return dist_to_point(px, py, ax + t * dx, ay + t * dy)


def inside_rounded(x: int, y: int, size: int, radius: int, inset: int) -> bool:
    lo, hi = inset, size - 1 - inset
    if not (lo <= x <= hi and lo <= y <= hi):
        return False
    r = max(0, radius - inset)
    if r == 0:
        return True
    for cx, cy in (
        (lo + r, lo + r),
        (hi - r, lo + r),
        (lo + r, hi - r),
        (hi - r, hi - r),
    ):
        if (x < lo + r or x > hi - r) and (y < lo + r or y > hi - r):
            if dist_to_point(x, y, cx, cy) <= r:
                return True
    return True


def png_bytes(rows: list[list[tuple[int, int, int, int]]]) -> bytes:
    """Encodes RGBA rows as a PNG."""
    height = len(rows)
    width = len(rows[0])
    raw = bytearray()
    for row in rows:
        raw.append(0)  # filter type: none
        for px in row:
            raw.extend(px)

    def chunk(tag: bytes, data: bytes) -> bytes:
        return (
            struct.pack(">I", len(data))
            + tag
            + data
            + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)
        )

    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(bytes(raw), 9))
        + chunk(b"IEND", b"")
    )


def ico_bytes(images: list[tuple[int, bytes]]) -> bytes:
    """Packs PNG-encoded images into an ICO container (Vista+ form)."""
    header = struct.pack("<HHH", 0, 1, len(images))
    offset = len(header) + 16 * len(images)
    entries = bytearray()
    payloads = bytearray()
    for size, data in images:
        entries += struct.pack(
            "<BBBBHHII",
            size if size < 256 else 0,
            size if size < 256 else 0,
            0,  # palette
            0,  # reserved
            1,  # colour planes
            32,  # bits per pixel
            len(data),
            offset,
        )
        payloads += data
        offset += len(data)
    return header + bytes(entries) + bytes(payloads)


def main() -> None:
    OUT_DIR.mkdir(parents=True, exist_ok=True)

    cache: dict[int, list[list[tuple[int, int, int, int]]]] = {}
    for size in SIZES:
        rows = render(size)
        cache[size] = rows
        (OUT_DIR / f"{size}x{size}.png").write_bytes(png_bytes(rows))
        print(f"wrote {size}x{size}.png")

    icon = cache.get(256)
    if icon is not None:
        (OUT_DIR / "icon.png").write_bytes(png_bytes(icon))
        print("wrote icon.png")

    ico_images = []
    for size in ICO_SIZES:
        rows = cache.get(size)
        if rows is None:
            rows = render(size)
        ico_images.append((size, png_bytes(rows)))
    (OUT_DIR / "icon.ico").write_bytes(ico_bytes(ico_images))
    print(f"wrote icon.ico ({len(ico_images)} sizes)")


if __name__ == "__main__":
    main()