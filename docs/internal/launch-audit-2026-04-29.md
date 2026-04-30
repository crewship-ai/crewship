# Overnight Dependency & Security Audit

Generated: 2026-04-29
Branch: `overnight/docs-2026-04-29`
Base: `origin/main` (`fa21f03`)

Read-only audit. **No remediation applied** — awaiting human prioritization.

---

## 1. Severity summary

### npm advisories (`pnpm audit`)

| Severity | Count |
|----------|------:|
| critical |     0 |
| high     |     9 |
| moderate |     9 |
| low      |     0 |
| info     |     0 |
| **total**|  **18** |

Total dependencies scanned: 1257.

### Distinct vulnerable modules

| Severity | Module              | Findings | Lead title (truncated)                                                  |
|----------|---------------------|---------:|-------------------------------------------------------------------------|
| high     | `flatted`           |        2 | unbounded recursion DoS in `parse()` revive phase                       |
| high     | `lodash-es`         |        1 | code injection via `_.template`                                         |
| high     | `minimatch`         |        3 | ReDoS (repeated wildcards / matchOne / nested extglobs)                 |
| high     | `vite`              |        2 | `server.fs.deny` bypass via queries                                     |
| moderate | `@hono/node-server` |        1 | middleware bypass via repeated slashes in `serveStatic`                 |
| moderate | `ajv`               |        1 | ReDoS with `$data` option                                               |
| moderate | `brace-expansion`   |        1 | zero-step sequence → hang / memory exhaustion                           |
| moderate | `lodash-es`         |        2 | prototype pollution in `_.unset` / `_.omit` / array path bypass         |
| moderate | `postcss`           |        1 | XSS via unescaped `</style>` in CSS stringify output                    |
| moderate | `uuid`              |        1 | missing buffer bounds check in v3/v5/v6 when `buf` is provided          |
| moderate | `vite`              |        1 | path traversal in optimized deps `.map` handling                        |

### Top consumers bringing vulnerable transitives

- `eslint` → `ajv`, `brace-expansion`, `flatted`, `minimatch`
- `@streamdown/mermaid` → `lodash-es`, `uuid`
- `@vitejs/plugin-react` → `vite`
- `next` → `postcss`
- `prisma` → `@hono/node-server`

Most findings are **dev-tooling transitives** (eslint, vitejs). Runtime exposure is concentrated in `lodash-es` (via mermaid) and `postcss` (via Next.js).

---

## 2. Outdated dependencies

### npm (`pnpm outdated`) — 59 outdated of declared deps

| Bucket | Count |
|--------|------:|
| major  |     2 |
| minor  |     9 |
| patch  |    32 |

#### Major bumps (review before applying)

| Package | Current | Latest |
|---------|---------|--------|
| `@vitejs/plugin-react` | 5.1.4 | 6.0.1 |
| `eslint` | 9.39.2 | 10.2.1 |

#### Minor bumps (likely safe)

| Package | Current | Latest |
|---------|---------|--------|
| `@prisma/adapter-better-sqlite3` | 7.7.0 | 7.8.0 |
| `@prisma/client` | 7.7.0 | 7.8.0 |
| `prisma` | 7.7.0 | 7.8.0 |
| `dompurify` | 3.3.3 | 3.4.1 |
| `eslint-plugin-react-hooks` | 7.0.1 | 7.1.1 |
| `@tanstack/react-query` | 5.99.0 | 5.100.6 |
| `@typescript-eslint/eslint-plugin` | 8.58.2 | 8.59.1 |
| `@typescript-eslint/parser` | 8.58.2 | 8.59.1 |
| `lucide-react` | 1.8.0 | 1.14.0 |

Patch updates (32 total) include `next` 16.2.3→16.2.4, `typescript` 6.0.2→6.0.3, `vitest` 4.1.4→4.1.5, the entire `@tiptap/*` family 3.22.3→3.22.5, plus `tailwindcss`, `postcss`, `marked`, `nanoid`. Recommend a single batched patch sweep.

### Go modules (`go list -u -m all`) — 70+ modules with updates available

Notable Go bumps observed (full list in `go.mod`/`go list` output):

- `cel.dev/expr`, `filippo.io/edwards25519`, `github.com/alecthomas/chroma/v2 v2.20.0 → v2.24.0`
- `github.com/charmbracelet/bubbles 0.21.x-pre → v1.0.0` (major-effective)
- `github.com/charmbracelet/x/ansi 0.10.2 → 0.11.7`
- `github.com/containerd/typeurl/v2 2.2.0 → 2.2.3`
- `github.com/danieljoos/wincred 1.2.2 → 1.2.3`

`govulncheck` is **not installed** on this host — Go vulnerability scan was skipped. Install with `go install golang.org/x/vuln/cmd/govulncheck@latest` to add it to the audit.

---

## 3. Dead dependencies (`pnpm dlx depcheck`)

### Declared but not detected as imported (19 deps + 3 devDeps)

`@dnd-kit/core`, `@dnd-kit/sortable`, `@prisma/client`, `canvas-confetti`, `cron-parser`, `date-fns`, `embla-carousel-react`, `next-intl`, `next-themes`, `nuqs`, `react-arborist`, `react-error-boundary`, `react-intersection-observer`, `react-pdf`, `react-resizable-panels`, `react-textarea-autosize`, `react-virtuoso`, `tw-animate-css`, `vaul`

devDependencies: `@tailwindcss/postcss`, `postcss`, `tailwindcss`

> **Caveat:** depcheck has known false positives for: PostCSS plugins resolved via config strings, Prisma runtime client (loaded by generated code), and Tailwind/PostCSS pipelines. **Do not bulk-remove.** Verify each by searching for dynamic import strings, postcss config references, or generated-client paths before deleting.

### Used but not declared (missing in `package.json`)

- `highlight.js` — used in `components/features/issues/tiptap-editor-markdown.ts`
- `@tiptap/core` — used in `components/features/issues/tiptap-editor-slash.tsx`

Both currently work because they are transitive deps of declared `@tiptap/*` packages. Should be **promoted to direct deps** for stability.

---

## 4. Secret scan (`gitleaks` with project config)

11 candidates flagged. **All inspected — all are test fixtures or detection patterns**, not real secrets:

| Rule | File | Line | Notes |
|------|------|-----:|-------|
| generic-api-key | `internal/encryption/encryption_bench_test.go` | 8 | hex test key (`0123456789abcdef…`) |
| generic-api-key | `internal/encryption/layout_test.go` | 14 | hex test key |
| generic-api-key | `internal/encryption/layout_test.go` | 141 | hex test key (`fedcba…`) |
| aws-access-token | `internal/lookout/lookout_test.go` | 261 (×2) | `AKIAABCDEFGHIJKLMNOP` placeholder for redaction test |
| private-key | `internal/scrubber/scrubber.go` | 30 | regex pattern `BEGIN OPENSSH PRIVATE KEY` (detection rule, not a key) |
| generic-api-key | `internal/services/onboarding_test.go` | 85, 154, 190, 224, 301 | `ENCRYPTION_KEY` test fixture |

**Status:** all false positives from a security-tooling codebase that legitimately contains key-shaped patterns. Recommended action: extend `.gitleaksignore` to cover `internal/encryption/*_test.go`, `internal/lookout/lookout_test.go`, `internal/scrubber/scrubber.go`, `internal/services/onboarding_test.go` to silence the noise without weakening detection elsewhere.

---

## 5. TODO / FIXME / HACK / XXX debt

### Real comment-style markers (filtered to actual `// TODO`/`# TODO` style)

Total: **2** (across `internal/`, `app/`, `components/`, `lib/`)

| File:Line | Author / Date | Marker |
|-----------|---------------|--------|
| `internal/backup/keyring.go:32` | Pavel Srba, 2026-04-15 | `TODO(CRE-130 follow-up)`: mutex does NOT cover concurrent CLI |
| `components/features/orchestration/orchestration-layout.tsx:602` | Pavel Srba, 2026-04-10 | `TODO`: wire up status/label/priority filters |

Both are **<3 weeks old** and authored by the maintainer. No `FIXME`, no `HACK`, no `XXX`. The earlier raw scan (~150 hits) was dominated by the literal status enum value `"TODO"` used in mission/issue state machines — not actual deferred work.

**Verdict:** debt level is very low. Both real TODOs are actionable and tracked in tickets/context.

---

## 6. Bundle bloat (top 25 by `du -sk` of `node_modules/.pnpm`)

| Size (KB) | Package@version |
|----------:|-----------------|
| 172 444   | `next@16.2.3` (full deps closure) |
| 172 412   | `next@16.2.2` ← duplicate, see below |
| 118 896   | `@next/swc-darwin-arm64@16.2.3` |
| 118 784   | `@next/swc-darwin-arm64@16.2.2` ← duplicate |
| 85 320    | `react-icons@5.6.0` (×2: against react@19.2.5 and react@19.2.4) |
| 77 188    | `@prisma/client@7.7.0` |
| 77 156    | `@prisma/client@7.6.0` ← duplicate |
| 71 808    | `mermaid@11.13.0` |
| 66 884    | `mermaid@11.12.2` ← duplicate |
| 41 084    | `prisma@7.7.0` |
| 41 060    | `prisma@7.6.0` ← duplicate |
| 39 064    | `lucide-react@1.8.0` |
| 39 032    | `lucide-react@1.7.0` ← duplicate |
| 38 944    | `date-fns@4.1.0` |
| 38 500    | `@prisma/studio-core@0.27.3` (×2) |
| 33 480    | `effect@3.20.0` |
| 24 124    | `typescript@6.0.2` |
| 23 704    | `@electric-sql/pglite@0.4.1` |
| 23 700    | `@prisma/engines@7.7.0` (+ `@7.6.0`) |
| 23 388    | `typescript@5.9.3` ← duplicate of 6.0.2 |
| 16 700    | `happy-dom@20.9.0` (+ `@20.8.9`) |

### Bloat observations

1. **Multiple versions of the same package coexist** in the pnpm store — `next` (16.2.2 + 16.2.3), `mermaid`, `prisma`, `lucide-react`, `react-icons`, `typescript`, `happy-dom`. Each duplicate adds ~30–170 MB. Likely a stale lockfile or lingering peer-dep range artifact. Run `pnpm store prune` and rebuild lockfile.
2. **`react-icons` (~85 MB ×2)** is large because it ships every icon set. If only a few icons are used, switch to per-icon imports or `lucide-react` (already in tree).
3. **`mermaid` (~70 MB)** is heavy. If only used in docs/preview, lazy-load.
4. **Two TypeScript versions** (5.9.3 + 6.0.2) suggests a transitive constraint pinning the older one. Investigate via `pnpm why typescript`.

---

## 7. Recommended actions (prioritized)

### P0 — security, ship within 1–2 days

1. Bump `next` 16.2.3 → 16.2.4 (patch) to inherit fixed `postcss`. Validate via `pnpm build && go test ./... -count=1`.
2. Bump `lucide-react`, `dompurify`, `@tanstack/react-query` minor — addresses some transitive paths.
3. Plan the `eslint` 9 → 10 upgrade in a dedicated PR; it transitively resolves `minimatch`, `ajv`, `flatted`, `brace-expansion` advisories. Major bump → expect lint config breakage.
4. Plan `@vitejs/plugin-react` 5 → 6 to clear `vite` advisories. Not used in production runtime — check tooling only.

### P1 — hygiene, ship within 1 week

5. Add `highlight.js` and `@tiptap/core` as direct deps in `package.json` (currently relying on transitive resolution).
6. Bump remaining minor deps in one PR (Prisma 7.7→7.8 family, eslint-plugin-react-hooks, typescript-eslint).
7. Sweep all 32 patch updates in a single PR; re-run audit afterward.
8. Install `govulncheck` and add a `make audit-go` target.
9. Extend `.gitleaksignore` for the documented test-fixture lines (Section 4) so future scans are noise-free.

### P2 — cleanup, ship when convenient

10. **Verify** each "unused dep" from depcheck (Section 3) by hand. Delete only those genuinely unimported. Do not bulk-remove; tooling-config-resolved deps (postcss, tailwind) are false positives.
11. Run `pnpm store prune` + clean `pnpm-lock.yaml` to dedupe `next`, `mermaid`, `prisma`, `lucide-react`, `react-icons`, `typescript`, `happy-dom`.
12. Replace `react-icons` full-package import with per-set imports, or migrate to `lucide-react` (already in tree).
13. Lazy-load `mermaid` if it's only on doc/preview routes.
14. Address the 2 real `TODO` comments (`internal/backup/keyring.go:32`, `components/features/orchestration/orchestration-layout.tsx:602`) or convert them to tracked issues.

---

## 8. Audit method (reproducibility)

Commands run from `/Volumes/SSD 990 PRO/Development/crewship-overnight-docs` (worktree, `node_modules` symlinked to main worktree where needed):

```sh
pnpm audit --json                                                           # severity counts, advisory list
pnpm outdated --format json                                                 # current vs latest, bucketed major/minor/patch
pnpm dlx depcheck --json                                                    # unused / missing deps
go list -u -m all                                                           # outdated Go modules
gitleaks detect --no-git --config .gitleaks.toml --report-format json       # secret scan
grep -rn -E '(// |/\* |# )(TODO|FIXME|HACK|XXX)' internal/ app/ components/ lib/   # comment-style markers
du -sk node_modules/.pnpm/*/                                                # bundle bloat (.pnpm dir, since top-level entries are symlinks)
```

`govulncheck`: not installed. Install via `go install golang.org/x/vuln/cmd/govulncheck@latest`, then `govulncheck ./...`.
