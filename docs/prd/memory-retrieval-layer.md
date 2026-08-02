# Design — The memory retrieval and storage layer

Status: draft · 2026-08-02 · Companion to `agent-memory-on-wake.md` and
`HANDOFF-2026-08-02.md` §5 · **Blocks #1669**

> **Scope.** How memory is stored and found. Not what to remember, not how to
> decide what is a fact — three research passes already covered that and are
> condensed in `HANDOFF-2026-08-02.md` §3. This document is the missing fourth
> pass.
>
> All `file:line` references verified against `research/memory-retrieval-layer`
> (branched from `fix/aux-reach-probes-the-slots-endpoint`) on 2026-08-02.
> Every number marked **[M]** was measured by
> `scripts/memory-retrieval-bench` on this machine; every claim marked **[I]**
> is inferred and says what it rests on. Re-run before trusting a number:
>
> ```
> go run -tags memorybench ./scripts/memory-retrieval-bench
> ```

---

## 0. Verdict

**The index is fine. The query is not.**

The storage layer does almost everything right: WAL, a sane synchronous level,
an incremental per-file reindex, a symlink-hardened walker, transactional
rebuilds. The measured cost of keeping the index current is **86 µs per write**
**[M]** and it does not grow with corpus size.

What is broken is the two lines that turn a human question into an FTS5
expression. `memory.search` joins every word of the query with FTS5's implicit
**AND**, so an eight-word question demands all eight words inside one ~500-char
chunk. Against a realistic corpus, **8 of 10 natural-language queries return
zero rows** **[M]** — not bad rows, *zero*. The `[MEMORY GAP]` block tells a
woken agent "Before you start: memory.search the project or task you are
picking up" (`orchestrator/memory.go:362`), and for most phrasings that
instruction returns an empty array.

Everything else in §5 of the handoff — tokenizer, prefix indexes, `k`, pragmas
— is second-order by a wide margin. Two of the seven turn out to be non-issues,
one turns out to be doing nothing at all, and one is a 5.5× win for three
milliseconds of work.

Ranked work items are in §7. The single highest-leverage change is **§7.1**.

---

## 1. What we do today

Three separate FTS5 indexes, two separate query builders, no shared
configuration between them.

### 1.1 The markdown index — `memory_chunks`

Created per memory directory (per agent, per crew, per workspace) as
`index.sqlite` inside that directory:

```
CREATE VIRTUAL TABLE IF NOT EXISTS memory_chunks USING fts5(
    file,
    content,
    tokenize='unicode61'
);
```

`internal/memory/engine.go:100-104`. Constructed by `New`
(`engine.go:66`), which every tier goes through — the workspace tier included
(`internal/memory/workspace.go:51`).

Connection string, `engine.go:80`:

```
?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)
&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)
```

No `cache_size`, no `mmap_size`, no `foreign_keys` — deliberately lighter than
the main DB (`internal/database/database.go:68-75`), which sets all three.

Population: `ReindexContext` walks every `.md`, chunks it with `ChunkMarkdown`
and rebuilds the whole table in one transaction (`internal/memory/index.go:26`,
chunk call at `:121`). `ReindexPath` (`index.go:166`) is the per-write fast
path — `DELETE FROM memory_chunks WHERE file = ?` then re-insert that one
file's chunks, short-circuited by a SHA-256 content hash (`index.go:205-209`).

Queried by `Engine.Search` (`internal/memory/search.go:28`):

```sql
SELECT file, content, rank
  FROM memory_chunks
 WHERE memory_chunks MATCH ?
 ORDER BY rank
 LIMIT ?
```

`search.go:46-52`. Default `bm25()` via the `rank` shorthand — no column
weights.

### 1.2 The journal index — `journal_entries_fts`

```
CREATE VIRTUAL TABLE IF NOT EXISTS journal_entries_fts USING fts5(
    summary, payload,
    content='journal_entries',
    content_rowid='rowid',
    tokenize='porter ascii'
);
```

`internal/database/migrate.go:688-693` (migration v55). External-content table,
kept in sync by three triggers (`:694-709`). The migration comment explains the
choice: *"stemming reduces 'deploys / deployed / deploying' to one bucket, and
ascii so we don't pay the overhead of unicode case folding for an
operator-facing tool"* (`migrate.go:665-667`).

### 1.3 The conversation index — `conversation_messages_fts`

Same shape, same `tokenize='porter ascii'`
(`internal/database/migrate_consts_v111_conversation_search.go:50-54`), copied
verbatim from v55 and documented as such at `:29`. Not currently reachable from
an agent — `conversation.search` was removed from the prompt this session
(handoff §1).

### 1.4 The two query builders

**Markdown side** — `sanitizeFTSQuery` (`internal/memory/search.go:80`). Strips
`{ } : ^ ~ ( ) +` (`search.go:88`), then, for a query containing none of them
and no explicit operator, **wraps each word in quotes and joins with a space**
(`search.go:105-111`). A space between two FTS5 terms is an implicit AND.

**Journal side** — `escapeFTSQuery` (`internal/episodic/hybrid.go:158`). Walks
the lowercased string accepting only `a-z` and `0-9` (`hybrid.go:166`);
everything else is a separator. Runs of length ≤ 1 are discarded; the rest
become **prefix terms joined with OR** (`hybrid.go:169-181`).

The two builders share nothing and behave oppositely: one is
all-terms-required-exact, the other is any-term-prefix.

### 1.5 The two fusions

`internal/episodic/hybrid.go:195` — `rrfFuse(dense, sparse, topK)`, `k = 60`
(`:196`). Both lanes rank rows from the same `journal_entries` table, so a row
can appear in both and accumulate `1/(k+rank)` twice. **This is textbook RRF and
it is correct.**

`internal/memory/hybrid.go:97` — `HybridSearch`, `rrfK = 60` (`:74`). Fuses the
markdown FTS list with the episodic list. These two lists are drawn from
**disjoint corpora** — markdown chunks keyed `file:line`, journal rows keyed
`entry_id` — so no item ever appears in both. §3.4 shows what that does.

### 1.6 What the model is told

Tool description, `internal/memory/tools.go:155-162`:

> "Keyword search across memory tiers. […] Search query. **Plain text; the
> engine handles tokenisation.**"

`[MEMORY INSTRUCTIONS]`, `internal/orchestrator/memory.go:848-854`, now names
both tools explicitly and tells the agent to search on a gap.

`[MEMORY GAP]`, `internal/orchestrator/memory.go:359-367`, ends with:

> "Before you start: memory.search the project or task you are picking up."

---

## 2. How this was measured

`scripts/memory-retrieval-bench` (build tag `memorybench`, excluded from every
normal build, test run and the shipped binary — verified: `go build ./...` and
`go test ./internal/memory ./internal/episodic` are unaffected). It drives the
**production** `memory.Engine`, `ChunkMarkdown`, `sanitizeFTSQuery` and
`escapeFTSQuery` rather than copies — the two unexported query builders are
reached through `bench_export.go` files carrying the same build tag, so a
change to either function changes the measurement.

Three corpora:

| corpus | what it is | why |
|---|---|---|
| 7 lines, 5 Czech | hand-written memory sentences | tokenizer probes with a known answer |
| 406 chunks | 2 realistic files + 400 vocabulary-overlapping distractors | ranking under competition |
| **7 681 chunks** | **this repository's real `docs/` tree** | real prose, real vocabulary |

**What none of this is.** It is not an evaluation set. Ten queries with a
hand-written answer key is a smoke test; a one-query difference is noise. It
scores retrieval only — nothing here measures whether the model then *uses* the
chunk correctly. §8 says what a real eval would have to be.

---

## 3. The seven unknowns, answered

### 3.1 FTS5 tokenizer — `unicode61` vs `trigram` vs `porter`

**Today:** `unicode61` for markdown (`engine.go:103`), `porter ascii` for
journal and conversation (`migrate.go:692`, `v111:54`).

**The Czech worry is mostly unfounded, and the measurement says so.** [M]

`unicode61`'s default is `remove_diacritics 1`, and it folds **15 of 15** Czech
diacritics — every one of `á é í ó ú ý č ď ě ň ř š ť ž ů`. A note written
`rozhodnutí` is found by a query typed `rozhodnuti`. `remove_diacritics 2`
folds the same 15; `remove_diacritics 0` folds none:

| letter set | rd=1 (today) | rd=0 | rd=2 |
|---|---|---|---|
| all 15 Czech diacritics | **15/15** | 0/15 | 15/15 |

So the markdown side needs no tokenizer change for diacritics. Setting
`remove_diacritics 2` explicitly is worth doing anyway — it is a no-op today
and immunises against SQLite ever changing the default — but it is cosmetic.

**The `ascii` tokenizer on the journal side is a different story.** [M] Probing
it directly (rather than trusting the docs) shows it treats bytes ≥ 0x80 as
*token characters* but does not case-fold or diacritic-fold them:

```
Tokens produced by ascii:      keeperu  kontext  o  rozhodnutí  žádný
Tokens produced by unicode61:  keeperu  kontext  o  rozhodnuti  zadny
```

| MATCH | `ascii` (journal today) | `unicode61` |
|---|---|---|
| `"rozhodnutí"` | 1 | 1 |
| `"rozhodnuti"` | **0** | 1 |
| `"zadny"` | **0** | 1 |

A Czech word typed without its diacritics — which is how people type when they
are in a hurry, and how any ASCII-only pipeline will have mangled it — cannot
find the journal entry that contains it. The migration comment's stated reason
for `ascii` (*"so we don't pay the overhead of unicode case folding"*) buys
nothing measurable: `porter unicode61` and `porter ascii` index this corpus to
within 1% of the same size and the same term count (§3.3 table).

**What the alternatives buy, on the same task** [M] (406-chunk corpus,
recall@10 out of 10 queries; `file UNINDEXED` held constant so the comparison
isolates the tokenizer):

| query builder | unicode61 (today) | porter unicode61 | trigram ci+rd |
|---|---|---|---|
| AND (today) | 2/10 | 2/10 | 2/10 |
| OR | 5/10 | 5/10 | 5/10 |
| OR + stopwords | 4/10 | **5/10** | **5/10** |
| OR + stopwords + prefix | 5/10 | **6/10** | 5/10 |
| phrase OR terms | 4/10 | **5/10** | **5/10** |

The tokenizer moves the score by **at most one query**. The query builder moves
it by **three to four**. That ratio is the whole finding.

On the 16-query Czech probe, `porter unicode61` scored best of the seven
configurations tried (11/16 exact-expression, 14/16 prefix-expression, vs 10/16
and 13/16 for `unicode61`) — porter's English stemming is additive on the
English half of a bilingual corpus and, measurably, does not damage the Czech
half.

**Trigram is the one to reject.** [M] It matches substrings, which is
genuinely attractive for a heavily inflected language — it was the only
configuration to find `slot` inside `slotů` and `žurnál` inside `žurnálu`. It
costs **7.03× the raw text in index bytes** against unicode61's 1.53×:

| tokenizer | index bytes | × text | distinct terms |
|---|---|---|---|
| unicode61 (today) | 2.3 MB | 1.53× | 4 051 |
| porter unicode61 | 2.3 MB | 1.52× | 4 050 |
| trigram | **10.5 MB** | **7.03×** | 1 799 |

The per-agent memory budget is 10 MB total (`engine.go:30`). A 7× index does
not fit beside the content it indexes. Trigram also cannot use a prefix index
and cannot be combined with `porter`.

**Recommendation.**

1. Change `journal_entries_fts` and `conversation_messages_fts` from
   `porter ascii` to **`porter unicode61 remove_diacritics 2`**. Requires a
   migration with an FTS rebuild (`INSERT INTO t(t) VALUES('rebuild')`, the
   form migration v167 already uses at
   `migrate_consts_v167_journal_append_only_fks.go:460`).
2. Change `memory_chunks` from `unicode61` to
   **`porter unicode61 remove_diacritics 2`**. Requires a full `Reindex`,
   which the sidecar already performs at boot (`sidecar/server.go:343`) — so
   this one is nearly free, but the `CREATE VIRTUAL TABLE IF NOT EXISTS` at
   `engine.go:100` will not alter an existing table. Needs an explicit
   drop-and-recreate keyed on a `memory_meta` schema-version row.
3. Do **not** adopt trigram.

**Evidence class:** measured, on three corpora. The one inference [I] is that
porter's behaviour on Czech is *neutral rather than harmful* — the 16-query
probe found no case where porter lost a Czech match that `unicode61` won, but
16 hand-written queries cannot prove absence.

### 3.2 BM25 column weighting, and whether `rank` should be customised

**Today:** `ORDER BY rank` with no weights (`search.go:47,50`), over
`fts5(file, content)` where **`file` is a normal indexed column**
(`engine.go:100-102`).

**The column weights are not the problem; the indexed `file` column is.** [M]

Indexing the path means every path fragment is a full-strength search term:

| query | `file` indexed (today) | `file UNINDEXED` |
|---|---|---|
| `"daily"` | 3 hits | 0 |
| `"md"` | **6 hits (the entire corpus)** | 0 |
| `"agent"` | 3 | 1 |
| `"2026"` | 3 | 1 |

Every chunk of every file whose name ends `.md` matches the term `md`. Every
chunk under `daily/` matches `daily`. Under today's implicit-AND builder this
mostly hides — an extra required term makes the query *stricter*, so it fails
silently rather than wrongly. **Under the OR builder recommended in §3.7 it
stops hiding**: one path word would pull an entire tier into the candidate set.

So this is not an independent work item. It is a **prerequisite** of the
highest-leverage change, and shipping the OR rewrite without it would trade a
zero-recall bug for a zero-precision one.

Adjusting `bm25()` weights instead of un-indexing does not work: with weights
`(0.0, 1.0)`, `(1.0, 1.0)` and `(10.0, 1.0)` the returned scores for a
content-only term were **identical to three decimal places** [M]. Weights
change ranking among matched rows; they do not change *what matches*. The
`file` column has to leave the match set, not be down-weighted.

**Recommendation.** `fts5(file UNINDEXED, content, …)`. Keep `ORDER BY rank`
with default weights — with one indexed column there is nothing to weight, and
there is no evidence for tuning `bm25`'s `k1`/`b` (FTS5 does not expose them
anyway).

If path-matching is wanted deliberately (searching for "the daily note about
X"), the right shape is a **separate, explicitly-scoped filter argument** on
the tool, not a silent term in the free-text match — `memory.search` already
has a `tier` argument (`tools.go:164-168`) that is the correct home for it.

**Evidence class:** measured.

### 3.3 Prefix indexes — worth it at our corpus size?

**Today:** none.

**No.** [M]

| tokenizer | index bytes | × text |
|---|---|---|
| unicode61 | 2.3 MB | 1.53× |
| unicode61 + `prefix='2 3'` | 5.1 MB | **3.40×** |
| unicode61 + `prefix='2 3 4'` | 6.0 MB | **4.01×** |

A `prefix='2 3'` index **doubles the index** for a query-time saving that does
not survive measurement. Timed at 20 000 chunks — already far larger than the
10 MB per-agent cap allows — on three-character prefixes, i.e. inside the
index's coverage:

| variant | `"dep"*` | `"rozh"*` | `"keep"*` |
|---|---|---|---|
| no prefix index | 2.20 ms | 2.24 ms | 2.06 ms |
| `prefix='2 3'` | 1.03 ms | 1.94 ms | 2.07 ms |

One of three prefixes got faster; two were unchanged within run-to-run
variance. At a realistic corpus size (§3.5 measures a real agent-scale index at
7 681 chunks) the absolute latency is well under a millisecond either way, and
the whole search sits behind a container exec and an LLM round trip.

**Recommendation.** Do not add a prefix index. Revisit only if a single memory
directory is ever measured past ~50 000 chunks, which the 10 MB cap makes
impossible today.

**Evidence class:** measured. The timing numbers are noisy (300 ms of repeats
per cell on a shared laptop); the *size* numbers are exact and are what carries
the decision.

### 3.4 RRF `k = 60` — inherited or chosen?

**Inherited, correctly attributed, and in one of the two call sites it is doing
nothing at all.**

**Provenance, from the primary source.** Cormack, Clarke & Büttcher, *Reciprocal
Rank Fusion outperforms Condorcet and individual Rank Learning Methods*,
SIGIR '09. Read directly, not from a summary. The paper says:

> "where *k* = 60 was fixed during a pilot investigation and not altered during
> subsequent validation."

and

> "The results of the first, shown in table 1, indicated that *k* = 60 was
> **near-optimal, but that the choice was not critical**."

Its Table 1 is the sensitivity curve, on TREC topics 351–400 fusing 30 Wumpus
Search configurations:

| k | 0 | 10 | 30 | 60 | 80 | 100 | 500 |
|---|---|---|---|---|---|---|---|
| MAP | .2072 | .2123 | .2139 | .2145 | **.2147** | .2142 | .2098 |

Everything from k=10 to k=100 sits inside 1% of everything else. So the
comment at `internal/memory/hybrid.go:71-74` — *"60 is the literature standard
… Tweaking this is rarely worth it"* — is **accurate**, and the corresponding
comment at `internal/episodic/hybrid.go:186-188` is accurate too. The handoff's
question ("inherited or chosen?") answers: inherited, from the right paper,
with the right justification. **No change needed to the value.**

**But the paper's formula assumes something our markdown fusion violates.** The
paper defines RRF over *"a set D of documents to be ranked and a set of
rankings R, each a permutation on 1..|D|"*, scoring
`RRFscore(d) = Σ_{r∈R} 1/(k + r(d))` — a **sum over the rankings a document
appears in**. The whole mechanism is that a document ranked mid-list by two
systems beats a document ranked top by one and absent from the other.

`internal/memory/hybrid.go` fuses markdown chunks against journal rows. The
corpora are disjoint; **no item can ever appear in both lists**; every score is
a single `1/(k+rank)` term. Measured across six values of k [M]:

| k | resulting order |
|---|---|
| 0 | fts#1 → epi#1 → fts#2 → epi#2 → fts#3 → epi#3 → … |
| 1 | *identical* |
| 10 | *identical* |
| 60 | *identical* |
| 600 | *identical* |
| 60 000 | *identical* |

This is analytic, not statistical: with disjoint lists the score is a pure
function of rank, identical for both lists, so the fusion degenerates to a
**fixed round-robin interleave** and `k` cannot change it. A rank-1 FTS chunk
that happens to be irrelevant always outranks a rank-2 episodic entry that is
exactly right — and there is no signal anywhere in the function that could
prefer otherwise, because ranks are the only input and both lists supply
rank 1.

**And in production it is worse than round-robin.** The in-container tool path
calls `HybridSearch(ctx, src.engine, nil, nil, …)` per source
(`internal/memory/tools.go:866`) — so both fused lists are FTS lists, one per
engine (agent, crew), fused by the same rank-only score. Because the FTS list
is appended first and the sort is stable (`hybrid.go:173`, `tools.go:925`),
**crew memory can never outrank agent memory at equal rank.** That is a
policy — arguably a defensible one — but it is nowhere stated, and it is
emergent from list-append order rather than chosen.

**Compounding defect: `ftsKey` collides.** [M] `ftsKey` builds its identity as
`h.File + ":" + itoa(h.LineStart)` (`hybrid.go:189`). `Engine.Search` never
assigns `LineStart` — it scans only `file`, `content` and `rank`
(`search.go:62`) — so **`LineStart` is 0 on every result**:

```
| hit | file                  | LineStart | LineEnd |
|   0 | daily/2026-07-26.md   |         0 |       0 |
|   1 | AGENT.md              |         0 |       0 |
|   2 | AGENT.md              |         0 |       0 |
|   3 | daily/2026-07-26.md   |         0 |       0 |

key `daily/2026-07-26.md:0` is shared by 2 distinct chunks
key `AGENT.md:0`            is shared by 2 distinct chunks
```

The rank map is written in a loop (`hybrid.go:122-124`), so every chunk of a
file overwrites the previous one's entry and they all end up scored at the
**last (worst) rank that file achieved**. `ChunkMarkdown` computes correct line
numbers (`chunk.go:11-14`) and the indexer throws them away — they are never
stored in `memory_chunks` at all (`index.go:90`, `:226`).

**Recommendation.**

1. Keep `k = 60`. It is right, and it is cited correctly.
2. **Stop calling the markdown/episodic merge "RRF."** With disjoint corpora,
   either (a) accept round-robin and name it that, so nobody tunes a constant
   that cannot do anything, or (b) make the lists comparable — a shared
   normalised score, or a deliberate source prior (e.g. "an exact-phrase FTS hit
   outranks any episodic hit"), which is a product decision, not a fusion
   parameter. Prefer (a) first: it is honest and free.
3. **Fix `ftsKey` regardless.** Store `line_start` in `memory_chunks`, return it
   from `Search`, and the key stops colliding. This also gives the tool the
   `file:line` locator it currently reconstructs with a substring re-scan
   (`lineOfSnippet`, `tools.go:914`).
4. Make the agent-before-crew tie-break explicit rather than emergent.
5. `internal/episodic/hybrid.go:219` uses `sort.Slice`, which is **not
   stable**, over a slice built by ranging a Go map (`:216-218`). Ties are
   common (any entry in exactly one lane at the same rank), so identical inputs
   can produce different orderings across runs. Use `sort.SliceStable` with a
   deterministic tie-break on `EntryID`. [I] — reasoned from the code and Go's
   documented map-iteration randomisation; not reproduced under test.

**Evidence class:** provenance and sensitivity are from the primary source
(read, not summarised). The degeneracy is analytic and demonstrated. The
`ftsKey` collision is measured. Item 5 is inferred.

### 3.5 Chunking strategy for markdown

**Today:** split on `## ` headings only (`chunk.go:34`), then break sections
over 500 chars (`chunk.go:7`) at blank-line boundaries (`chunk.go:79`).

**The handoff guessed this was "plausibly the dominant factor." It is not the
dominant factor — the query builder is — but it has three real defects.** [M]

Measured on the real 7 681-chunk `docs/` corpus:

| p10 | p50 | p90 | p99 | max | >500 | >2000 |
|---|---|---|---|---|---|---|
| 128 | 381 | 624 | 1727 | **5 685** | 1 280 (17%) | 51 (1%) |

**Defect 1 — 500 is a floor, not a cap.** `splitLargeChunk` only breaks at
`\n\n` (`chunk.go:70,79`). Any run of text without a blank line stays whole
however long it gets. Measured on synthetic inputs:

| input | chunks | max chunk |
|---|---|---|
| one 4 KB paragraph, no blank lines | 2 | **3 999** |
| 60-line bullet list, no blank lines | 2 | **2 039** |

A bullet list with no blank lines is exactly what an agent writes into a daily
note. Since BM25 normalises by document length, an oversized chunk is
systematically *under*-ranked for any single term — the retrieval failure is
silent and biased against the longest, most content-dense notes.

**Defect 2 — only `## ` is a boundary.** A file using `#` and `###` (very
common — `agent-memory-on-wake.md` itself uses `###` for its subsections) gets
no structural split at all; measured, an h1+h3 document produced 4 chunks with
2 of them oversized.

**Defect 3 — the heading is dropped from every chunk but the first.** [M]

```
chunk 0 (500 B) starts: "## Retence žurnálu"
chunk 1 (478 B) starts: "Paragraph about retention policy and archival."
```

Chunk 1 is indexed without the words that say what it is about. A query for
`retence` cannot find the second half of the section on retention. This is the
single cheapest chunking fix available and it is well-established practice —
prepending the heading path to each chunk of a section costs a few bytes per
chunk and is the standard "contextual chunk header" pattern.

**Recommendation, in order of value per line of code:**

1. **Prepend the heading breadcrumb** (`# Title > ## Section`) to every chunk of
   a section, not just the first.
2. **Make 500 a real cap.** Fall back to sentence, then line, then hard
   character split when no blank line is available.
3. **Treat any ATX heading as a boundary** (`#`, `##`, `###`), not just `## `.
4. **Store `line_start` / `line_end`** — already computed, currently discarded
   (§3.4).
5. **Do not add chunk overlap yet.** It is the obvious next lever, but it
   multiplies index size and its value cannot be judged without §8's eval. [I]

**Evidence class:** distributions and the heading-loss behaviour are measured on
real markdown. The claim that oversized chunks are *under*-ranked is [I] — it
follows from BM25's length normalisation, which is definitional, but this
document did not measure the rank penalty directly.

### 3.6 SQLite pragmas, and the cost of index maintenance on every write

**Today:** `journal_mode(wal)`, `busy_timeout(5000)`, `synchronous(NORMAL)`,
`temp_store(MEMORY)` (`engine.go:80`). No `optimize` anywhere.

**The pragmas are already right, and the measurement says leave them alone.**
[M] Per write-transaction (DELETE + 3 INSERTs):

| pragma set | per write-tx |
|---|---|
| **TODAY** (wal, synchronous NORMAL) | **86 µs** |
| journal_mode delete, synchronous FULL | 311 µs (3.6× worse) |
| wal, synchronous OFF | 102 µs (no gain, loses durability) |
| wal, synchronous NORMAL, mmap 256 MB | 106 µs (no gain) |

`synchronous(OFF)` and `mmap_size` both measured *slower* than today, within
noise — there is no headroom here and no reason to touch it. Page size was not
varied: SQLite's 4096-byte default matches the page size on every platform we
target and changing it requires a vacuum. [I]

**The incremental reindex works as designed.** [M] `ReindexPath` cost against
corpus size, confirming O(changed file) not O(corpus):

| corpus files | ReindexPath p50 | full Reindex |
|---|---|---|
| 10 | 89 µs | ~0 ms |
| 100 | 148 µs | 2 ms |
| 1 000 | 294 µs | 24 ms |
| 5 000 | 1.37 ms | 132 ms |

The 5 000-file row shows mild growth — the `DELETE … WHERE file = ?` still
touches a larger index — but a memory write costs on the order of a
millisecond against a corpus far larger than the 10 MB cap permits. **This is
not a problem and does not need fixing.**

**The real finding here is what is missing: nothing ever runs `optimize`.** A
tree-wide search finds `INSERT INTO <t>(<t>) VALUES('optimize')` in exactly
zero production files; the only maintenance command anywhere is a `'rebuild'`
inside migration v167
(`migrate_consts_v167_journal_append_only_fks.go:460`). FTS5 never rewrites in
place — a DELETE appends tombstones and each INSERT appends a segment — so an
index written by `ReindexPath` fragments monotonically.

Measured: one daily note rewritten 3 000 times, which is exactly the shape
`memory.write` produces. [M]

| variant | index size | `%_data` rows | query p50 | optimize cost |
|---|---|---|---|---|
| **never optimize (TODAY)** | 48.0 KB | 17 | **197 µs** | — |
| optimize every 250 writes | 48.0 KB | 3 | **36 µs** | **3 ms total** |

**5.5× faster queries for three milliseconds of work across three thousand
writes.** Same rows, same content, same file size — the entire difference is
accumulated index debt.

**Recommendation.**

1. Run `INSERT INTO memory_chunks(memory_chunks) VALUES('optimize')` on a
   counter — every ~200 `ReindexPath` calls — and once at the end of the
   sidecar's boot `Reindex`. Cheap, bounded, no schema change.
2. Do the same for `journal_entries_fts`, driven from the existing daily
   consolidation job (`consolidate/compact.go` already runs at 03:00 UTC), where
   the corpus is far larger and every journal row insert appends a segment.
3. Leave every other pragma exactly as it is.

**Evidence class:** measured. The absolute latencies are small; the **ratio** is
the durable result and it will grow with corpus size and uptime, neither of
which this bench simulated to production scale. [I]

### 3.7 Query rewriting — the actual problem

**Today:** nothing. Each word is quoted and space-joined, i.e. ANDed
(`search.go:105-111`).

**This is the finding.** [M] Ten realistic questions against the production
`Engine`:

| query | words | hits |
|---|---|---|
| `aux slots` | 2 | **0** |
| `keeper aux sloty` | 3 | **0** |
| `co-author trailer` | 2 | 1 |
| `what did we decide about journal retention` | 7 | **0** |
| `journal retention` | 2 | **0** |
| `jak dlouho se drží žurnál` | 5 | **0** |
| `why does the backend return 502 after deploy` | 8 | **0** |
| `deploy 502` | 2 | **0** |
| `trust zone` | 2 | 1 |
| `how do I deploy to a dev slot` | 8 | **0** |

**2 of 10 found anything. 8 returned zero rows.** The expression built for the
fourth is `"what" "did" "we" "decide" "about" "journal" "retention"` — seven
terms, all required, inside one ~500-char chunk.

The tool description tells the model *"Plain text; the engine handles
tokenisation"* (`tools.go:162`). It does not. A model that reads that sentence
and asks a natural question gets an empty array and no indication why.

**On the real 7 681-chunk `docs/` corpus, where chunks are larger and prose is
denser, the same three builders score:** [M]

| builder | right file in top-3 |
|---|---|
| AND (today) | 5/10 |
| OR | 7/10 |
| **OR + stopword removal** | **8/10** |

and on the 406-chunk adversarial corpus, AND 2/10 → OR 5/10.

Stopword removal matters because a bare OR inherits the opposite failure:
`what did we decide about journal retention` OR-expands to include `what`,
`did`, `we`, `about`, which match nearly everything and drown the two terms
that carried the meaning. On the real corpus stopword removal recovered one
query that plain OR lost; on the synthetic one it was neutral-to-negative,
which is exactly the ambiguity a 10-query smoke test cannot resolve.

**What Hermes does, and what we should take.** Hermes ships
`plugins/memory/query_rewrite.py`: an aux-model pass turning the user message
into a retrieval question, with injection guards. Adopting the *aux-model* part
is premature here — it adds a model call and a Keeper dependency to a path that
currently has neither, and it cannot help the 8 zero-result queries above,
because those fail on Boolean structure rather than on phrasing. **Take the
idea of a rewrite stage; implement stage one deterministically.**

**Recommendation — the single highest-leverage change:**

Replace the space-join in `sanitizeFTSQuery` with:

1. `file UNINDEXED` first (§3.2) — otherwise OR pulls whole tiers in.
2. Drop stopwords from a short, flat, bilingual list. Keep it short: the
   failure mode of a too-short list is a little noise; of a too-long one, a
   lost answer.
3. Emit `"<phrase>" OR "t1" OR "t2" OR …` — the full quoted phrase first so an
   exact match ranks top, then the individual terms so a partial match still
   returns something.
4. Keep the existing operator passthrough for a caller who writes real FTS5
   syntax, and keep the `{ } : ^ ~ ( ) +` stripping unchanged — it is the
   column-filter injection guard and OR does not weaken it.
5. Fix the tool description to describe what the engine actually does.

Everything above degrades correctly with no embedder: it is pure FTS5 and does
not touch the Keeper/Ollama path (`server/server.go:500`).

**Separately, fix `escapeFTSQuery` on the journal side.** [M] Its ASCII-only
scan shreds Czech at every diacritic:

| query | expression built |
|---|---|
| `rozhodnutí o retenci žurnálu` | `rozhodnut* OR retenci* OR urn* OR lu*` |
| `jak dlouho se drží žurnál` | `jak* OR dlouho* OR se* OR dr* OR urn*` |
| `Které sloty se projevily až po restartu?` | `kter* OR sloty* OR se* OR projevily* OR po* OR restartu*` |

Two- and three-character fragments are not search terms. Measured against the
real 7 681-chunk corpus (10 927 distinct index terms):

| fragment | distinct terms it expands to | chunks matched |
|---|---|---|
| `se*` | 156 | **3 724 (48.5%)** |
| `po*` | 76 | 1 379 (18.0%) |
| `dr*` | 38 | 585 (7.6%) |
| `keep*` | 5 | 585 (7.6%) |

`se*` selects **half the corpus** and is OR-ed in alongside the terms that
mattered. The fix is to make the scan Unicode-aware (`unicode.IsLetter` /
`unicode.IsDigit`) and to require ≥ 3 characters before emitting a prefix term.

**Evidence class:** measured, on three corpora including a real one. The
recommended composite builder's exact stopword list is [I] — the list used in
the bench is hand-written and unvalidated, and is the part most likely to need
revision once §8's eval exists.

---

## 4. The one thing that is genuinely fine

Storage and durability. `agent-memory-on-wake.md` §0 said *"Storage is solid"*
and this pass agrees, with numbers: the index rebuilds from source markdown at
sidecar boot, per-write maintenance is 86 µs, the incremental path is O(file),
the walker is symlink- and FIFO-hardened (`index.go:291`, with a documented
residual on intermediate components), and the DELETE+INSERT runs inside one
transaction so a concurrent `Search` never sees a file with zero chunks
(`index.go:219-221`).

**No storage work is recommended.** Every item in §7 is a query-side or
index-configuration change.

---

## 5. The open question — eager snapshot or lazy search?

`agent-memory-on-wake.md` §7.1 asks which is the intended primary path, noting
that "today it is eager with a fixed path list, and the prompt actively
discourages the lazy one."

**Half of that premise is now stale.** The prompt no longer discourages the
lazy path — this session's merged work replaced it. `[MEMORY INSTRUCTIONS]`
now names both tools and instructs on the gap
(`orchestrator/memory.go:847-854`), and `[MEMORY GAP]` ends by telling the
agent to search (`:362-364`). The stale "lands in PR-A (F1)" sentence is gone.

**The answer: eager is the floor, lazy is the ceiling, and the architecture has
already committed to both.** Neither can be dropped:

- **Lazy alone cannot be primary, structurally.** `memorySinkReady`
  (`orchestrator/mcp_memory_inject.go:75-77`) probes the sidecar and
  `injectMemoryMCP` drops the **entire** `crewship-memory` server when it is
  unreachable (`:92-95`). Assignment- and mission-dispatched runs use
  `SkipSidecar`. A crew container TTL-stopped during an idle week and restarted
  for an assignment gets **no `memory.*` tools at all** — precisely the
  one-week-gap scenario the requirement names. If lazy were primary, the wake
  that needs memory most would have none.
- **Eager alone cannot be sufficient, by budget.** The snapshot is a bounded
  character window over a closed path list. Any design where the snapshot is
  the whole answer is a design where memory stops working once it exceeds the
  budget — which is every agent, eventually.

So the question is not "which one" but **"what is each one's job."** The
commitment worth making:

> **Eager guarantees a floor: identity, standing constraints, pins, and the
> last active day — always present, never dependent on a tool call. Lazy
> provides depth on demand, and the eager block's job includes telling the
> agent when depth is needed and that it is reachable.**

That is very close to what the code already does. Three things follow from
committing to it:

1. **The `[MEMORY GAP]` block's instruction must be true.** Today it says
   "memory.search the project or task you are picking up" and, for most
   phrasings, that returns zero rows (§3.7). **Fixing retrieval is what makes
   the eager/lazy split coherent** — it is not a separate concern from this
   question, it is the whole of it.
2. **The cold-container hole becomes a correctness bug, not a degradation.**
   If lazy is load-bearing for depth, silently withholding it is not
   "degrading gracefully." Either the tool must be available on a cold
   container, or the gap block must say that recall is unavailable this run —
   the standing constraint is *"when a control genuinely cannot be honoured,
   say so on every surface rather than blocking or lying"* (handoff §0.4), and
   today it does neither.
3. **The eager floor should stop being a fixed path list.** It should be
   "identity + pins + last active day", where "last active day" is resolved
   (already done this session) rather than enumerated.

**Recommendation:** adopt the sentence above as the design commitment, and
treat §7.1 of `agent-memory-on-wake.md` as answered. It does not require
choosing between that document's §6.1 and §6.2 — it makes both necessary, and
orders them.

---

## 6. Corrections to earlier documents

- **`agent-memory-on-wake.md` §3.2** — "the prompt tells the agent not to look"
  is **stale**. `memory.search` and `memory.read` are now named explicitly at
  `orchestrator/memory.go:848-850`, and the "lands in PR-A (F1)" sentence is
  gone. The remaining problem is that the named tool does not work well, which
  is a different problem.
- **`HANDOFF-2026-08-02.md` §5** — "what that means for Czech content" implies
  a diacritics problem in `memory_chunks`. Measured: there is none;
  `unicode61`'s default folds all 15 Czech diacritics. The Czech problem is
  real but lives in `escapeFTSQuery` (ASCII-only, §3.7) and `porter ascii`
  (journal, §3.1), not in the markdown tokenizer.
- **`HANDOFF-2026-08-02.md` §5** — "RRF k=60 — inherited or chosen?" is
  answered: inherited, cited correctly, and near-optimal per the source. The
  problem is not the value; it is that in `internal/memory/hybrid.go` the
  constant cannot affect the output at all (§3.4).
- **`HANDOFF-2026-08-02.md` §5** — "Chunking … plausibly the dominant factor in
  result quality." Measured: it is not. The query builder moves the score 3–4×
  further on the same corpus. Chunking is item 6, not item 1.
- **`internal/memory/hybrid.go:71-74`** — the comment claims k=60 is "tuned to
  dampen the dominance of rank-1 items so cross-list mid-rank items can still
  surface." That is true of RRF in general and **false of this call site**,
  where no item is ever cross-list.
- **`internal/memory/tools.go:162`** — "Plain text; the engine handles
  tokenisation" is not true of the current builder.
- **`internal/database/migrate.go:665-667`** — "ascii so we don't pay the
  overhead of unicode case folding" — measured, that overhead is under 1% of
  index size and term count.

---

## 7. Work items, ranked

Ranked by measured effect per unit of risk. **1 and 2 must ship together.**

### 7.1 Rewrite the query builder — phrase-OR-terms with stopword removal

`internal/memory/search.go:105-111`. **AND → 8/10 zero-result on realistic
queries; OR+stopwords → 8/10 correct in top-3 on the real corpus.** [M] The
largest measured effect in this document by a wide margin, and the change is
confined to one function with existing test coverage.

### 7.2 `file UNINDEXED` on `memory_chunks`

`internal/memory/engine.go:100-102`. **Prerequisite of 7.1** — without it, one
path word in an OR query pulls a whole tier (`"md"` matched 100% of the test
corpus). [M] Needs the same schema-version-keyed recreate as 7.7.

### 7.3 Periodic FTS5 `optimize`

`internal/memory/index.go` (counter in `ReindexPath`) and the daily
consolidation job for `journal_entries_fts`. **5.5× query latency for 3 ms per
3 000 writes.** [M] No schema change, no migration, trivially revertible.

### 7.4 Make `escapeFTSQuery` Unicode-aware, minimum 3-char prefixes

`internal/episodic/hybrid.go:158-182`. Czech input currently decomposes into
2-char prefixes; `se*` selects **48.5% of a real corpus**. [M]

### 7.5 Store and return chunk line numbers; fix `ftsKey`

`internal/memory/index.go:90,226`, `search.go:62`, `hybrid.go:189`. Removes the
measured key collision that gives every chunk of a file the file's worst rank,
and replaces the `lineOfSnippet` substring re-scan (`tools.go:914`) with a
stored value. [M]

### 7.6 Chunking: heading breadcrumbs, a real 500-char cap, all ATX headings

`internal/memory/chunk.go:34,70,79`. 17% of real chunks exceed the target and
the largest is 5 685 bytes; every non-first chunk of a section loses its
heading. [M]

### 7.7 Tokenizer migration to `porter unicode61 remove_diacritics 2`

`engine.go:103`, `migrate.go:692`, `v111:54`. Worth ≤ 1 query on a 10-query
probe [M], and needs a migration plus an FTS rebuild — so it is real work for a
small, measured gain. **Do it after 7.1–7.6, and only with §8's eval in place
to confirm the gain is real.**

### 7.8 Stop calling the markdown/episodic merge "RRF"

`internal/memory/hybrid.go:71-97`. Documentation and naming only; prevents
someone tuning a constant that provably cannot do anything (§3.4). Make the
agent-before-crew tie-break explicit while there.

### 7.9 `sort.SliceStable` + deterministic tie-break in `rrfFuse`

`internal/episodic/hybrid.go:219`. [I] Non-stable sort over map iteration
order; identical inputs can order differently across runs.

### 7.10 Decide the cold-container contract

Per §5 — either make `memory.*` available on a cold container, or say so in
the gap block. Product decision, not a retrieval change; listed here because §5
makes it load-bearing.

**Explicitly NOT recommended:** prefix indexes (§3.3), trigram (§3.1), pragma
changes (§3.6), `bm25()` weight tuning (§3.2), chunk overlap (§3.5), a
vector database (handoff §3.1, and nothing measured here disturbs that
verdict), an aux-model query rewriter *as stage one* (§3.7).

---

## 8. The eval that does not exist, and what it would take

Every number in this document comes from **10 hand-written queries with a
hand-written answer key**. That is enough to find a bug that returns zero rows
80% of the time. It is **not** enough to choose between two configurations that
differ by one query, which is exactly the margin separating the tokenizer
options in §3.1 and the stopword variants in §3.7.

`HANDOFF-2026-08-02.md` §5 sketches the write-side eval (labelling
`(conversation, candidate fact)`). The **read**-side eval is a different, and
smaller, thing:

- **Corpus:** a real `.memory/` tree from a long-lived dev1 agent, not a
  synthetic one. The distractor structure is what makes ranking hard and it is
  the part synthesis gets wrong.
- **Queries:** ~150, drawn from *actual* agent tool calls to `memory.search`.
  The sidecar already journals `memory.searched` with the query text
  (`internal/sidecar/memory.go:256`), so the phrasing distribution can be
  observed rather than imagined. Bilingual, in the observed proportion.
- **Answer key:** graded relevance per `(query, file)`, two annotators, with
  disagreement reported rather than resolved away.
- **Metric:** recall@10 and nDCG@10, plus **zero-result rate**, which is the
  failure this document found and which averaged metrics hide.
- **Arms:** at minimum today's builder, the §7.1 builder, and a no-retrieval
  control — the control separates "the store failed" from "the model would have
  answered anyway."

`HaluMem` (arXiv:2511.03506) remains the closest published thing to build on,
but it scores the *write* stage; the read stage above shares only its corpus
discipline. Two people, two weeks, and it makes 7.7 decidable. **Until it
exists, ship 7.1–7.6 — all of which are justified by effects far larger than
the noise floor — and leave 7.7 parked.**

---

## Appendix — running the measurements

```
go run -tags memorybench ./scripts/memory-retrieval-bench
```

Sections 1–6 are tokenizer, ascii, diacritic, index-size, prefix and RRF
probes. Sections 7–11 drive the production `memory.Engine`. Sections 12–14 are
the episodic query builder and the pipeline comparison. Section 15 runs against
this repository's own `docs/` tree. Section 16 measures `optimize` cadence.

The `memorybench` build tag excludes all of it — plus the two
`bench_export.go` shims in `internal/memory` and `internal/episodic` — from
every normal build, every test run and the shipped binary. Verified:
`go build ./...` and `go test ./internal/memory ./internal/episodic` are
unaffected. No `t.Skip` was added anywhere; the CI skip budget is untouched.

The bench imports the production functions rather than copying them, so if
`sanitizeFTSQuery` or `escapeFTSQuery` changes, the measurement changes with
it. That is deliberate: a benchmark that silently stops measuring production
code is worse than no benchmark.
