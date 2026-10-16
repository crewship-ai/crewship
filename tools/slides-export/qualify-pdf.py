#!/usr/bin/env python3
"""Opt-in assertions over actual PDFs, using an externally supplied PyMuPDF."""
import argparse
import hashlib
import json
from pathlib import Path
import unicodedata
import pymupdf


def normalized(text):
    return "".join(unicodedata.normalize("NFKC", text).split())


def inspect(path, envelope, exact_geometry):
    result = {"file": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "pages": []}
    with pymupdf.open(path) as document:
        assert len(document) == len(envelope["deck"]["slide_order"])
        for page, slide_id in zip(document, envelope["deck"]["slide_order"]):
            slide = next(s for s in envelope["deck"]["slides"] if s["id"] == slide_id)
            assert abs(page.rect.width - 720) < 0.03 and abs(page.rect.height - 405) < 0.03
            actual_text = normalized(page.get_text())
            spans = [span for b in page.get_text("dict")["blocks"] if "lines" in b for line in b["lines"] for span in line["spans"]]
            assert all("DejaVuSans" in span["font"] for span in spans)
            for e in slide["elements"]:
                if e["type"] == "text":
                    assert normalized(e["text"]["value"]) in actual_text, (path.name, e["id"])
                elif e["type"] == "table":
                    assert all(normalized(cell) in actual_text for row in e["table"]["cells"] for cell in row)
                    # Fixture qualification requires unique cell lines, making
                    # actual assignment explicit instead of accepting whole-table bounds.
                    cell_w = e["width"] / len(e["table"]["cells"][0])
                    cell_h = e["height"] / len(e["table"]["cells"])
                    for r, row in enumerate(e["table"]["cells"]):
                        for c, value in enumerate(row):
                            cell_box = pymupdf.Rect(*(v * .75 for v in [e["x"] + c * cell_w, e["y"] + r * cell_h, e["x"] + (c + 1) * cell_w, e["y"] + (r + 1) * cell_h]))
                            for line in value.split("\n"):
                                if not line.strip():
                                    continue
                                matches = page.search_for(line)
                                assert len(matches) == 1, "fixture cell lines must be uniquely assignable"
                                assert (cell_box + (-.1, -.1, .1, .1)).contains(matches[0]), (path.name, r, c, line, matches[0])
                elif e["type"] == "image":
                    images = [pymupdf.Pixmap(document, image[0]) for image in page.get_images() if image[2:4] == (96, 64)]
                    assert images, "source raster must remain an image"
                    expected = bytes(channel for y in range(64) for x in range(96) for channel in (x * 2, y * 3, 150))
                    assert any(image.n == 3 and image.samples == expected for image in images), "raster pixel samples changed"
                if e["id"] == "fallback_shape":
                    matches = [draw for draw in page.get_drawings() if abs(draw["rect"].x0 - 36) < 0.06 and abs(draw["rect"].y0 - 187.5) < 0.06 and abs(draw["rect"].width - 300) < 0.06]
                    assert len(matches) == 1 and matches[0]["type"] == "f", "zero stroke must be fill-only, never a PDF hairline"
            # Actual text outside page is always wrong. For both qualified generators
            # additionally check each span lies within some declared text/cell box.
            text_boxes = []
            for e in slide["elements"]:
                if e["type"] in ["text", "table"]:
                    text_boxes.append(pymupdf.Rect(*(value * 0.75 for value in [e["x"], e["y"], e["x"] + e["width"], e["y"] + e["height"]])))
            for span in spans:
                rect = pymupdf.Rect(span["bbox"])
                assert (page.rect + (-0.1, -0.1, 0.1, 0.1)).contains(rect)
                if exact_geometry:
                    assert any((box + (-0.1, -0.1, 0.1, 0.1)).contains(rect) for box in text_boxes), (slide_id, span)
            result["pages"].append({"slide_id": slide_id, "size_points": [page.rect.width, page.rect.height], "span_count": len(spans), "text_sha256_normalized": hashlib.sha256(actual_text.encode()).hexdigest(), "text_bbox_points": [min(s["bbox"][0] for s in spans), min(s["bbox"][1] for s in spans), max(s["bbox"][2] for s in spans), max(s["bbox"][3] for s in spans)]})
    return result


parser = argparse.ArgumentParser()
parser.add_argument("--envelope", required=True)
parser.add_argument("--pdfkit", required=True)
parser.add_argument("--libreoffice", required=True)
parser.add_argument("--output", required=True)
args = parser.parse_args()
envelope = json.loads(Path(args.envelope).read_text())
report = {"kind": "actual PDF application parse/content/pixel/vector assertions", "engine": pymupdf.VersionBind,
          "pdfkit": inspect(Path(args.pdfkit), envelope, True), "libreoffice": inspect(Path(args.libreoffice), envelope, True),
          "qualification": "Both generators text/unique-cell bounds verified for this corpus; any-box text matching is not an arbitrary overlapping-layout proof; PowerPoint remains incomplete"}
Path(args.output).write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
print(json.dumps({"pages_per_generator": 5, "checks": ["full Czech and long text", "unique LF table cell contents/bounds", "native image exact RGB pixels", "fill-only zero stroke", "both generators declared text boxes", "actual page dimensions"]}))
