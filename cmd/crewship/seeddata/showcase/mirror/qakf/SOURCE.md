# Template Crewship Docs (mirror `qakf`)

- **Original:** https://claude.ai/artifact/QakFfQpVt3oFvMVqRYFi6B, a Claude Docs
  document (Claude Docs id `bef8639a-4e4c-4718-be5a-64368dd61610`). The link was
  handed over as `#3d50fd13-5693`, which is the doc's first tab, **Přehled**.
  Read on 2026-11-08 at tab revision 19 (Přehled) and 1 (Poznámky, Tab 1).
- **Crewship kind:** Docs, mode Page: a native Tiptap/Yjs document, schema 4.
- **Seed slug / name:** `ref-template-crewship-docs` / `Template Crewship Docs`.
- **Seeded by:** `cmd/crewship/cmd_seed_mirror_qakf.go`:
  `POST /api/v1/pages` (allow_empty), then `POST /api/v1/pages/{slug}/document/initialize`
  with `{id, schema: 4, page_id, body: document.json}`, then one
  `POST /api/v1/pages/{slug}/document/comments` per thread in `threads.json`:
  the same requests as `crewship page create --allow-empty`,
  `crewship doc initialize` and `crewship doc comment`. It needs a server with
  Docs on (`CREWSHIP_DOCUMENTS_ENABLED=true`); without it the item is skipped
  with one line. Threads have fixed ids; a run adds only the missing ones, and
  only where the document still holds the quoted text at the same place. A
  page an older seed created at schema 3 keeps its content and gets no threads.

## Files

| File | What it is |
| --- | --- |
| `document.json` | The initialize body's document tree (what the seed posts). |
| `threads.json` | The three comment threads: block id, UTF-16 range, quoted text and body. |
| `convert.py` | Regenerates `document.json` and `threads.json` from `original/`: `python3 -I convert.py` in this folder. |
| `original/tab-*.json` | Each tab exactly as the Claude Docs connector returned it (outline XML). `convert.py` checks every block against the outline's own hash (`h` = sha256 of the block text), so a copying slip fails the conversion. |
| `original/widgets.json` | The two embedded widgets' source (diagram and chart). |
| `original/comments.json` | The doc's three comment threads. |
| `original/doc.json` | Doc metadata: tabs, their order, the `Stav` dropdown options. |

## What is 1:1

Every word of both tabs that hold text, in order: 13 headings (levels 1–3),
all paragraphs, the bullet lists, the nested numbered list (numbered inside
numbered, bullets inside numbered), both check lists with their ticked item,
the block quote, both tables with their header rows, all 10 code blocks
character for character (tabs, quotes and `%%\n` included) with their
languages (latex, python, go, typescript, bash, sql, json, yaml, csv,
markdown), bold, italic, strikethrough and bold+italic, inline code
(`inline kód`, `crew ls`), every web link (`odkaz na web` and the four
artifact links in the second table) and the three comment threads with their
text, on the same quoted words. The Czech text, including the original's
typos ("Napánováno", "najedíš"), is unchanged.

## Known gaps (a Docs page cannot hold these yet)

Nothing below is faked; each is carried as close as schema 4 allows.

| Original | In the copy | Why |
| --- | --- | --- |
| Tabs (Přehled, Poznámky, empty "Tab 1"), left tab bar | One page: Přehled, then Poznámky from its own title "Pracovní poznámky". Tab 1 is empty and omitted. | A Docs page has one body, no tabs. |
| Date chips (2026-11-07, 2026-11-20) | The ISO date as text | No chips. |
| Mentions (Pavel; tab link "Poznámky") | The name as text | No mentions or tab links. |
| `Stav` dropdowns in the first table | The selected option as text | No dropdown cells. |
| Code block rendering (syntax colours, LaTeX formula, CSV grid, rendered Markdown, copy button) | Monospace code with its language label | The language is stored; no renderers per language. |
| Live diagram "Kapitán rozdělí zadání mezi agenty crew" (4 boxes, arrows) | Its title, a 4-column table of the box labels and sub-labels, and its caption | No embeds or drawings. |
| Live bar chart "Dokončené úkoly crew rostou týden od týdne" (T1–T6) | Its title, its subtitle as the chart computes it ("138 úkolů za 6 týdnů · testovací data"), a table of the six values, and its caption | No charts. |
| Cell background colours (blue header, green/amber/red status) | Uncoloured cells | Simple tables only. |
| Links inside the doc (`#…` to "Formátování textu", "Bloky kódu") | Plain words | Only HTTP(S) links are kept. |
| `https://claude.ai` | `https://claude.ai/` | The schema keeps a link only in canonical form (trailing slash). |
| The "oooo" thread selects "ej agenty a role / Zadej úkol / Popis a termín" across three list items | Anchored to its first part, "ej agenty a role" | A Docs comment anchors to one block. |
| Comment authors (Pavel), times, the "mention Claude" reply flow | The seeding account at seed time | Comments are written by the caller. |
| Byline author chip, doc sharing, suggestions mode | Page owner is the seeding account | Platform features, not content. |

The Crewship frame also shows the page name above the content, so the title
appears twice, as the original's title appears in its top bar and as its H1.
