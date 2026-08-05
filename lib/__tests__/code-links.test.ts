import { describe, it, expect } from "vitest"

import {
  codeLinkBranches,
  codeLinkNoun,
  codeLinkProblemMessage,
  codeLinkRef,
  codeLinkStaleReason,
  codeLinkStateBadge,
  MAX_STATE_LABEL,
  safeExternalHref,
} from "@/lib/code-links"
import type { IssueCodeLink } from "@/lib/types/mission"

function link(over: Partial<IssueCodeLink> = {}): IssueCodeLink {
  return {
    id: "cmsezurje0003d5e624ad",
    mission_id: "m1",
    workspace_id: "ws1",
    provider: "GITHUB",
    host: "github.com",
    owner: "acme",
    repo: "thing",
    number: 7,
    kind: "PULL_REQUEST",
    url: "https://github.com/acme/thing/pull/7",
    title: "Add the widget",
    state: "OPEN",
    author: "octocat",
    source_branch: "feat/widget",
    target_branch: "main",
    remote_created_at: null,
    remote_updated_at: null,
    remote_merged_at: null,
    remote_closed_at: null,
    credential_id: "cred1",
    last_synced_at: "2026-08-04T18:31:03Z",
    last_sync_error: null,
    created_at: "2026-08-04T18:31:03Z",
    updated_at: "2026-08-04T18:31:03Z",
    ...over,
  }
}

describe("codeLinkStateBadge", () => {
  // The four states are four different things and the reader is scanning for
  // which. They must not collapse onto one another — a merged pull request
  // and a closed one being the same colour is the failure this asserts away.
  it("gives each state its own label and tone", () => {
    expect(codeLinkStateBadge("OPEN")).toEqual({ label: "Open", tone: "success", icon: "open" })
    expect(codeLinkStateBadge("DRAFT")).toEqual({ label: "Draft", tone: "default", icon: "draft" })
    expect(codeLinkStateBadge("MERGED")).toEqual({ label: "Merged", tone: "purple", icon: "merged" })
    expect(codeLinkStateBadge("CLOSED")).toEqual({
      label: "Closed",
      tone: "destructive",
      icon: "closed",
    })
  })

  // The card draws these in a fixed-width column so the states line down the
  // left edge. That column is sized to MAX_STATE_LABEL; a longer label
  // overflows it into the title.
  it("never returns a label wider than the column can hold", () => {
    for (const s of ["OPEN", "DRAFT", "MERGED", "CLOSED", null, "NONSENSE"]) {
      expect(codeLinkStateBadge(s).label.length).toBeLessThanOrEqual(MAX_STATE_LABEL)
    }
  })

  it("keeps the four tones distinct", () => {
    const tones = (["OPEN", "DRAFT", "MERGED", "CLOSED"] as const).map(
      (s) => codeLinkStateBadge(s).tone,
    )
    expect(new Set(tones).size).toBe(4)
  })

  it("accepts the provider's casing", () => {
    expect(codeLinkStateBadge("merged").label).toBe("Merged")
    expect(codeLinkStateBadge("  Open ").label).toBe("Open")
  })

  // A row whose state never arrived (the column is nullable) still has to
  // render, and it must not be dressed as one of the real states.
  it("falls back to Unknown for a missing or unrecognised state", () => {
    expect(codeLinkStateBadge(null)).toEqual({ label: "Unknown", tone: "default", icon: "unknown" })
    expect(codeLinkStateBadge(undefined).label).toBe("Unknown")
    expect(codeLinkStateBadge("LOCKED").label).toBe("Unknown")
    expect(codeLinkStateBadge("").label).toBe("Unknown")
  })

  // The label is ours, never the wire's. A state string is server-normalised
  // today, but rendering it back verbatim would make the badge a text channel
  // the provider controls.
  it("never echoes the wire value", () => {
    expect(codeLinkStateBadge("<img src=x onerror=alert(1)>").label).toBe("Unknown")
  })
})

describe("codeLinkRef", () => {
  it("reads owner/repo#number", () => {
    expect(codeLinkRef(link())).toBe("acme/thing#7")
  })

  it("keeps a nested GitLab group path intact", () => {
    expect(codeLinkRef(link({ owner: "acme/platform", repo: "gw", number: 12 }))).toBe(
      "acme/platform/gw#12",
    )
  })
})

describe("codeLinkNoun", () => {
  it("names the object the way its provider does", () => {
    expect(codeLinkNoun("GITHUB")).toBe("pull request")
    expect(codeLinkNoun("GITLAB")).toBe("merge request")
  })

  it("falls back to the generic noun", () => {
    expect(codeLinkNoun("SOMETHING_ELSE")).toBe("pull request")
  })
})

describe("codeLinkBranches", () => {
  it("renders source → target", () => {
    expect(codeLinkBranches(link())).toBe("feat/widget → main")
  })

  // The CLI prints an em-dash when either half is missing rather than half an
  // arrow; the card just omits the line.
  it("is null unless both halves are known", () => {
    expect(codeLinkBranches(link({ source_branch: null }))).toBeNull()
    expect(codeLinkBranches(link({ target_branch: null }))).toBeNull()
    expect(codeLinkBranches(link({ source_branch: "  ", target_branch: "main" }))).toBeNull()
  })
})

describe("safeExternalHref", () => {
  it("passes http and https through unchanged", () => {
    expect(safeExternalHref("https://github.com/acme/thing/pull/7")).toBe(
      "https://github.com/acme/thing/pull/7",
    )
    // A self-hosted forge on a plain-http intranet is a supported target.
    expect(safeExternalHref("http://ghe.acme.internal/platform/gw/pull/12")).toBe(
      "http://ghe.acme.internal/platform/gw/pull/12",
    )
  })

  // The server reconstructs this URL from parsed parts, so it cannot be a
  // javascript: URL today. The guard is what keeps that true if the column is
  // ever fed from somewhere else — an href is the one place a stored string
  // becomes executable.
  it("refuses any scheme that is not http(s)", () => {
    expect(safeExternalHref("javascript:alert(1)")).toBeUndefined()
    expect(safeExternalHref("JavaScript:alert(1)")).toBeUndefined()
    expect(safeExternalHref("data:text/html,<script>alert(1)</script>")).toBeUndefined()
    expect(safeExternalHref("vbscript:msgbox(1)")).toBeUndefined()
    expect(safeExternalHref("  javascript:alert(1)")).toBeUndefined()
    expect(safeExternalHref("java\tscript:alert(1)")).toBeUndefined()
  })

  it("refuses a relative or unparseable value", () => {
    expect(safeExternalHref("/issues/ENG-4")).toBeUndefined()
    expect(safeExternalHref("not a url")).toBeUndefined()
    expect(safeExternalHref("")).toBeUndefined()
    expect(safeExternalHref(null)).toBeUndefined()
    expect(safeExternalHref(undefined)).toBeUndefined()
  })
})

describe("codeLinkStaleReason", () => {
  it("is null while the last refresh succeeded", () => {
    expect(codeLinkStaleReason(link())).toBeNull()
    expect(codeLinkStaleReason(link({ last_sync_error: "" }))).toBeNull()
    expect(codeLinkStaleReason(link({ last_sync_error: "   " }))).toBeNull()
  })

  // The refresh keeps the state it already had, so the card is showing what
  // was true at last_synced_at. Saying so is the difference between "merged"
  // and "was merged the last time we could look".
  it("returns the recorded reason when the refresh is failing", () => {
    expect(codeLinkStaleReason(link({ last_sync_error: "401 from github.com" }))).toBe(
      "401 from github.com",
    )
  })
})

describe("codeLinkProblemMessage", () => {
  // The 412 is the common failure and its detail already names the fix.
  // Replacing it with "Failed to attach link" throws away the only sentence
  // that tells the reader what to do.
  it("prefers the server's detail", () => {
    const body = {
      type: "https://crewship.ai/problems/code-link/no-credential",
      title: "Precondition Failed",
      status: 412,
      code: "no-credential",
      detail:
        'No ACTIVE GITHUB credential in this workspace can reach ghe.acme.internal. Add one, and for a self-hosted instance set its account label to "ghe.acme.internal" so it is matched by host.',
    }
    expect(codeLinkProblemMessage(body, "Failed to attach link")).toBe(body.detail)
  })

  it("carries blocked-host and already-linked through verbatim too", () => {
    expect(
      codeLinkProblemMessage(
        { code: "blocked-host", detail: "blocked host: 10.0.0.5 is a private address" },
        "x",
      ),
    ).toBe("blocked host: 10.0.0.5 is a private address")
    expect(
      codeLinkProblemMessage(
        { code: "already-linked", detail: "https://github.com/acme/thing/pull/7 is already linked to this issue" },
        "x",
      ),
    ).toBe("https://github.com/acme/thing/pull/7 is already linked to this issue")
  })

  // `invalid-body` answers with detail "Invalid JSON body", which is true and
  // useless to a reader who pasted a URL. Anything without a detail at all
  // falls through to the caller's sentence.
  it("falls back when there is no detail", () => {
    expect(codeLinkProblemMessage({ title: "Bad Gateway", status: 502 }, "Could not attach")).toBe(
      "Could not attach",
    )
    expect(codeLinkProblemMessage(null, "Could not attach")).toBe("Could not attach")
    expect(codeLinkProblemMessage("nonsense", "Could not attach")).toBe("Could not attach")
    expect(codeLinkProblemMessage({ detail: "   " }, "Could not attach")).toBe("Could not attach")
    expect(codeLinkProblemMessage({ detail: 42 }, "Could not attach")).toBe("Could not attach")
  })

  // The detail is a server string, but it embeds the host and URL the user
  // pasted. It is rendered as text — this only bounds how much text.
  it("bounds an absurdly long detail", () => {
    const long = "x".repeat(2000)
    const out = codeLinkProblemMessage({ detail: long }, "fallback")
    expect(out.length).toBeLessThanOrEqual(400)
    expect(out.endsWith("…")).toBe(true)
  })
})
