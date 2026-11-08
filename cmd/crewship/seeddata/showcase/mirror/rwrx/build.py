"""Regenerates manifest.json and data-tasks.json from the verbatim original.

    python3 -I build.py <path to d3 v7.9.0 dist/d3.min.js>

The page (original/index.html) and the rows (original/sample-crew-tasks.json)
are the original's bytes. host/ holds what the original's dashboard host
provided and the Crewship frame does not: the design tokens and the `dash`
API. d3 is inlined because the artifact runtime runs inline scripts only.
"""

import json
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
D3_VERSION = "v7.9.0"


def main() -> None:
    d3 = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
    if not d3.startswith("// https://d3js.org " + D3_VERSION + " "):
        raise SystemExit("expected d3 " + D3_VERSION + " minified build")
    for needle in ("</script", "<!--"):
        if needle in d3:
            raise SystemExit("d3 build cannot be inlined: contains " + needle)
    store = json.loads((HERE / "original/store.json").read_text(encoding="utf-8"))
    page = (HERE / "original/index.html").read_text(encoding="utf-8")
    runtime = (HERE / "host/runtime.js").read_text(encoding="utf-8")
    theme = (HERE / "host/theme.css").read_text(encoding="utf-8")
    title = store["dash/meta"]["title"]
    lang = store["dash/meta"]["lang"]

    html = (
        "<!doctype html>\n"
        f'<html lang="{lang}">\n<head>\n<meta charset="utf-8">\n'
        '<meta name="viewport" content="width=device-width, initial-scale=1">\n'
        f"<title>{title}</title>\n"
        f"<script>\n{d3.rstrip()}\n</script>\n"
        f"<script>\n{runtime.rstrip()}\n</script>\n"
        "</head>\n<body>\n"
        f'<div id="dash-root" data-dash-one-writer>\n{page.rstrip()}\n</div>\n'
        "<script>window.__dashStart()</script>\n"
        "</body>\n</html>\n"
    )
    manifest = {
        "entrypoint": "index.html",
        "files": [
            {"path": "index.html", "media_type": "text/html", "content": html},
            {"path": "theme.css", "media_type": "text/css", "content": theme},
            {"path": "interactive.json", "media_type": "application/json", "content": '{"schema_version": 1}'},
            {"path": "dashboard.json", "media_type": "application/json", "content": '{"schema_version": 1, "collections": ["tasks"]}'},
        ],
        "message": "Mirror of the reference dashboard",
    }
    (HERE / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False) + "\n", encoding="utf-8")

    rows = json.loads((HERE / "original/sample-crew-tasks.json").read_text(encoding="utf-8"))
    batch = {
        "base_version": 1,
        "documents": [{"id": "t%02d" % (i + 1), "ifVersion": 0, "value": row} for i, row in enumerate(rows)],
    }
    (HERE / "data-tasks.json").write_text(json.dumps(batch, ensure_ascii=False) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
