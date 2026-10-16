#!/usr/bin/env python3
"""Read and rasterize the owned PDFKit or LibreOffice PDFs with pinned optional PyMuPDF."""
import json
from pathlib import Path
import sys
import pymupdf

source = Path(sys.argv[1])
out = Path(sys.argv[2])
out.mkdir(parents=True, exist_ok=False)
with pymupdf.open(source) as document:
    pages = []
    for index, page in enumerate(document):
        image = out / ("page-" + str(index + 1) + ".png")
        page.get_pixmap(matrix=pymupdf.Matrix(96 / 72, 96 / 72), alpha=False).save(image)
        spans = [span for block in page.get_text("dict")["blocks"] if "lines" in block
                 for line in block["lines"] for span in line["spans"]]
        pages.append({"index": index, "widthPoints": page.rect.width, "heightPoints": page.rect.height,
                      "text": page.get_text(), "spans": spans,
                      "images": [{"width": entry[2], "height": entry[3]} for entry in page.get_images()],
                      "vectorPaths": len(page.get_drawings()), "png": image.name})
    report = {"source": source.name, "engine": pymupdf.VersionBind, "pages": pages}
    (out / "pdf-report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
print(json.dumps({"pdf": source.name, "pages": len(pages)}))
