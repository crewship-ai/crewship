import { describe, it, expect } from "vitest"
import {
  JOURNAL_URL_KEYS,
  journalFiltersFromJson,
  journalFiltersFromSearch,
  parseJournalUrl,
} from "@/hooks/use-journal-url-state"

// =============================================================================
// The decoder is the page's whole trust boundary now that the URL is the
// state: a bookmark from six months ago, a link pasted into Slack with a
// truncated param, and a `crewship saved-view create --filters '{…}'` payload
// written by hand all arrive here. None of them may produce a value the
// backend or the RBAC clamp did not expect.
// =============================================================================

const parse = (qs: string) => parseJournalUrl(new URLSearchParams(qs))

describe("parseJournalUrl", () => {
  it("defaults an empty query string", () => {
    const s = parse("")
    expect(s.tab).toBe("timeline")
    expect(s.timeRange).toBe("24h")
    expect(s.customRange).toBeNull()
    expect(s.severity).toBe("all")
    expect(s.muted.size).toBe(0)
    expect(s.q).toBe("")
    expect(s.runWindow).toBe("24h")
    expect(s.runStatus).toBe("all")
    expect(s.runTrigger).toBe("all")
    expect(s.runPage).toBe(1)
  })

  it("reads the full timeline contract", () => {
    const s = parse(
      "tab=runs&time=7d&crew_id=c1&agent_id=a1&trace_id=t1&severity=error&mute=container,exec&q=boom",
    )
    expect(s.tab).toBe("runs")
    expect(s.timeRange).toBe("7d")
    expect(s.crewId).toBe("c1")
    expect(s.agentId).toBe("a1")
    expect(s.traceId).toBe("t1")
    expect(s.severity).toBe("error")
    expect(s.q).toBe("boom")
  })

  it("reads the runs contract", () => {
    const s = parse("run_window=30d&run_status=FAILED&run_trigger=CRON&run_page=4")
    expect(s.runWindow).toBe("30d")
    expect(s.runStatus).toBe("FAILED")
    expect(s.runTrigger).toBe("CRON")
    expect(s.runPage).toBe(4)
  })

  it("falls back rather than binding a value the backend would reject", () => {
    const s = parse(
      "tab=admin&time=forever&severity=critical&mute=container,nonsense&run_window=1y&run_status=DROP+TABLE&run_trigger=hax&run_page=-3",
    )
    expect(s.tab).toBe("timeline")
    expect(s.timeRange).toBe("24h")
    expect(s.severity).toBe("all")
    expect(Array.from(s.muted)).toEqual(["container"])
    expect(s.runWindow).toBe("24h")
    expect(s.runStatus).toBe("all")
    expect(s.runTrigger).toBe("all")
    expect(s.runPage).toBe(1)
  })

  it("only accepts a custom range that is actually a range", () => {
    expect(parse("time=custom&from=100&to=200").customRange).toEqual({ fromMs: 100, toMs: 200 })
    // to <= from, and a bare `from` with no `to`: both used to slip through as
    // {fromMs: NaN} because Number("") is 0, not NaN.
    expect(parse("time=custom&from=200&to=100").customRange).toBeNull()
    expect(parse("time=custom&from=200").customRange).toBeNull()
    expect(parse("time=custom&to=200").customRange).toBeNull()
    expect(parse("time=custom&from=a&to=b").customRange).toBeNull()
  })

  it("keeps a non-numeric run_page off the API", () => {
    expect(parse("run_page=abc").runPage).toBe(1)
    expect(parse("run_page=2.5").runPage).toBe(1)
    expect(parse("run_page=0").runPage).toBe(1)
  })
})

describe("journalFiltersFromSearch", () => {
  it("keeps only the keys this page owns", () => {
    const f = journalFiltersFromSearch("tab=runs&q=boom&utm_source=slack&sidebar=open")
    expect(f).toEqual({ tab: "runs", q: "boom" })
  })

  it("drops empty values so a saved view does not store noise", () => {
    expect(journalFiltersFromSearch("tab=runs&q=&crew_id=")).toEqual({ tab: "runs" })
  })

  it("round-trips every key in the contract", () => {
    const qs = JOURNAL_URL_KEYS.map((k) => `${k}=v-${k}`).join("&")
    expect(Object.keys(journalFiltersFromSearch(qs)).sort()).toEqual([...JOURNAL_URL_KEYS].sort())
  })
})

describe("journalFiltersFromJson", () => {
  it("reads the envelope this UI writes", () => {
    const raw = JSON.stringify({ surface: "journal", params: { tab: "runs", q: "boom" } })
    expect(journalFiltersFromJson(raw)).toEqual({ tab: "runs", q: "boom" })
  })

  // `crewship saved-view create --filters '{"tab":"runs"}'` is the documented
  // way to make one from a script; it has no reason to know about an envelope.
  it("reads a flat payload written from the CLI", () => {
    expect(journalFiltersFromJson('{"tab":"runs","run_status":"FAILED"}')).toEqual({
      tab: "runs",
      run_status: "FAILED",
    })
  })

  it("accepts a numeric page — JSON has numbers, URLs do not", () => {
    expect(journalFiltersFromJson('{"run_page":3}')).toEqual({ run_page: "3" })
  })

  // filters_json is free-form and writable by anyone with the CLI. A view
  // must not be able to inject params this page does not own, and a malformed
  // one must not throw inside a click handler.
  it("ignores keys the journal does not own", () => {
    expect(journalFiltersFromJson('{"tab":"runs","__proto__x":"x","redirect":"//evil"}')).toEqual({
      tab: "runs",
    })
  })

  it("returns null for junk instead of throwing", () => {
    expect(journalFiltersFromJson("not json")).toBeNull()
    expect(journalFiltersFromJson("")).toBeNull()
    expect(journalFiltersFromJson(null)).toBeNull()
    expect(journalFiltersFromJson("[1,2,3]")).toEqual({})
    expect(journalFiltersFromJson('"a string"')).toBeNull()
  })
})
