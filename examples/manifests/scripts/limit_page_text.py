#!/usr/bin/env python3
"""Keep generated narrative text within the narrative.v1 schema limit."""

import sys
import re


MAX_CHARS = 1900


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: limit_page_text.py '<text>'", file=sys.stderr)
        return 2

    # narrative.v1 deliberately has no free-form link field. Keep source URLs
    # in upstream audit outputs, but never smuggle them into rendered prose.
    text = re.sub(r"https?://\S+", "", sys.argv[1]).strip()
    if len(text) <= MAX_CHARS:
        print(text)
        return 0

    clipped = text[: MAX_CHARS - 2]
    boundary = clipped.rfind("\n")
    if boundary >= MAX_CHARS // 2:
        clipped = clipped[:boundary]
    print(clipped.rstrip() + " …")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
