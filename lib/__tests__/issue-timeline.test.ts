import { describe, expect, it } from "vitest"

import { mergeTimeline, type TimelineItem, type TimelineStream } from "../issue-timeline"

const item = (kind: TimelineItem["kind"], key: string, at: string): TimelineItem => ({ kind, key, at, title: key })
const stream = (items: TimelineItem[], exhausted: boolean, loaded = true): TimelineStream => ({ items, exhausted, loaded })

describe("mergeTimeline", () => {
  it("lists every stream's items newest first", () => {
    const out = mergeTimeline([
      stream([item("event", "e1", "2026-10-01T10:00:00Z"), item("event", "e2", "2026-10-03T10:00:00Z")], true),
      stream([item("comment", "c1", "2026-10-02T10:00:00Z")], true),
      stream([item("run", "r1", "2026-10-04T10:00:00Z")], true),
    ])
    expect(out.items.map((i) => i.key)).toEqual(["r1", "e2", "c1", "e1"])
    expect(out.canLoadOlder).toBe(false)
  })

  it("never shows an item older than a stream that still has older pages", () => {
    // Comments have more pages below 10-05. An event from 10-01 would sit
    // above comments from 10-02 that are not loaded yet — so it waits.
    const out = mergeTimeline([
      stream([item("event", "e_old", "2026-10-01T00:00:00Z"), item("event", "e_new", "2026-10-06T00:00:00Z")], true),
      stream([item("comment", "c_page", "2026-10-05T00:00:00Z")], false),
      stream([], true),
    ])
    expect(out.items.map((i) => i.key)).toEqual(["e_new", "c_page"])
    expect(out.canLoadOlder).toBe(true)
  })

  it("shows nothing for a stream until its first page has arrived", () => {
    const out = mergeTimeline([
      stream([item("event", "e1", "2026-10-06T00:00:00Z")], true),
      stream([], false, false),
    ])
    expect(out.items).toEqual([])
    expect(out.loading).toBe(true)
  })

  it("lists each item once when pages overlap", () => {
    const e = item("event", "e1", "2026-10-06T00:00:00Z")
    const out = mergeTimeline([stream([e, { ...e }], true)])
    expect(out.items).toHaveLength(1)
  })

  it("drops the event log's 'commented' rows, which the comment stream already holds", () => {
    const out = mergeTimeline([
      stream([{ ...item("event", "e1", "2026-10-06T00:00:00Z"), action: "commented" }], true),
      stream([item("comment", "c1", "2026-10-06T00:00:00Z")], true),
    ])
    expect(out.items.map((i) => i.key)).toEqual(["c1"])
  })
})
