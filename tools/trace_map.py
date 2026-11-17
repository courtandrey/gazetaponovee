"""Обводит штаты на map.jpg и пишет SVG-контуры в mapdata.json.

Запуск (нужны opencv-python-headless и numpy):
    python tools/trace_map.py map.jpg mapdata.json [preview.png]
"""
import json
import sys

import cv2
import numpy as np

# Диапазоны оттенка (OpenCV, 0..179) для цветов заливки на карте.
HUES = {
    "pink": [(0, 10), (170, 180)],
    "orange": [(11, 21)],
    "yellow": [(22, 34)],
    "green": [(36, 70)],
    "purple": [(118, 150)],
}

# Штат -> (цвет заливки, точки внутри каждого куска штата).
STATES = {
    "rubezh": ("yellow", [(350, 480), (120, 300), (520, 570)]),
    "kanyon": ("green", [(540, 330)]),
    "gryada": ("pink", [(700, 110)]),
    "razlom": ("purple", [(730, 215)]),
    "delta": ("yellow", [(770, 340)]),
    "dolina": ("orange", [(905, 270)]),
    "pereprava": ("pink", [(843, 362)]),
    "poberezhe": ("green", [(900, 560)]),
    "arhipelag": ("purple", [(1095, 455), (995, 548), (1170, 548)]),
}


def main(src, dst, preview=None):
    img = cv2.imread(src)
    hsv = cv2.cvtColor(cv2.medianBlur(img, 5), cv2.COLOR_BGR2HSV)
    h, s = hsv[..., 0], hsv[..., 1]
    kernel = cv2.getStructuringElement(cv2.MORPH_ELLIPSE, (5, 5))
    out, canvas = {}, np.full_like(img, 255)
    for i, (sid, (color, seeds)) in enumerate(STATES.items()):
        mask = np.zeros(h.shape, np.uint8)
        for lo, hi in HUES[color]:
            mask |= ((h >= lo) & (h < hi) & (s > 18)).astype(np.uint8)
        # закрываем подписи и выноски, затем слегка расширяем, чтобы соседи сомкнулись
        mask = cv2.morphologyEx(mask, cv2.MORPH_CLOSE, kernel, iterations=2)
        mask = cv2.morphologyEx(mask, cv2.MORPH_OPEN, kernel)
        _, labels = cv2.connectedComponents(mask)
        keep = np.zeros_like(mask)
        for x, y in seeds:
            if labels[y, x] == 0:
                raise SystemExit(f"{sid}: точка {x},{y} не попала в заливку")
            keep |= (labels == labels[y, x]).astype(np.uint8)
        keep = cv2.dilate(keep, np.ones((3, 3), np.uint8))
        contours, _ = cv2.findContours(keep, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_NONE)
        parts = []
        for c in sorted(contours, key=cv2.contourArea, reverse=True):
            if cv2.contourArea(c) < 60:
                continue
            c = cv2.approxPolyDP(c, 0.9, True).reshape(-1, 2)
            parts.append("M" + "L".join(f"{x},{y}" for x, y in c) + "Z")
            cv2.fillPoly(canvas, [c], [int(v) for v in np.random.default_rng(i).integers(60, 230, 3)])
            cv2.polylines(canvas, [c], True, (40, 20, 60), 1)
        m = cv2.moments(keep, True)
        out[sid] = {"d": "".join(parts), "cx": round(m["m10"] / m["m00"]), "cy": round(m["m01"] / m["m00"])}
        print(sid, len(parts), "контур(ов),", len(out[sid]["d"]), "байт")
    json.dump({"w": img.shape[1], "h": img.shape[0], "states": out}, open(dst, "w"), separators=(",", ":"))
    if preview:
        cv2.imwrite(preview, canvas)


if __name__ == "__main__":
    main(*sys.argv[1:])
