# Memory retrieval and storage — technical design reference

Status: public technical extract of the 2026-08-02 design. Internal corpus
measurements, comparisons and execution transcripts are retained in private
context. Source references and section numbers describe the dated design;
verify current implementation before changing it. Companion:
[agent context at wake](agent-memory-on-wake.md).

## 0. Verdict

The design separates durable Markdown storage, its rebuildable search index
and query construction. A durable index does not by itself establish useful
recall: tokenization, query rewriting, chunk identity and prompt delivery must
be validated independently. Public implementation choices are listed in §7.

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
an agent; tool availability must be checked on the actual wake path.

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
`entry_id` — so no item ever appears in both. Keep the two result identities distinct when combining rankings.

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

Internal corpora, query observations and raw benchmark results are retained
in private context. Use synthetic fixtures and the public memory retrieval
benchmark to reproduce the technical behaviour without private data.

## 3. The seven unknowns, answered

The design evaluates tokenizer compatibility, BM25 weighting, prefix indexes,
reciprocal-rank fusion, Markdown chunk boundaries, index maintenance and query
rewriting. The public implementation choices and remaining proposals are listed
in §7; raw experiments and comparisons belong to private context. Preserve
compatibility and verify changes against synthetic retrieval fixtures rather
than assuming a measured gain transfers to another corpus.

## 4. The one thing that is genuinely fine

The index is rebuildable from source Markdown. Incremental indexing should
replace a file’s chunks transactionally so concurrent searches do not observe
a partially replaced file. Preserve symlink/FIFO handling and validate storage
durability separately from retrieval quality; private benchmark numbers are
not product performance guarantees.

## 5. The open question — eager snapshot or lazy search?

Eager context supplies a bounded floor: identity, standing constraints, pins
and recent work. Lazy search supplies depth beyond that budget. Neither is
a substitute for the other. If a wake cannot reach memory tools, disclose the
limit rather than instructing the agent to call an unavailable tool. Verify
authority and delivery on each wake path, including cold containers.

## 6. Corrections to earlier documents

Internal review corrections are preserved with the original record.
The implementation notes in §7 state the public algorithm and compatibility
boundaries; they do not establish current benchmark results.

## 7. Implementation choices and compatibility

These retain the original numbered references; they describe design choices
and constraints rather than a fresh claim that every proposal is shipped.

### 7.0 What implementing 7.1–7.4 corrected in this document

Keep schema/index changes, query construction and result evaluation separate. Synthetic tests must cover both valid recall and misleading matches; no private-corpus score is a release guarantee.

### 7.1 Rewrite the query builder — phrase-OR-terms with stopword removal

Build a quoted phrase alternative plus useful terms with stopword handling. Preserve escaping and test natural-language queries with a synthetic corpus. See `internal/memory/search.go`.

### 7.2 `file UNINDEXED` on `memory_chunks`

Keep the path as returned metadata rather than searchable content where required by the index contract. Changing `file` indexing requires a schema-versioned rebuild, not a silent interpretation change.

### 7.3 Periodic FTS5 `optimize`

Compact FTS indexes at a bounded maintenance cadence and after full rebuilds. Measure latency and write cost on reproducible inputs before choosing a cadence.

### 7.4 Make `escapeFTSQuery` Unicode-aware, minimum 3-char prefixes

Handle Unicode terms explicitly. Distinguish minimum prefix length from minimum accepted term length: short valid terms may need exact matching rather than truncation or dropping.

### 7.5 Store and return chunk line numbers; fix `ftsKey`

Return stored chunk line numbers and use an identity that distinguishes chunks in one file. Avoid rank collisions and repeated substring scans to recover positions.

### 7.6 Chunking: heading breadcrumbs, a real 500-char cap, all ATX headings

Carry heading breadcrumbs and enforce chunk size consistently across Markdown heading forms. Cover long sections and non-first chunks with synthetic fixtures.

### 7.7 Tokenizer migration to `porter unicode61 remove_diacritics 2`

A tokenizer change requires compatibility review, a schema migration/rebuild and retrieval evaluation. Do not infer a quality improvement from tokenizer choice alone.

### 7.8 Stop calling the markdown/episodic merge "RRF"

Distinguish actual rank-based fusion from weighted merging of normalized scores. Use the algorithm’s real semantics in the public interface and documentation.

### 7.9 `sort.SliceStable` + deterministic tie-break in `rrfFuse`

Use stable ordering and deterministic tie-breaking for equal fused scores; repeated queries over identical inputs should not reorder results accidentally.

### 7.10 Decide the cold-container contract

Define and test the cold-container memory boundary: eager context, tool availability, explicit unavailable state and recovery without unauthorized fallback.

## 8. The eval that does not exist, and what it would take

Use a synthetic corpus, explicit relevance judgments and reproducible queries.
Measure retrieval quality and latency separately from whether a model uses
the retrieved context correctly. Private agent memory is not a public fixture.

## Appendix — reproducing retrieval checks

Use the [script catalog](../../scripts/README.md) and the public
`tools`/`scripts/memory-retrieval-bench` instructions with a synthetic corpus.
Internal measurement inputs and transcripts are not public dependencies.
