#!/usr/bin/env python3
"""Regenerate m-nwpv.json, the App manifest the seed posts, from the verbatim
claude.ai originals in _original/project/ plus _dc.js and _fonts.css (published as dc.js and fonts.css).

    python3 -I _build.py        (from this directory)

Not embedded in the binary (leading underscore). The transformation of each
artboard is the one SOURCE.md documents and the Go test
TestMirrorNwpv_ArtboardsAreTheOriginals re-applies; keep the three in step.
"""
import html
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ORIGINAL = os.path.join(HERE, "_original", "project")

SUPPORT = '<script src="./support.js"></script>'
RUNTIME = '<link rel="stylesheet" href="fonts.css">\n<script src="dc.js"></script>'
GOOGLE_FONTS = '<link rel="stylesheet" href="https://fonts.googleapis.com/'


def published(name):
    """Main.dc.html -> Main.html: the Crewship screen menu names a screen by its file."""
    return name[: -len(".dc.html")] + ".html"


def convert(source, boards):
    if source.count(SUPPORT) != 1:
        raise SystemExit("support.js line missing or repeated")
    out = source.replace(SUPPORT, RUNTIME)
    lines = [line for line in out.split("\n") if not line.startswith(GOOGLE_FONTS)]
    if len(lines) != out.count("\n"):
        raise SystemExit("expected exactly one Google Fonts line")
    out = "\n".join(lines)
    for board in boards:
        out = out.replace('href="%s"' % board, 'href="%s"' % published(board))
    return out


FILL = {"orange": "#FCE9D9", "blue": "#E1E9F3", "green": "#DDEFE6", "purple": "#ECE3F6", "gray": "#ECE6DA",
        "red": "#F8DEDC", "teal": "#D9EFEC", "pink": "#F8E1EC"}
SCALE = 0.25


def overview(canvas):
    e = html.escape
    pages = canvas["pages"]
    first = pages[0]["id"]
    out = ['<!doctype html>\n<html lang="cs">\n<head>\n<meta charset="utf-8">\n'
           '<meta name="viewport" content="width=device-width, initial-scale=1">\n'
           "<title>%s</title>\n" % e(canvas["title"]),
           '<link rel="stylesheet" href="fonts.css">\n<style>\n'
           ":root{--bg:#ECE6DA;--ink:#0F1F33;--muted:#4A5568;--board:#FFFDF8;--line:#C9C1B1;--accent:#B85E1F}\n"
           "*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.5 'IBM Plex Sans',Arial,sans-serif}\n"
           "header{padding:24px 16px 8px;max-width:1240px;margin:0 auto}h1{margin:0;font-size:28px;line-height:34px}\n"
           "header p{margin:4px 0 0;color:var(--muted)}nav{display:flex;flex-wrap:wrap;gap:8px;margin-top:16px}\n"
           "nav a{display:inline-flex;align-items:center;min-height:44px;padding:0 14px;border-radius:12px;background:var(--board);border:1px solid var(--line);color:var(--ink);text-decoration:none;font-weight:600}\n"
           "section{max-width:1240px;margin:0 auto;padding:16px}h2{margin:16px 0 8px;font-size:20px}\n"
           ".map{overflow:auto;border-radius:16px;background:#E2DCCF;padding:16px;-webkit-overflow-scrolling:touch}\n"
           ".plane{position:relative}.title1{position:absolute;font-weight:700;font-size:18px;line-height:22px;white-space:nowrap}\n"
           ".sticky{position:absolute;padding:8px;border-radius:6px;font-size:9px;line-height:12px;color:#0F1F33;box-shadow:0 1px 2px rgba(15,31,51,.18)}\n"
           ".board{position:absolute;display:flex;flex-direction:column;justify-content:flex-end;gap:2px;padding:6px;border-radius:6px;background:var(--board);border:1px solid var(--line);color:var(--ink);text-decoration:none;font-size:10px;line-height:13px;overflow:hidden}\n"
           ".board:hover,.board:focus-visible{outline:2px solid var(--accent);outline-offset:1px}.board b{font-weight:600}.board span{color:var(--muted);font-family:'JetBrains Mono','Courier New',monospace;font-size:9px}\n"
           "ul{list-style:none;margin:12px 0 0;padding:0;display:grid;gap:8px}li a{display:flex;flex-wrap:wrap;justify-content:space-between;gap:4px 12px;min-height:44px;align-items:center;padding:10px 14px;border-radius:12px;background:var(--board);border:1px solid var(--line);color:var(--ink);text-decoration:none}\n"
           "li small{color:var(--muted);font-family:'JetBrains Mono','Courier New',monospace}\n"
           "</style>\n</head>\n<body>\n<header>\n<h1>%s</h1>\n" % e(canvas["title"]),
           "<p>%d artboards on %d pages. Each artboard opens as its own screen.</p>\n<nav aria-label=\"Pages\">\n" % (len(canvas["order"]), len(pages))]
    for page in pages:
        out.append('<a href="#page-%s">%s</a>\n' % (e(page["id"]), e(page["name"])))
    out.append("</nav>\n</header>\n")
    for page in pages:
        pid = page["id"]
        boards = [(name, canvas["boards"][name]) for name in canvas["order"] if canvas["boards"][name].get("page", first) == pid]
        notes = [(nid, n) for nid, n in canvas["notes"].items() if n.get("page", first) == pid]
        xs = [b["x"] for _, b in boards] + [n["x"] for _, n in notes]
        ys = [b["y"] for _, b in boards] + [n["y"] for _, n in notes]
        x2 = [b["x"] + b["w"] for _, b in boards] + [n["x"] + n.get("w", n.get("maxW", 300)) for _, n in notes]
        y2 = [b["y"] + b["h"] for _, b in boards] + [n["y"] + (100 if n.get("kind") == "title1" else n.get("w", 300) * 4 / 3) for _, n in notes]
        ox, oy = min(xs), min(ys)
        width, height = (max(x2) - ox) * SCALE, (max(y2) - oy) * SCALE
        out.append('<section id="page-%s" aria-labelledby="h-%s">\n<h2 id="h-%s">%s</h2>\n' % (e(pid), e(pid), e(pid), e(page["name"])))
        out.append('<div class="map"><div class="plane" style="width:%dpx;height:%dpx">\n' % (round(width), round(height)))
        for nid, n in notes:
            left, top = (n["x"] - ox) * SCALE, (n["y"] - oy) * SCALE
            if n.get("kind") == "title1":
                out.append('<div class="title1" style="left:%dpx;top:%dpx">%s</div>\n' % (round(left), round(top), e(n["text"])))
            else:
                out.append('<div class="sticky" style="left:%dpx;top:%dpx;width:%dpx;background:%s">%s</div>\n'
                           % (round(left), round(top), round(n.get("w", 300) * SCALE), FILL.get(n.get("fill", "gray"), FILL["gray"]), e(n["text"])))
        for name, b in boards:
            left, top = (b["x"] - ox) * SCALE, (b["y"] - oy) * SCALE
            out.append('<a class="board" href="%s" style="left:%dpx;top:%dpx;width:%dpx;height:%dpx" aria-label="%s"><b>%s</b><span>%d × %d</span></a>\n'
                       % (published(name), round(left), round(top), round(b["w"] * SCALE), round(b["h"] * SCALE),
                          e(b.get("title", name)), e(b.get("title", name)), b["w"], b["h"]))
        out.append("</div></div>\n<ul>\n")
        for name, b in boards:
            extra = " · interactive" if b.get("is_interactive") else ""
            extra += " · fluid page" if b.get("expand") == "fill" else ""
            out.append('<li><a href="%s"><span>%s</span><small>%d × %d%s</small></a></li>\n' % (published(name), e(b.get("title", name)), b["w"], b["h"], extra))
        out.append("</ul>\n</section>\n")
    out.append("</body>\n</html>\n")
    return "".join(out)


def main():
    with open(os.path.join(ORIGINAL, "canvas.json"), encoding="utf-8") as f:
        canvas = json.load(f)
    boards = canvas["order"]
    files = [{"path": "index.html", "media_type": "text/html", "content": overview(canvas)}]
    for name in boards:
        with open(os.path.join(ORIGINAL, name), encoding="utf-8") as f:
            files.append({"path": published(name), "media_type": "text/html", "content": convert(f.read(), boards)})
    for name, media in (("dc.js", "text/javascript"), ("fonts.css", "text/css")):
        with open(os.path.join(HERE, "_" + name), encoding="utf-8") as f:
            files.append({"path": name, "media_type": media, "content": f.read()})
    manifest = {"entrypoint": "index.html", "files": files, "message": "Mirror of the claude.ai Design canvas Template Crewship Design"}
    with open(os.path.join(HERE, "m-nwpv.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=1)
        f.write("\n")
    print("m-nwpv.json: %d files" % len(files), file=sys.stderr)


if __name__ == "__main__":
    main()
