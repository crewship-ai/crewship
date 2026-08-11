/**
 * The URLs /activity has always accepted, and where they now land.
 *
 * This route used to be the run-trace canvas, and fourteen places across the
 * product link into it with a query string: an inbox waitpoint links a run, a
 * routine's overview links "view runs" by slug, the activity bell links
 * `?status=active`, an issue card links `?mission=`. Those links were written
 * against a page that no longer exists.
 *
 * Replacing the page without reading them would not have broken anything
 * loudly. Every one of them would still return 200, render the stream, and
 * silently ignore what the reader asked for — which is the worst kind of
 * regression, because nothing reports it and the reader concludes the run they
 * clicked has no trace.
 */

import { describe, expect, it } from "vitest"

import { activityDeepLink } from "@/lib/activity-deeplink"
import { currentStop } from "@/lib/activity-selection"

const parse = (qs: string) => activityDeepLink(new URLSearchParams(qs))

describe("activityDeepLink", () => {
  it("is nothing at all for a bare /activity", () => {
    // Not "the overview by default" — null, so the caller can tell "no deep
    // link" from "a deep link that asked for the overview" and does not clear
    // state the reader arrived with.
    expect(parse("")).toBeNull()
  })

  it("lands on the run an inbox or a routine row linked", () => {
    const d = parse("run=prn_01j4")
    expect(currentStop(d!.path!)).toEqual({ kind: "run", id: "prn_01j4", label: "prn_01j4" })
  })

  it("lands on the routine behind ?pipeline=", () => {
    // The routine overview's "view runs" link. A slug, not an id — that is what
    // the routine lens keys on too.
    const d = parse("pipeline=triage")
    expect(currentStop(d!.path!)).toEqual({ kind: "routine", id: "triage", label: "triage" })
  })

  it("lands on the issue behind ?mission=", () => {
    const d = parse("mission=msn_7")
    expect(currentStop(d!.path!)).toEqual({ kind: "issue", id: "msn_7", label: "msn_7" })
  })

  it("keeps the routine BEHIND the run when a link carries both", () => {
    // routine-card-detail builds `?pipeline=<slug>&run=<id>` so the reader
    // arrives at the run without losing the routine they came from. Back must
    // therefore land on the routine, not on the overview — the whole reason
    // that link carries two params.
    const d = parse("pipeline=triage&run=prn_01j4")
    expect(d!.path!.stops.map((s) => `${s.kind}:${s.id}`)).toEqual(["routine:triage", "run:prn_01j4"])
  })

  it("turns ?status= into the status segment, not into a stop", () => {
    // The activity bell's "view all" is a NARROWING, not a destination. Making
    // it a stop would put a breadcrumb over a filter.
    expect(parse("status=active")).toEqual({ path: null, scope: "active" })
    expect(parse("status=failed")).toEqual({ path: null, scope: "failed" })
  })

  it("ignores a status this page has no segment for", () => {
    // An unknown value is not a reason to show the reader an empty page: the
    // segments are a closed set, and a link written against a different
    // vocabulary should land on everything rather than on nothing.
    expect(parse("status=banana")).toBeNull()
  })

  it("accepts a status alongside a stop", () => {
    const d = parse("run=prn_01j4&status=failed")
    expect(d!.scope).toBe("failed")
    expect(currentStop(d!.path!)?.id).toBe("prn_01j4")
  })

  it("ignores ?step= rather than pretending to honour it", () => {
    // The canvas focused a single step in a side panel; the stream has no such
    // object — a run's steps are the whole RunDrillDown. Landing on the run is
    // the honest answer, and silently dropping the reader at the overview is
    // not.
    const d = parse("run=prn_01j4&step=fetch")
    expect(currentStop(d!.path!)?.kind).toBe("run")
    expect(d!.path!.stops).toHaveLength(1)
  })

  it("ignores empty and whitespace-only values", () => {
    // `?run=` is what a builder produces when its id was undefined. Treating it
    // as a stop lands the reader on a page headed by an empty string.
    expect(parse("run=")).toBeNull()
    expect(parse("run=%20%20")).toBeNull()
    expect(parse("pipeline=&mission=")).toBeNull()
  })

  it("does not invent a stop from a parameter it does not know", () => {
    expect(parse("agent=agt_1&foo=bar")).toBeNull()
  })

  it("puts the most specific destination last when several are named", () => {
    // An issue, a routine and a run at once is not a shape any current link
    // builds, but a URL is user-editable. The run is the most specific thing
    // named, so it is where the reader lands.
    const d = parse("mission=msn_7&pipeline=triage&run=prn_1")
    expect(currentStop(d!.path!)?.kind).toBe("run")
  })
})
