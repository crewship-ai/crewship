"""Convert the Claude Docs original (original/*.json) into the Crewship Docs
initialize body (document.json): one schema 4 Tiptap document, and the
comment threads (threads.json) anchored to the same quoted text.

Run from this directory:  python3 -I convert.py

Every text run is copied verbatim with its marks, code languages, check
lists and quotes. What schema 4 cannot hold is mapped as SOURCE.md describes
(chips, embeds, cell colours, tabs).
"""
import hashlib
import json
import re
import xml.etree.ElementTree as ET

SCHEMA = 4
LANGUAGE = re.compile(r"^[a-z0-9][a-z0-9+#-]{0,31}$")
# The engine projects several marks in its schema order; emit them the same way.
MARK_ORDER = ("link", "bold", "italic", "strike", "code")
# comment id -> (block id, UTF-16 offset of the marker), the first marker wins.
ANCHORS = {}
HREF = re.compile(r"^https?://[^\s@/]+(/\S*)?$")


class Ids:
    """Expands the outline's short ids ('.90') to full ones and makes them schema-safe."""

    def __init__(self, prefix):
        self.prefix, self.session, self.used = prefix, None, set()

    def __call__(self, raw):
        if not raw.startswith("."):
            self.session, _, raw = raw.partition(".")
            raw = "." + raw
        value = f"{self.prefix}-{self.session}-{raw[1:]}"
        assert re.fullmatch(r"[a-zA-Z0-9_-]{1,80}", value) and value not in self.used, value
        self.used.add(value)
        return value

    def extra(self, name):
        value = f"{self.prefix}-{name}"
        assert value not in self.used, value
        self.used.add(value)
        return value


def href_of(value):
    # The schema keeps a link only when URL.href equals it: a bare origin gains its "/".
    if re.fullmatch(r"https?://[^/\s]+", value):
        value += "/"
    return value if HREF.match(value) else None


def runs(element, marks=()):
    """Inline content of one block as (text, marks) pairs; a comment marker is (None, id)."""
    out = []
    if element.text and element.tag in ("text", "bold", "italic", "strike", "code", "link", "dropdown"):
        out.append((element.text, marks))
    for child in element:
        tag = child.tag
        if tag == "text":
            out += runs(child, marks)
        elif tag in ("bold", "italic", "strike"):
            out += runs(child, marks + ({"type": tag, "attrs": {}},))
        elif tag == "code":
            out += runs(child, marks + ({"type": "code", "attrs": {}},))
        elif tag == "link":
            href = href_of(child.attrib["href"])
            out += runs(child, marks + (({"type": "link", "attrs": {"href": href}},) if href else ()))
        elif tag == "dropdown":
            out.append((child.text or "", marks))
        elif tag == "date":
            out.append((child.attrib["value"], marks))
        elif tag == "mention":
            out.append((child.attrib.get("name") or child.attrib.get("label"), marks))
        elif tag == "comment":
            out.append((None, child.attrib["id"]))
        else:
            raise ValueError(f"unexpected inline {tag}")
        if child.tail and element.tag in ("text",):
            out.append((child.tail, marks))
    return out


def utf16(text):
    return len(text.encode("utf-16-le")) // 2


def inline(element, block_id):
    merged = []
    offset = 0
    for text, marks in runs(element):
        if text is None:
            ANCHORS.setdefault(marks, (block_id, offset))
            continue
        if not text:
            continue
        offset += utf16(text)
        if any(m["type"] == "code" for m in marks):
            marks = [m for m in marks if m["type"] == "code"]  # code stands alone in every schema
        marks = sorted({m["type"]: m for m in marks}.values(), key=lambda m: MARK_ORDER.index(m["type"]))
        if merged and merged[-1].get("marks", []) == marks:
            merged[-1]["text"] += text
            continue
        node = {"type": "text", "text": text}
        if marks:
            node["marks"] = marks
        merged.append(node)
    return merged


def block_text(type_, id_, content, **attrs):
    node = {"type": type_, "attrs": {"id": id_, **attrs}}
    if content:
        node["content"] = content
    return node


def paragraph(ids, element):
    id_ = ids(element.attrib["id"])
    level = element.attrib.get("heading")
    if level:
        return block_text("heading", id_, inline(element, id_), level=int(level))
    return block_text("paragraph", id_, inline(element, id_))


def convert_list(ids, element):
    kind = element.attrib["kind"]
    items = []
    for li in element:
        assert li.tag == "listItem"
        content = []
        for child in li:
            if child.tag == "paragraph":
                content.append(paragraph(ids, child))
            elif child.tag == "list":
                content.append(convert_list(ids, child))
            else:
                raise ValueError(f"unexpected list child {child.tag}")
        if kind == "check":
            items.append({"type": "taskItem", "attrs": {"id": ids(li.attrib["id"]), "checked": li.attrib.get("checked") == "true"}, "content": content})
        else:
            items.append({"type": "listItem", "attrs": {"id": ids(li.attrib["id"])}, "content": content})
    type_ = {"ordered": "orderedList", "check": "taskList"}.get(kind, "bulletList")
    attrs = {"id": ids(element.attrib["id"])}
    if type_ == "orderedList":
        attrs["start"] = 1
    return {"type": type_, "attrs": attrs, "content": items}


def cell(type_, id_, paragraphs):
    return {"type": type_, "attrs": {"id": id_, "colspan": 1, "rowspan": 1, "colwidth": None}, "content": paragraphs}


def convert_table(ids, element):
    rows = []
    for row in element:
        cells = []
        for c in row:
            paras = [paragraph(ids, p) for p in c]
            cells.append(cell("tableHeader" if c.attrib.get("header") == "true" else "tableCell", ids(c.attrib["id"]), paras))
        rows.append({"type": "tableRow", "attrs": {"id": ids(row.attrib["id"])}, "content": cells})
    return {"type": "table", "attrs": {"id": ids(element.attrib["id"])}, "content": rows}


def simple_table(ids, name, header, rows):
    def p(key, text):
        return block_text("paragraph", ids.extra(f"{name}-{key}-p"), [{"type": "text", "text": text}])
    out = [{"type": "tableRow", "attrs": {"id": ids.extra(f"{name}-r0")},
            "content": [cell("tableHeader", ids.extra(f"{name}-r0c{i}"), [p(f"r0c{i}", t)]) for i, t in enumerate(header)]}]
    for r, values in enumerate(rows, 1):
        out.append({"type": "tableRow", "attrs": {"id": ids.extra(f"{name}-r{r}")},
                    "content": [cell("tableCell", ids.extra(f"{name}-r{r}c{i}"), [p(f"r{r}c{i}", t)]) for i, t in enumerate(values)]})
    return {"type": "table", "attrs": {"id": ids.extra(name)}, "content": out}


def plain(ids, key, text):
    return block_text("paragraph", ids.extra(key), [{"type": "text", "text": text}])


def embed(ids, element, widgets):
    """A live widget has no schema 3 node: its own title, labels and data rows become text and a table."""
    ref = element.attrib["ref"].removeprefix("node/")
    widget = widgets[ref]
    code = widget["code"]
    title = re.search(r"data-claude-text-id='title'[^>]*>([^<]+)<", code).group(1)
    out = [plain(ids, f"embed-{ref}-title", title)]
    if "claude.Visualize" in code:
        rows = [(m.group(1), m.group(2)) for m in re.finditer(r"\{week:'([^']+)', done:(\d+)\}", code)]
        total = sum(int(v) for _, v in rows)
        # The chart's own subtitle template: `${total} úkolů za ${rows.length} týdnů · testovací data`.
        out.append(plain(ids, f"embed-{ref}-subtitle", f"{total} úkolů za {len(rows)} týdnů · testovací data"))
        out.append(simple_table(ids, f"embed-{ref}", [w for w, _ in rows], [[v for _, v in rows]]))
    else:
        steps = re.findall(r"data-claude-text-id='([a-z]+)'[^>]*>([^<]+)</text><text data-claude-text-id='\1-sub'[^>]*>([^<]+)<", code)
        out.append(simple_table(ids, f"embed-{ref}", [s[1] for s in steps], [[s[2] for s in steps]]))
    out.append(plain(ids, f"embed-{ref}-caption", element.attrib["caption"]))
    return out


def convert_tab(prefix, xml, widgets):
    ids = Ids(prefix)
    out = []
    for element in ET.fromstring(xml):
        tag = element.tag
        if tag == "paragraph":
            if not "".join(element.itertext()) and not len(element):
                continue  # an empty trailing line
            out.append(paragraph(ids, element))
        elif tag == "list":
            out.append(convert_list(ids, element))
        elif tag == "table":
            out.append(convert_table(ids, element))
        elif tag == "codeBlock":
            language = element.attrib.get("language")
            assert language is None or LANGUAGE.match(language), language
            attrs = {"language": language} if language else {}
            out.append(block_text("codeBlock", ids(element.attrib["id"]), [{"type": "text", "text": "".join(element.itertext())}], **attrs))
        elif tag == "blockquote":
            out.append({"type": "blockquote", "attrs": {"id": ids(element.attrib["id"])}, "content": [paragraph(ids, p) for p in element]})
        elif tag == "embed":
            ids(element.attrib["id"])
            out += embed(ids, element, widgets)
        else:
            raise ValueError(f"unexpected block {tag}")
    return out


def verify_hashes(xml):
    """The outline's h is sha256(block text)[:8]: proof the saved original was copied without a slip."""
    for element in ET.fromstring(xml).iter():
        if element.tag in ("paragraph", "codeBlock") and "h" in element.attrib and element.find("dropdown") is None:
            got = hashlib.sha256("".join(element.itertext()).encode()).hexdigest()[:8]
            assert got == element.attrib["h"], (element.attrib.get("id"), got)


def block_texts(nodes, out):
    for node in nodes:
        if node["type"] in ("paragraph", "heading", "codeBlock"):
            out[node["attrs"]["id"]] = "".join(c["text"] for c in node.get("content", []))
        block_texts(node.get("content", []), out)
    return out


def comments(content):
    """Each thread at its first marker, as long as the original's character range, cut at the block's end.

    The original anchors by character ids; their count is the selection's length.
    Crewship anchors one block, so a selection across blocks keeps its first block."""
    texts = block_texts(content, {})
    out = []
    rows = json.load(open("original/comments.json", encoding="utf-8"))["rows"]
    for row in sorted(rows, key=lambda r: r["seq"]):
        value = row["payload"]["value"]
        start, end = (int(x.rsplit(".", 1)[1]) for x in value["parent"]["anchor"]["range"])
        block, offset = ANCHORS[row["id"]]
        units = texts[block].encode("utf-16-le")
        to = min(offset + end - start + 1, len(units) // 2)
        quote = units[offset * 2:to * 2].decode("utf-16-le")
        assert quote and (value["parent"].get("label") or quote).startswith(quote), (row["id"], quote)
        out.append({"id": f"seed-qakf-{row['id']}", "block": block, "from": offset, "to": to, "quote": quote, "text": value["body"]})
    return out


def main():
    widgets = json.load(open("original/widgets.json", encoding="utf-8"))
    content = []
    for prefix, name in (("prehled", "tab-prehled.json"), ("poznamky", "tab-poznamky.json"), ("tab1", "tab-1.json")):
        xml = json.load(open(f"original/{name}", encoding="utf-8"))["xml"]
        verify_hashes(xml)
        content += convert_tab(prefix, xml, widgets)
    body = {"type": "doc", "content": content}
    for name, value in (("document.json", body), ("threads.json", comments(content))):
        with open(name, "w", encoding="utf-8") as f:
            json.dump(value, f, ensure_ascii=False, indent=1)
            f.write("\n")


if __name__ == "__main__":
    main()
