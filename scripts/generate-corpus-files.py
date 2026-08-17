#!/usr/bin/env python3
"""Generate rendered corpus files (PDF text-layer, scanned-style PDF, PNG image)
for the demo knowledge base.

The host Python has no PIL/PyMuPDF, so this script is meant to run *inside* the
parser-service container (which ships PIL 12.3.0, PyMuPDF and a CJK font):

  docker cp /tmp/day03-fonts/NotoSansCJKsc-Regular.otf ai-etl-platform-parser-service-1:/tmp/
  docker cp scripts/generate-corpus-files.py ai-etl-platform-parser-service-1:/tmp/
  docker exec ai-etl-platform-parser-service-1 python3 /tmp/generate-corpus-files.py \
      --corpus /tmp/enterprise-kb.json --font /tmp/NotoSansCJKsc-Regular.otf > /tmp/corpus-files.tar

It reads the corpus, renders every case whose `format` is `pdf` / `pdf-scan` /
`png` into real files, and writes a tar archive to stdout. The host unpacks it
under docs/corpora/generated/ so load-corpus.py can upload the bytes.

Usage:
  generate-corpus-files.py [--corpus PATH] [--font PATH]
"""

from __future__ import annotations

import argparse
import io
import json
import math
import sys
from pathlib import Path

import fitz  # PyMuPDF
from PIL import Image, ImageDraw, ImageFont

PAGE_W = 1240  # ~A4 @150dpi
PAGE_H = 1754
MARGIN = 90
TITLE_PX = 44
BODY_PX = 32
LINE_H = int(BODY_PX * 1.5)


def wrap(text: str, draw: ImageDraw.ImageDraw, font, max_width: int) -> list[str]:
    """Wrap a paragraph into lines that fit max_width pixels."""
    lines: list[str] = []
    cur = ""
    for ch in text:
        test = cur + ch
        if draw.textlength(test, font=font) <= max_width:
            cur = test
        else:
            lines.append(cur)
            cur = ch
    if cur:
        lines.append(cur)
    return lines


def render_png(title: str, body: str, font_path: str) -> bytes:
    """Render title + wrapped body onto a white image; returns PNG bytes."""
    font_title = ImageFont.truetype(font_path, TITLE_PX)
    font_body = ImageFont.truetype(font_path, BODY_PX)
    # Measure a temp image to compute wrapping, then size the canvas to content.
    probe = Image.new("RGB", (10, 10), "white")
    probe_d = ImageDraw.Draw(probe)
    max_text_w = PAGE_W - 2 * MARGIN
    body_lines = wrap(body, probe_d, font_body, max_text_w)
    content_h = MARGIN + TITLE_PX + 30 + len(body_lines) * LINE_H + MARGIN
    height = min(max(PAGE_H // 2, content_h), PAGE_H)

    img = Image.new("RGB", (PAGE_W, height), "white")
    d = ImageDraw.Draw(img)
    y = MARGIN
    d.text((MARGIN, y), title, font=font_title, fill=(20, 20, 30))
    y += TITLE_PX + 30
    for line in body_lines:
        d.text((MARGIN, y), line, font=font_body, fill=(30, 30, 40))
        y += LINE_H
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()


def build_pdf_text(title: str, body: str, font_path: str) -> bytes:
    """PDF with a real text layer (PyMuPDF + embedded CJK font)."""
    doc = fitz.open()
    page = doc.new_page(width=595, height=842)  # A4 pt
    fontname = "cjk"
    page.insert_font(fontname=fontname, fontfile=font_path)
    y = 56
    page.insert_text((56, y), title, fontname=fontname, fontsize=20)
    y += 30
    rect = fitz.Rect(56, y, 595 - 56, 842 - 56)
    # Split into paragraphs (corpus is single-paragraph) and insert each.
    paras = [p for p in body.split("\n") if p.strip()]
    for para in paras:
        rc = fitz.Rect(rect.x0, rect.y0, rect.x1, rect.y1)
        # insert_textbox returns unused height; empty result means it fit.
        page.insert_textbox(rc, para, fontname=fontname, fontsize=11, lineheight=1.5)
        rect.y0 += 20
    # Subset the embedded CJK font so each PDF keeps only used glyphs (16MB→KBs).
    doc.subset_fonts()
    data = doc.tobytes()
    doc.close()
    return data


def build_pdf_scan(title: str, body: str, font_path: str) -> bytes:
    """Scanned-style PDF: page images only, no text layer (forces OCR)."""
    doc = fitz.open()
    font_title = ImageFont.truetype(font_path, TITLE_PX)
    font_body = ImageFont.truetype(font_path, BODY_PX)
    probe = Image.new("RGB", (10, 10), "white")
    probe_d = ImageDraw.Draw(probe)
    max_text_w = PAGE_W - 2 * MARGIN
    body_lines = wrap(body, probe_d, font_body, max_text_w)
    all_lines = [title] + [""] + body_lines
    per_page = max(1, (PAGE_H - 2 * MARGIN) // LINE_H)
    pages = math.ceil(len(all_lines) / per_page)
    for p in range(pages):
        img = Image.new("RGB", (PAGE_W, PAGE_H), (248, 246, 240))  # paper tint
        d = ImageDraw.Draw(img)
        y = MARGIN
        for line in all_lines[p * per_page : (p + 1) * per_page]:
            d.text((MARGIN, y), line, font=font_title if line == title else font_body, fill=(25, 25, 35))
            y += LINE_H
        png = io.BytesIO()
        img.save(png, format="PNG")
        page = doc.new_page(width=PAGE_W, height=PAGE_H)
        page.insert_image(fitz.Rect(0, 0, PAGE_W, PAGE_H), stream=png.getvalue())
    data = doc.tobytes()
    doc.close()
    return data


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate rendered corpus files into a directory")
    parser.add_argument("--corpus", default="/tmp/enterprise-kb.json", help="corpus JSON path")
    parser.add_argument("--font", default="/tmp/NotoSansCJKsc-Regular.otf", help="CJK font path")
    parser.add_argument("--outdir", default="/tmp/ai-etl-corpus-files", help="output directory")
    args = parser.parse_args()

    font_path = Path(args.font)
    if not font_path.exists():
        print(f"[gen] font not found: {font_path}", file=sys.stderr)
        return 2
    corpus = json.load(open(args.corpus, encoding="utf-8"))
    cases = corpus["cases"]

    outdir = Path(args.outdir)
    outdir.mkdir(parents=True, exist_ok=True)
    ext_map = {"pdf": "pdf", "pdf-scan": "pdf", "png": "png"}
    n = 0
    for c in cases:
        fmt = c.get("format", "txt")
        if fmt not in ext_map:
            continue
        title = Path(c["filename"]).stem
        body = c["content"]
        if fmt == "png":
            data = render_png(title, body, args.font)
        elif fmt == "pdf-scan":
            data = build_pdf_scan(title, body, args.font)
        else:
            data = build_pdf_text(title, body, args.font)
        name = f"{c['id']}.{ext_map[fmt]}"
        (outdir / name).write_bytes(data)
        n += 1
        print(f"[gen] {name} ({fmt}, {len(data)} bytes)", file=sys.stderr)

    print(f"[gen] wrote {n} files to {outdir}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
