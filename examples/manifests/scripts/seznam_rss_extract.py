#!/usr/bin/env python3
"""Turn Seznam's RSS XML into a small, deterministic JSON model input."""

import html
import json
import re
import sys
import xml.etree.ElementTree as ET


def clean(value: str, limit: int) -> str:
    value = re.sub(r"<[^>]+>", " ", html.unescape(value or ""))
    value = " ".join(value.split())
    return value[:limit]


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: seznam_rss_extract.py '<rss-xml>'", file=sys.stderr)
        return 2

    try:
        root = ET.fromstring(sys.argv[1])
    except ET.ParseError as exc:
        print(f"invalid RSS XML: {exc}", file=sys.stderr)
        return 1

    items = []
    for item in root.findall("./channel/item")[:15]:
        items.append(
            {
                "title": clean(item.findtext("title", ""), 240),
                "url": clean(item.findtext("link", ""), 500),
                "summary": clean(item.findtext("description", ""), 360),
                "published": clean(item.findtext("pubDate", ""), 100),
            }
        )

    if not items:
        print("RSS feed contained no items", file=sys.stderr)
        return 1

    print(json.dumps(items, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
