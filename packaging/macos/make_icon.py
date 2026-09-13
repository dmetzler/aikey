#!/usr/bin/env python3
"""Render aikey's app icon and assemble an .icns.

The icon is generated, not committed as a binary blob, so it can be reviewed as
code and re-rendered at any size. Requires Pillow; no macOS-only tooling, so it
runs on any platform (iconutil is not available off macOS).

    python3 packaging/macos/make_icon.py --out packaging/macos/aikey.icns
"""

from __future__ import annotations

import argparse
import io
import os
import struct
import sys

try:
    from PIL import Image, ImageDraw, ImageFilter
except ImportError:
    sys.exit("Pillow is required: pip install Pillow")

# Pillow 10 moved the resampling constants onto Image.Resampling; the old
# aliases still work but are deprecated, so resolve them once.
_R = getattr(Image, "Resampling", Image)
BICUBIC = _R.BICUBIC
LANCZOS = _R.LANCZOS
AFFINE = getattr(Image, "Transform", Image).AFFINE

# Big Sur-style corner radius, as a fraction of the canvas.
CORNER = 0.2237
GRAD_TOP = (86, 132, 250)
GRAD_BOTTOM = (33, 64, 180)


def _rounded_mask(size: int, radius: int) -> Image.Image:
    m = Image.new("L", (size, size), 0)
    ImageDraw.Draw(m).rounded_rectangle([0, 0, size - 1, size - 1], radius=radius, fill=255)
    return m


def _vertical_gradient(size: int, top, bottom) -> Image.Image:
    col = Image.new("RGB", (1, size))
    for y in range(size):
        t = y / max(1, size - 1)
        col.putpixel((0, y), tuple(int(top[i] + (bottom[i] - top[i]) * t) for i in range(3)))
    return col.resize((size, size), BICUBIC)


def render(size: int, supersample: int = 4) -> Image.Image:
    """Render the icon at `size` px, drawn large and downsampled for clean edges."""
    n = size * supersample
    radius = int(n * CORNER)
    mask = _rounded_mask(n, radius)

    icon = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    icon.paste(_vertical_gradient(n, GRAD_TOP, GRAD_BOTTOM).convert("RGBA"), (0, 0), mask)

    # Blurred sheen. Drawn as a soft ellipse rather than a hard shape, otherwise
    # its edge reads as a seam across the icon.
    sheen = Image.new("L", (n, n), 0)
    ImageDraw.Draw(sheen).ellipse([-n * 0.40, -n * 0.85, n * 1.40, n * 0.38], fill=40)
    sheen = sheen.filter(ImageFilter.GaussianBlur(n * 0.06))
    white = Image.new("RGBA", (n, n), (255, 255, 255, 255))
    white.putalpha(sheen)
    icon = Image.alpha_composite(
        icon, Image.composite(white, Image.new("RGBA", (n, n), (0, 0, 0, 0)), mask)
    )

    icon = Image.alpha_composite(icon, _key_glyph(n))
    return icon.resize((size, size), LANCZOS)


def _key_glyph(n: int) -> Image.Image:
    """A key: ring bow, shaft, two teeth, rotated 45 degrees."""
    layer = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    white = (255, 255, 255, 255)

    cx = cy = n * 0.405
    r_out, r_in = n * 0.148, n * 0.070
    d.ellipse([cx - r_out, cy - r_out, cx + r_out, cy + r_out], fill=white)
    d.ellipse([cx - r_in, cy - r_in, cx + r_in, cy + r_in], fill=(0, 0, 0, 0))

    shaft = n * 0.072
    d.rounded_rectangle(
        [cx + r_out * 0.5, cy - shaft / 2, n * 0.775, cy + shaft / 2],
        radius=shaft * 0.35, fill=white,
    )
    tooth = n * 0.060
    for tx, th in ((n * 0.630, n * 0.140), (n * 0.752, n * 0.104)):
        d.rounded_rectangle(
            [tx - tooth / 2, cy, tx + tooth / 2, cy + th], radius=tooth * 0.35, fill=white
        )

    layer = layer.rotate(-45, resample=BICUBIC, center=(cx, cy))
    # Rotation leaves the glyph high and left of centre; nudge it back.
    return layer.transform(
        layer.size, AFFINE, (1, 0, -n * 0.045, 0, 1, -n * 0.030), resample=BICUBIC
    )


# OSType -> pixel size. Retina entries hold the @2x bitmap.
ICNS_ENTRIES = [
    ("icp4", 16), ("icp5", 32), ("ic11", 32), ("ic12", 64),
    ("ic07", 128), ("ic13", 256), ("ic08", 256),
    ("ic14", 512), ("ic09", 512), ("ic10", 1024),
]


def build_icns(out_path: str) -> None:
    chunks = []
    cache: dict[int, bytes] = {}
    for ostype, size in ICNS_ENTRIES:
        if size not in cache:
            buf = io.BytesIO()
            render(size).save(buf, format="PNG")
            cache[size] = buf.getvalue()
        data = cache[size]
        chunks.append(ostype.encode("ascii") + struct.pack(">I", len(data) + 8) + data)

    body = b"".join(chunks)
    icns = b"icns" + struct.pack(">I", len(body) + 8) + body

    os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
    with open(out_path, "wb") as f:
        f.write(icns)
    print(f"wrote {out_path} ({len(icns)} bytes, {len(ICNS_ENTRIES)} entries)")


def verify(path: str) -> None:
    """Re-parse the file, so a malformed header fails here and not in Finder."""
    with open(path, "rb") as f:
        blob = f.read()
    if blob[:4] != b"icns":
        raise SystemExit("bad magic: not an icns file")
    declared = struct.unpack(">I", blob[4:8])[0]
    if declared != len(blob):
        raise SystemExit(f"length mismatch: header says {declared}, file is {len(blob)}")

    off, seen = 8, []
    while off < len(blob):
        ostype = blob[off:off + 4].decode("ascii")
        length = struct.unpack(">I", blob[off + 4:off + 8])[0]
        payload = blob[off + 8:off + length]
        if len(payload) != length - 8:
            raise SystemExit(f"truncated chunk {ostype}")
        with Image.open(io.BytesIO(payload)) as im:
            seen.append(f"{ostype}:{im.size[0]}")
        off += length
    print("verified:", " ".join(seen))


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--out", default="packaging/macos/aikey.icns")
    ap.add_argument("--png", metavar="DIR", help="also write individual PNGs here")
    args = ap.parse_args()

    build_icns(args.out)
    verify(args.out)

    if args.png:
        os.makedirs(args.png, exist_ok=True)
        for size in sorted({s for _, s in ICNS_ENTRIES}):
            p = os.path.join(args.png, f"icon_{size}.png")
            render(size).save(p)
        print(f"wrote PNGs to {args.png}")


if __name__ == "__main__":
    main()
