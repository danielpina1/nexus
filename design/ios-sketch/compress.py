"""Shrinks the rendered PNGs (256-colour palette, dithered) so the folder stays small.
Run after render.cjs:  python3 design/ios-sketch/compress.py"""
import pathlib
from PIL import Image

root = pathlib.Path(__file__).parent / "png"
# Wallpaper-heavy screens band badly with a palette; keep them true-colour at 2x.
KEEP = {"lockscreen.png", "widgets.png", "controls.png"}
for path in sorted(root.rglob("*.png")):
    if path.parent == root and path.name in KEEP:
        continue
    img = Image.open(path).convert("RGB")
    q = img.quantize(colors=256, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.FLOYDSTEINBERG)
    q.save(path, optimize=True)
    print(f"{path.relative_to(root)}: {path.stat().st_size // 1024} KB")
