# Template Crewship Docs (mirror `qakf`)

- **Original:** https://claude.ai/artifact/QakFfQpVt3oFvMVqRYFi6B, a Claude Docs
  document (Claude Docs id `bef8639a-4e4c-4718-be5a-64368dd61610`). The link was
  handed over as `#3d50fd13-5693`, which is the doc's first tab, **Přehled**.
  Read on 2026-11-08 at tab revision 19 (Přehled) and 1 (Poznámky, Tab 1).
- **Crewship kind:** Docs, mode Page: a native Tiptap/Yjs document, schema 3.
- **Seed slug / name:** `ref-template-crewship-docs` / `Template Crewship Docs`.
- **Seeded by:** `cmd/crewship/cmd_seed_mirror_qakf.go`:
  `POST /api/v1/pages` (allow_empty), then `POST /api/v1/pages/{slug}/document/initialize`
  with `{id, schema: 3, page_id, body: document.json}`, the same request as
  `crewship page create --allow-empty` plus `crewship doc initialize`. It needs
  a server with Docs on (`CREWSHIP_DOCUMENTS_ENABLED=true`); without it the
  item is skipped with one line.

## Files

| File | What it is |
| --- | --- |
| `document.json` | The initialize body's document tree (what the seed posts). |
| `convert.py` | Regenerates `document.json` from `original/`: `python3 -I convert.py` in this folder. |
| `original/tab-*.json` | Each tab exactly as the Claude Docs connector returned it (outline XML). `convert.py` checks every block against the outline's own hash (`h` = sha256 of the block text), so a copying slip fails the conversion. |
| `original/widgets.json` | The two embedded widgets' source (diagram and chart). |
| `original/comments.json` | The doc's three comment threads. |
| `original/doc.json` | Doc metadata: tabs, their order, the `Stav` dropdown options. |

## What is 1:1

Every word of both tabs that hold text, in order: 13 headings (levels 1–3),
all paragraphs, the bullet lists, the nested numbered list (numbered inside
numbered, bullets inside numbered), both tables with their header rows, all
10 code blocks character for character (tabs, quotes and `%%\n` included),
inline code (`inline kód`, `crew ls`) and every web link (`odkaz na web` and
the four artifact links in the second table). The Czech text, including the
original's typos ("Napánováno", "najedíš"), is unchanged.

## Known gaps (a Docs page cannot hold these yet)

Docs schema 3 has headings, paragraphs, lists, simple tables, code blocks and
two marks, inline code and web link, one per run. Everything else is carried
as close as that allows; nothing is faked.

| Original | In the copy | Why |
| --- | --- | --- |
| Tabs (Přehled, Poznámky, empty "Tab 1"), left tab bar | One page: Přehled, then Poznámky from its own title "Pracovní poznámky". Tab 1 is empty and omitted. | A Docs page has one body, no tabs. |
| Bold, italic, strikethrough, bold+italic | Plain words | No such marks in schema 3. |
| Date chips (2026-11-07, 2026-11-20) | The ISO date as text | No chips. |
| Mentions (Pavel; tab link "Poznámky") | The name as text | No mentions or tab links. |
| `Stav` dropdowns in the first table | The selected option as text | No dropdown cells. |
| Check lists (one item ticked) | Bullet lists, items prefixed ☑ / ☐ | No task lists. |
| Block quote | A plain paragraph | No quote block. |
| Code block languages (latex, python, go, typescript, bash, sql, json, yaml, csv, markdown) and their rendering (syntax colours, LaTeX formula, CSV grid, rendered Markdown, copy button) | Plain code blocks; the label above each block names the language | Code blocks carry no language in schema 3. |
| Live diagram "Kapitán rozdělí zadání mezi agenty crew" (4 boxes, arrows) | Its title, a 4-column table of the box labels and sub-labels, and its caption | No embeds or drawings. |
| Live bar chart "Dokončené úkoly crew rostou týden od týdne" (T1–T6) | Its title, its subtitle as the chart computes it ("138 úkolů za 6 týdnů · testovací data"), a table of the six values, and its caption | No charts. |
| Cell background colours (blue header, green/amber/red status) | Uncoloured cells | Simple tables only. |
| Links inside the doc (`#…` to "Formátování textu", "Bloky kódu") | Plain words | Only HTTP(S) links are kept. |
| `https://claude.ai` | `https://claude.ai/` | The schema keeps a link only in canonical form (trailing slash). |
| Comments: "Ukázka komentáře: odpověz tady nebo zmiň Clauda a změnu zapracuje." on "Bloky kódu"; "🎉" on the CSV paragraph; "oooo" on "Přidej agenty a role / Zadej úkol / Popis a termín" | Not seeded | The Docs route offers no comments (`can_comment: false`). |
| Byline author chip, doc sharing, suggestions mode | Page owner is the seeding account | Platform features, not content. |

The Crewship frame also shows the page name above the content, so the title
appears twice, as the original's title appears in its top bar and as its H1.
