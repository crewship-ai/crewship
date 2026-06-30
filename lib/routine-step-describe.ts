// routine-step-describe — the single canonical per-step renderer and URL
// helpers shared by every routine surface (the readable summary, the
// "What it does" plain-step list, the flow diagram, the mini-trace). One
// switch over the DSL step types lives here so a step renders identically
// everywhere and adding/renaming a kind is a one-place edit.
//
// Pure + framework-free (no React, no DOM, no network). Defensive: a
// half-formed or agent-authored DSL must never throw here, because every
// call site is a render path. Unknown step kinds degrade to a generic
// line rather than crashing.

/** The closed set of step kinds the executor recognises, plus a
 *  synthetic "trigger" row and an "unknown" fallback. Mirrors
 *  internal/pipeline/types.go StepType. */
export type ReadableStepKind =
  | "trigger"
  | "agent_run"
  | "http"
  | "transform"
  | "wait"
  | "code"
  | "call_pipeline"
  | "unknown"

export interface ReadableStep {
  /** 1-based position among real steps. The synthetic trigger row is 0. */
  position: number
  kind: ReadableStepKind
  /** Plain-language headline, e.g. "Ask alex" or "Fetch from Hacker News". */
  title: string
  /** Optional secondary plain-language detail — a prompt excerpt, a
   *  transform expression, a channel, an approval question. */
  detail?: string
  /** Compact technical one-liner mirroring the wireframe's mono subtitle
   *  (e.g. "http GET https://…", "agent_run · fast"). */
  technical?: string
}

/* ------------------------------------------------------------------ *
 *  Small pure helpers                                                 *
 * ------------------------------------------------------------------ */

export function isRecord(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === "object" && !Array.isArray(v)
}

export function asString(v: unknown): string {
  return typeof v === "string" ? v : ""
}

/** First non-empty line of a (possibly multi-line) string, clipped. */
function firstLine(s: string, max = 100): string {
  const line = s
    .split("\n")
    .map((l) => l.trim())
    .find((l) => l.length > 0)
  if (!line) return ""
  return line.length > max ? line.slice(0, max - 1).trimEnd() + "…" : line
}

export function clip(s: string, max = 100): string {
  const t = s.trim()
  return t.length > max ? t.slice(0, max - 1).trimEnd() + "…" : t
}

/** Best-effort hostname extraction that never throws. Strips a leading
 *  "www." and falls back to a cleaned-up version of the raw string for
 *  templated/relative URLs. This is the canonical helper — every routine
 *  surface routes its URL parsing through it so a step reads identically
 *  in the readable summary, the flow diagram and the mini-trace. */
export function hostOf(url: string): string {
  const raw = url.trim()
  if (!raw) return ""
  try {
    return new URL(raw).hostname.replace(/^www\./, "")
  } catch {
    // Templated ("{{ inputs.url }}") or scheme-less — strip protocol +
    // path so we still show something readable.
    return raw.replace(/^[a-z]+:\/\//i, "").split("/")[0] || raw
  }
}

/** A handful of well-known hosts get a friendly label so an http step
 *  to e.g. a Slack webhook reads as "Slack" instead of "hooks.slack.com". */
function knownIntegrationLabel(host: string): string | null {
  const h = host.toLowerCase()
  // Anchored host match: the host must EQUAL the domain or be a subdomain of it
  // (suffix at a dot boundary). A substring test (`h.includes("slack.com")`)
  // is unsafe — "slack.com.evil.com" and "evilslack.com" would both match.
  const is = (...domains: string[]) =>
    domains.some((d) => h === d || h.endsWith("." + d))
  if (is("slack.com")) return "Slack"
  if (is("github.com")) return "GitHub"
  if (is("discord.com", "discordapp.com")) return "Discord"
  if (is("notion.com", "notion.so")) return "Notion"
  if (is("zapier.com")) return "Zapier"
  if (is("ycombinator.com")) return "Hacker News"
  return null
}

/** Pull a "#channel" mention out of a slack body/url, if present. */
function channelHint(...parts: string[]): string | undefined {
  for (const p of parts) {
    const m = p.match(/#[\w-]+/)
    if (m) return m[0]
  }
  return undefined
}

/* ------------------------------------------------------------------ *
 *  Per-step rendering — the one canonical switch                      *
 * ------------------------------------------------------------------ */

export function describeStep(step: unknown, position: number): ReadableStep {
  if (!isRecord(step)) {
    return { position, kind: "unknown", title: "Step" }
  }
  const type = asString(step["type"])

  switch (type) {
    case "agent_run": {
      const agent = asString(step["agent_slug"]).trim()
      const prompt = firstLine(asString(step["prompt"]), 120)
      const complexity = asString(step["complexity"]).trim()
      const model = asString(step["model_override"]).trim()
      const tier = complexity || model || "default"
      return {
        position,
        kind: "agent_run",
        title: agent ? `Ask ${agent}` : "Ask an agent",
        detail: prompt || undefined,
        technical: `agent_run · ${tier}`,
      }
    }

    case "http": {
      const http = isRecord(step["http"]) ? step["http"] : {}
      const method = (asString(http["method"]) || "GET").toUpperCase()
      const url = asString(http["url"])
      const body = asString(http["body"])
      const host = hostOf(url)
      const label = knownIntegrationLabel(host)
      const isRead = method === "GET" || method === "HEAD"
      let title: string
      if (label) {
        if (isRead) {
          title = `Fetch from ${label}`
        } else {
          const channel = channelHint(body, url)
          title = channel ? `${label} → ${channel}` : `Send to ${label}`
        }
      } else {
        title = isRead
          ? `Fetch from ${host || "a URL"}`
          : `Send to ${host || "a URL"}`
      }
      return {
        position,
        kind: "http",
        title,
        technical: url ? `http ${method} ${url}` : `http ${method}`,
      }
    }

    case "transform": {
      const transform = isRecord(step["transform"]) ? step["transform"] : {}
      const expr = asString(transform["expression"])
      return {
        position,
        kind: "transform",
        title: "Transform data",
        detail: expr ? clip(expr, 80) : undefined,
        technical: "transform",
      }
    }

    case "wait": {
      const wait = isRecord(step["wait"]) ? step["wait"] : {}
      const kind = asString(wait["kind"])
      if (kind === "approval") {
        return {
          position,
          kind: "wait",
          title: "Wait for approval",
          detail: firstLine(asString(wait["approval_prompt"]), 100) || undefined,
          technical: "wait · approval",
        }
      }
      if (kind === "datetime") {
        const until = asString(wait["until"]).trim()
        return {
          position,
          kind: "wait",
          title: "Wait until a set time",
          detail: until || undefined,
          technical: "wait · datetime",
        }
      }
      if (kind === "event") {
        const ev = asString(wait["event_type"]).trim()
        return {
          position,
          kind: "wait",
          title: ev ? `Wait for event: ${ev}` : "Wait for an event",
          technical: "wait · event",
        }
      }
      return { position, kind: "wait", title: "Wait", technical: "wait" }
    }

    case "code": {
      const code = isRecord(step["code"]) ? step["code"] : {}
      const runtime = asString(code["runtime"]).trim()
      return {
        position,
        kind: "code",
        title: runtime ? `Run ${runtime} code` : "Run code",
        technical: runtime ? `code · ${runtime}` : "code",
      }
    }

    case "call_pipeline": {
      const target = asString(step["pipeline_slug"]).trim()
      return {
        position,
        kind: "call_pipeline",
        title: target ? `Call routine ${target}` : "Call another routine",
        technical: "call_pipeline",
      }
    }

    default: {
      const id = asString(step["id"]).trim()
      return {
        position,
        kind: "unknown",
        title: type ? `${type} step` : id ? `Step ${id}` : "Step",
        technical: type || undefined,
      }
    }
  }
}
