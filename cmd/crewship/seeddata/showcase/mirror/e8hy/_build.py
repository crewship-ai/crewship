"""Regenerates m-e8hy.json from the verbatim original (original-index.html.txt).

    python3 -I _build.py <cache dir>

The Crewship interactive runtime has no network and runs inline scripts only,
so the page's three external resources become local, pinned copies:

- three.js r128 (MIT), the same file the original loads from cdnjs, inlined
  in place of its <script src> tag;
- Mermaid 8.11.5 (MIT), inlined in place of the claude.ai host runtime tag
  (/_runtime/mermaid-11.16.1.min.js). Mermaid 11 does not fit the 2 MiB
  interactive limit next to three.js; 8.11.5 is the newest release that does.
  A short shim gives its callback render() the Promise shape the page's
  runtime block expects;
- the page's Google Fonts faces (OFL), latin and latin-ext subsets, as data
  URIs in fonts.css, which the runtime adds to the page; the <link> tag is
  replaced by a marker comment.

Every other byte of index.html is unchanged; the Go test reverses the three
substitutions and compares with the original. Downloads are verified by
sha256 and kept in the cache dir.
"""

import base64
import hashlib
import json
import pathlib
import sys
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent

THREE = ("https://cdnjs.cloudflare.com/ajax/libs/three.js/r128/three.min.js",
         "9274bbcec8d96168626c732b5d31c775aa8cfb7eaa0599bec0c175908a2c1ce2")
MERMAID = ("https://cdn.jsdelivr.net/npm/mermaid@8.11.5/dist/mermaid.min.js",
           "d972d792daa8eb7d66c5dd20fb05f8ded19689444ccfa25895e905b3427c876b")
GF = "https://fonts.gstatic.com/s/"
LATIN_EXT = ("U+0100-02BA, U+02BD-02C5, U+02C7-02CC, U+02CE-02D7, U+02DD-02FF, U+0304, U+0308, U+0329, "
             "U+1D00-1DBF, U+1E00-1E9F, U+1EF2-1EFF, U+2020, U+20A0-20AB, U+20AD-20C4, U+2113, U+2C60-2C7F, U+A720-A7FF")
LATIN = ("U+0000-00FF, U+0131, U+0152-0153, U+02BB-02BC, U+02C6, U+02DA, U+02DC, U+0304, U+0308, U+0329, "
         "U+2000-206F, U+20AC, U+2122, U+2191, U+2193, U+2212, U+2215, U+FEFF, U+FFFD")
# family, weight range, subset, url, sha256 (variable fonts: one file per subset)
FONTS = [
    ("Bricolage Grotesque", "500 700", LATIN_EXT, GF + "bricolagegrotesque/v9/3y9K6as8bTXq_nANBjzKo3IeZx8z6up5BeSl9D4dj_x9PpZBMlGGInHEVA.woff2",
     "776f6dcaf03636cd69a5802c94808cc8896c0a66c8e9ce0fe147231b6ee01957"),
    ("Bricolage Grotesque", "500 700", LATIN, GF + "bricolagegrotesque/v9/3y9K6as8bTXq_nANBjzKo3IeZx8z6up5BeSl9D4dj_x9PpZBMlGIInE.woff2",
     "a79fdb52d4a5c76552452f69202add96e287401fff03d3e8c0e38b4dcb5a99cd"),
    ("IBM Plex Sans", "400 600", LATIN_EXT, GF + "ibmplexsans/v23/zYXzKVElMYYaJe8bpLHnCwDKr932-G7dytD-Dmu1syxQKYbABA.woff2",
     "d160e20920ae4d6556518d352d3af27a74e9b0de3d8fe17b1c1044fc75aa2f81"),
    ("IBM Plex Sans", "400 600", LATIN, GF + "ibmplexsans/v23/zYXzKVElMYYaJe8bpLHnCwDKr932-G7dytD-Dmu1syxeKYY.woff2",
     "e2291e842cf5af167122a22881a740c7f2dda7716f1e8cd76680264f4a859470"),
    ("JetBrains Mono", "400 600", LATIN_EXT, GF + "jetbrainsmono/v24/tDbv2o-flEEny0FZhsfKu5WU4zr3E_BX0PnT8RD8yKwBNntkaToggR7BYRbKPx7cwhsk.woff2",
     "db5ff4db83e580426280e9337a58dc57d3a83784a1b03ad80914651594441d52"),
    ("JetBrains Mono", "400 600", LATIN, GF + "jetbrainsmono/v24/tDbv2o-flEEny0FZhsfKu5WU4zr3E_BX0PnT8RD8yKwBNntkaToggR7BYRbKPxDcwg.woff2",
     "83c005d49d8a6a50474c73a5a36ac0468076e9c4a29da7bdb14995d80560a5be"),
]
THREE_TAG = '<script src="https://cdnjs.cloudflare.com/ajax/libs/three.js/r128/three.min.js"></script>'
MERMAID_TAG = '<script src="/_runtime/mermaid-11.16.1.min.js"></script>'
FONTS_TAG = ('<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Bricolage+Grotesque:opsz,wght@12..96,500;12..96,700'
             '&family=IBM+Plex+Sans:wght@400;600&family=JetBrains+Mono:wght@400;600&display=swap">')
THREE_OPEN = '<script data-mirror="three.js r128">'
MERMAID_OPEN = '<script data-mirror="mermaid 8.11.5">'
SHIM = ('<script data-mirror="mermaid render shim">(function(){if(typeof mermaid==="undefined")return;'
        'var render=mermaid.render.bind(mermaid);mermaid.render=function(id,src){return new Promise(function(ok,no){'
        'try{render(id,src,function(svg){ok({svg:svg})})}catch(e){no(e)}})}})();</script>')
FONTS_MARK = "<!--mirror: Google Fonts faces are in fonts.css-->"


def fetch(cache: pathlib.Path, url: str, sha: str) -> bytes:
    path = cache / url.rsplit("/", 1)[-1]
    if not path.exists():
        path.write_bytes(urllib.request.urlopen(url).read())
    data = path.read_bytes()
    got = hashlib.sha256(data).hexdigest()
    if got != sha:
        raise SystemExit(f"{url}: sha256 {got}, want {sha}")
    return data


def inline(js: str, name: str) -> str:
    for needle in ("</script", "<!--"):
        if needle in js.lower():
            raise SystemExit(f"{name} cannot be inlined: contains {needle}")
    return js.rstrip("\n")


def main() -> None:
    cache = pathlib.Path(sys.argv[1])
    cache.mkdir(parents=True, exist_ok=True)
    page = (HERE / "original-index.html.txt").read_text(encoding="utf-8")
    for tag in (THREE_TAG, MERMAID_TAG, FONTS_TAG):
        if page.count(tag) != 1:
            raise SystemExit("original no longer has exactly one " + tag)
    three = inline(fetch(cache, *THREE).decode("utf-8"), "three.js")
    mermaid = inline(fetch(cache, *MERMAID).decode("utf-8"), "mermaid")
    page = page.replace(THREE_TAG, THREE_OPEN + three + "</script>")
    page = page.replace(MERMAID_TAG, MERMAID_OPEN + "/*! mermaid 8.11.5 | MIT License | https://github.com/mermaid-js/mermaid */\n" + mermaid + "</script>" + SHIM)
    page = page.replace(FONTS_TAG, FONTS_MARK)

    faces = ["/* Bricolage Grotesque, IBM Plex Sans and JetBrains Mono: SIL Open Font License 1.1, via Google Fonts. */"]
    for family, weight, ranges, url, sha in FONTS:
        data = fetch(cache, url, sha)
        faces.append(
            "@font-face{font-family:'%s';font-style:normal;font-weight:%s;font-display:swap;"
            "src:url(data:font/woff2;base64,%s) format('woff2');unicode-range:%s}"
            % (family, weight, base64.b64encode(data).decode("ascii"), ranges))
    fonts = "\n".join(faces) + "\n"

    manifest = {
        "entrypoint": "index.html",
        "files": [
            {"path": "index.html", "media_type": "text/html", "content": page},
            {"path": "fonts.css", "media_type": "text/css", "content": fonts},
            {"path": "interactive.json", "media_type": "application/json", "content": '{"schema_version": 1}'},
            {"path": "dashboard.json", "media_type": "application/json", "content": '{"schema_version": 1, "collections": ["showcase"]}'},
        ],
        "message": "Reference copy of claude.ai artifact E8HYX1pSVKSAbLqgvTCZ8Z",
    }
    (HERE / "m-e8hy.json").write_text(json.dumps(manifest, ensure_ascii=False) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
