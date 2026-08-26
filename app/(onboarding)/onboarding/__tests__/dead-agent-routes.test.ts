import { describe, it, expect } from "vitest"
import { readFileSync, readdirSync } from "node:fs"
import { join, relative, sep } from "node:path"

// =============================================================================
// No live surface may link into the /crews/agents family — the bare index
// /crews/agents included, not just /crews/agents/<id>/*.
//
// The selection-driven /crews redesign deleted that whole subtree — app/
// (dashboard)/crews/ is a single page.tsx now — but a long tail of call sites
// kept pointing at it. The worst of them was the onboarding wizard's completion
// redirect: a brand-new user finished setup and their very first click landed
// on a 404. That shipped, and nothing caught it, because every one of these is
// a plain string handed to router.push() or <Link href>, so a route that no
// longer exists still type-checks, still renders, and still looks like a
// working link right up until it is clicked.
//
// So the check has to be on the source. It reads the files rather than the
// rendered DOM because several of these sit inside a dropdown three clicks
// deep, and one only fires after a successful POST.
//
// The surviving agent surfaces are:
//   chat        -> /chat/<agentSlug>          (or /chat, the index)
//   settings    -> /crews?agent=<agentSlug>   (canvas Configuration tab)
//   workspace   -> /crews?agent=<agentSlug>   (canvas Files)
//   new agent   -> /crews?new=agent           (dialog on /crews, no route)
//   new crew    -> /crews?new=crew            (same)
//
// NOTE ON SLUGS: /crews selection is keyed on the *slug*
// (hooks/use-crews-selection.tsx reads ?agent= and the roster matches it
// against agent.slug). Passing an id there is worse than passing nothing —
// the stale-selection watcher clears it and the user lands on an empty
// canvas. Where only an id is in scope, link to plain /crews.
//
// This started as a hand-maintained list of four repaired files. It is now a
// repo scan: every .ts/.tsx under app/, components/, hooks/, lib/ and stores/.
// A list has to be remembered; a scan cannot be forgotten. e2e/ is excluded on
// purpose — those specs walk the deleted routes deliberately to assert the
// 404/redirect behaviour, which playwright.config.ts:7 already acknowledges.
//
// WHY THE MATCHER IS A REGEX AND NOT A PREFIX STRING. It used to be the literal
// "/crews/agents/" — with a trailing slash, because every site found in the
// first sweep was a sub-route. That shape cannot see the *index*: the toolbar
// breadcrumb's <Link href="/crews/agents"> and app/(dashboard)/agents/page.tsx's
// router.replace("/crews/agents") both sailed through a green gate into a route
// that has no page.tsx and no web/out/crews/agents.html — the Go static handler
// falls all the way through to the SPA root, so the click lands the user on the
// dashboard under a URL that promises an agent roster. A gate aimed one segment
// too deep is the kind that gets trusted.
//
// So the rule is the whole family, index and subtree alike, and it is checked
// after unescaping backslash-slash — /^\/crews\/agents\/([^/]+)/ is a live
// reference to the dead route written in a form a substring search cannot see,
// and that regex was in fact the gate that kept the toolbar's dead branch
// alive. `/crews?agent=<slug>`, the convention that replaced all of this, does
// not contain the sequence and is never flagged.
// =============================================================================

const REPO_ROOT = join(__dirname, "..", "..", "..", "..")

const SCAN_ROOTS = ["app", "components", "hooks", "lib", "stores"]

/** Never descended into. e2e is the deliberate exception (see header). */
const SKIP_DIRS = new Set(["node_modules", ".next", "out", ".git", "e2e", "coverage"])

/**
 * This file necessarily contains the very string it is looking for. Excluding
 * itself is the only exemption; every other test file is scanned like any
 * other source, because a test that still drives a deleted route is also a
 * lie, just a slower one.
 */
const SELF = join("app", "(onboarding)", "onboarding", "__tests__", "dead-agent-routes.test.ts")

/**
 * The dead family: `/crews/agents` and anything below it. The negative
 * lookahead stops the match one character short of a longer word so a
 * hypothetical sibling segment (`/crews/agents-archive`) would have to be
 * judged on its own merits rather than inheriting this verdict; `/crews/agents`
 * itself, `/crews/agents/`, `/crews/agents/<id>/chat`, `/crews/agents?x=1` and
 * the same strings closed by a quote all match.
 *
 * Assembled from parts for the same reason the old prefix constant was: this
 * file is scanned like any other, and a literal here would report itself.
 */
const DEAD_ROUTE_RE = new RegExp(["", "crews", "agents"].join("/") + "(?![\\w-])")

/**
 * Undo regex-literal escaping before matching. `/^\/crews\/agents\/([^/]+)/`
 * is a reference to the dead route that a search for the plain path cannot
 * see — and that exact regex is how the app toolbar kept a breadcrumb branch
 * for a deleted page. Unescaping is safe in the only direction that matters:
 * it can add matches, never hide one.
 */
function unescapeSlashes(line: string): string {
  return line.split("\\/").join("/")
}

function hitsDeadRoute(line: string): boolean {
  return DEAD_ROUTE_RE.test(unescapeSlashes(line))
}

function walk(dir: string, out: string[] = []): string[] {
  let entries
  try {
    entries = readdirSync(dir, { withFileTypes: true })
  } catch {
    return out
  }
  for (const e of entries) {
    const full = join(dir, e.name)
    if (e.isDirectory()) {
      if (SKIP_DIRS.has(e.name)) continue
      walk(full, out)
    } else if (/\.tsx?$/.test(e.name)) {
      out.push(full)
    }
  }
  return out
}

/**
 * Blank out comments, keeping line numbers, so the check reads code and not
 * prose. Each repaired file now carries a comment saying which dead route it
 * used to point at and why — naming the mistake is the point of the comment,
 * and a checker that cannot tell a link from an explanation would force those
 * comments to be written in circumlocutions.
 *
 * This is a left-to-right scanner and not the pair of regexes it replaced.
 * The regex version ran the block rule first, so a LINE comment that happened
 * to contain the two characters `/` `*` opened a block that swallowed
 * everything up to the next `*​/` — tens of lines of real code, silently
 * exempted. agent-card.tsx was exactly that: a prose mention of a
 * `group/*` Tailwind class on line 122 blanked the dead <Link> on line 138,
 * so the file passed this check while shipping a 404. A checker with a blind
 * spot is worse than no checker, because it is believed.
 *
 * String literals are copied verbatim rather than parsed — the routes we are
 * hunting live inside them, and mis-tracking a quote can only ever make the
 * check *stricter* (an unstripped comment gets reported), never blind.
 */
export function stripComments(src: string): string {
  let out = ""
  let i = 0
  const n = src.length
  // Last character emitted as code. Guards regex literals like /\/\//: a
  // slash escaped by a backslash never starts a comment.
  let prev = ""

  while (i < n) {
    const c = src[i]
    const d = i + 1 < n ? src[i + 1] : ""

    if (c === "/" && d === "/" && prev !== "\\") {
      while (i < n && src[i] !== "\n") {
        out += " "
        i++
      }
      prev = ""
      continue
    }

    if (c === "/" && d === "*" && prev !== "\\") {
      while (i < n && !(src[i] === "*" && src[i + 1] === "/")) {
        out += src[i] === "\n" ? "\n" : " "
        i++
      }
      if (i < n) {
        out += "  "
        i += 2
      }
      prev = ""
      continue
    }

    if (c === '"' || c === "'" || c === "`") {
      out += c
      i++
      while (i < n) {
        if (src[i] === "\\") {
          out += src.slice(i, i + 2)
          i += 2
          continue
        }
        const ch = src[i]
        out += ch
        i++
        if (ch === c) break
      }
      prev = c
      continue
    }

    out += c
    prev = c
    i++
  }
  return out
}

function offendersIn(rel: string): string[] {
  const src = stripComments(readFileSync(join(REPO_ROOT, rel), "utf8"))
  return src
    .split("\n")
    .map((line, i) => [i + 1, line] as const)
    .filter(([, line]) => hitsDeadRoute(line))
    .map(([n, line]) => `${rel.split(sep).join("/")}:${n}: ${line.trim()}`)
}

function scan(): string[] {
  const found: string[] = []
  for (const root of SCAN_ROOTS) {
    for (const abs of walk(join(REPO_ROOT, root))) {
      const rel = relative(REPO_ROOT, abs)
      if (rel === SELF) continue
      found.push(...offendersIn(rel))
    }
  }
  return found.sort()
}

// The files repaired so far, kept as named cases purely so a regression in
// one of them reads as "approval-detail.tsx is broken again" in the runner
// output instead of being one line inside a hundred-line array diff. The
// repo-wide scan below is the actual gate.
const REPAIRED = [
  "app/(onboarding)/onboarding/page.tsx",
  "components/features/dashboard/welcome-checklist.tsx",
  "app/(dashboard)/chat/[agentSlug]/chat-page-client.tsx",
  "components/features/crews/agent-canvas-tabs/overview-tab.tsx",
  "components/features/approvals/approval-detail.tsx",
  "components/features/journal/runs-view.tsx",
  "components/features/journal/journal-entry-card.tsx",
  "components/features/agents/agent-card.tsx",
  "components/features/crews/crew-agents.tsx",
  "components/features/onboarding/setup-nudge.tsx",
  "hooks/use-active-runs.ts",
  // Index-route sweep: these two linked at /crews/agents itself, which the
  // trailing-slash prefix this gate used to carry could not see.
  "components/layout/app-toolbar.tsx",
  "app/(dashboard)/agents/page.tsx",
]

// The matcher is the whole gate, so it gets pinned directly rather than only
// through the files it happens to clear today. The negative cases are the
// reason this check can be run over the real tree at all: the surviving
// convention and the prose that explains the dead one both have to survive it.
describe("the /crews/agents matcher", () => {
  it.each([
    ["/crews", "agents"].join("/"),
    ["/crews", "agents/"].join("/"),
    ["/crews", "agents/ag_123"].join("/"),
    ["/crews", "agents/ag_123/chat"].join("/"),
    ["/crews", "agents/new?crew_id=c1"].join("/"),
    `router.replace("${["/crews", "agents"].join("/")}")`,
    // Escaped-slash form — a regex literal matching the dead route.
    "const AGENT_PATH_RE = /^\\/crews\\/agents\\/([^/]+)/",
  ])("flags %s", (line) => {
    expect(hitsDeadRoute(line)).toBe(true)
  })

  it.each([
    "/crews",
    "/crews?agent=casey",
    'href={`/crews?agent=${encodeURIComponent(slug)}`}',
    "/crews?new=agent",
    "/crews?crew=platform",
    "/chat/casey",
    "/agents",
    "/api/v1/agents",
    "/api/v1/agents/ag_123/chats",
  ])("allows %s", (line) => {
    expect(hitsDeadRoute(line)).toBe(false)
  })
})

describe("no component links into the deleted /crews/agents family", () => {
  it.each(REPAIRED)("%s", (rel) => {
    expect(offendersIn(rel)).toEqual([])
  })

  it("nothing under app/, components/, hooks/, lib/ or stores/ does either", () => {
    expect(scan()).toEqual([])
  })

  it("actually scanned something (guards the walker itself)", () => {
    // A walker that silently returns [] would make the gate above vacuous.
    const files = SCAN_ROOTS.flatMap((r) => walk(join(REPO_ROOT, r)))
    expect(files.length).toBeGreaterThan(200)
  })
})
