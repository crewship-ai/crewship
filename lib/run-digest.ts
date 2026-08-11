// Thirty runs of one routine, made readable.
//
// A routine on a one-minute schedule produces a list where every row carries
// the same name, the same icon, and — rendered the way the rail renders a
// workflow — the same relative timestamp. "9h ago" thirty times is not a list,
// it is a wall. Two things fix it, and neither is more data:
//
//   · ABSOLUTE TIME. 14:51:00, 14:50:00, 14:49:00. A relative stamp answers
//     "was this recent"; on a per-minute routine the reader already knows it
//     was, and is asking WHICH ONE.
//   · THE RESULT, not the id. run_cmsj6xyay000172c9d652 is thirty characters
//     of noise that differs between rows in a way no human reads. "3 tickets
//     classified" versus "no tickets" is the difference they came for.
//
// And one thing that makes the wall skippable: an hour header that says what
// happened in that hour, so a reader can pass over "12 runs · all ok" without
// reading twelve rows to learn it.
//
// Everything here is derived from what GET .../run-records already sends. No
// field was added for this: `output` has been on that DTO since v83, and a new
// summary column would be a second place for the same fact to be wrong.

/** The subset of a run record this module reads. */
export interface DigestRun {
  id: string
  status: string
  /** Stored instant. Any of the syntaxes the server writes; unparseable is handled. */
  started_at: string
  duration_ms: number
  triggered_via?: string
  /** Last step's output. Author- and model-written — escape before rendering. */
  output?: string
  error_message?: string
}

/** How long the headline may be before it starts owning the column. */
const MAX_HEADLINE = 72

/**
 * What a run's row says about itself, and in which tone.
 *
 * The tone is separate from the text because the row colours a dot from it, and
 * deriving a colour by matching on the text is how "no tickets" ends up red.
 */
export type HeadlineTone = "failed" | "slow" | "running" | "ok"

export interface RunHeadline {
  text: string
  tone: HeadlineTone
}

export interface HeadlineContext {
  /**
   * The median duration of this routine's other runs, if known.
   *
   * "Slow" only means anything against peers. A fixed threshold would flag
   * every run of a routine that simply takes a minute, and would never flag a
   * 900ms run of one that normally takes 40ms — which is the interesting case.
   */
  medianMs?: number
}

/** A run is called slow at this multiple of its peers' median. */
const SLOW_FACTOR = 1.4

/**
 * One line describing what a run did.
 *
 * A failure wins over everything: the row exists so a reader can find the run
 * that went wrong, and the partial output of a failed run is not what they came
 * for. Otherwise it is the first line of the output.
 *
 * An empty output yields an EMPTY STRING rather than a stock phrase. The
 * obvious alternative — "completed" on every row — is the status column
 * rendered a second time, and a column that reads the same on every row tells
 * two runs apart never. Nothing is the honest answer when the routine recorded
 * nothing, and the row still carries its time, duration and status.
 */
export function runHeadline(run: DigestRun, ctx: HeadlineContext = {}): RunHeadline {
  if (isFailedRunStatus(run.status)) {
    return { text: clip(firstLine(run.error_message ?? "")), tone: "failed" }
  }
  if (isRunning(run.status)) {
    return { text: clip(firstLine(run.output ?? "")), tone: "running" }
  }

  const body = clip(firstLine(run.output ?? ""))
  // Slow is a property of the run AND its peers, so it is only claimed when
  // peers are known. Reported alongside the output rather than instead of it:
  // "it took a while" and "here is what it did" are both true and the reader
  // wants both.
  if (ctx.medianMs != null && ctx.medianMs > 0 && run.duration_ms > ctx.medianMs * SLOW_FACTOR) {
    const note = `slow — ${formatSeconds(run.duration_ms)}`
    return { text: body ? `${body} · ${note}` : note, tone: "slow" }
  }
  return { text: body, tone: "ok" }
}

/**
 * Whether a run's status means it did not deliver.
 *
 * EXPORTED because this page counts failures in two places — the hour header's
 * "3 runs · 1 failed" and the strip's "Failed" cell — and while each owned its
 * own idea of the word they disagreed: a cancelled run was one failure in the
 * header and zero in the strip, six pixels apart. One word, one predicate.
 *
 * `cancelled` and `interrupted` are folded in DELIBERATELY, and just as
 * deliberately the server does not fold `interrupted` into the `failed_runs` it
 * puts on a chain row (see FailedRuns in internal/api/chains_list.go: "a process
 * that died mid-run is an operational event"). The two are not in conflict
 * because they answer different questions. A chain row asks *did this workflow
 * fail*, where a restart mid-run is the operator's problem and not the
 * workflow's verdict. This page asks *which of these thirty runs did not
 * produce the thing I came for*, and a run killed by a deploy produced it
 * exactly as little as one that threw. Do NOT "align" the two by editing either
 * side: aligning them makes one of the two questions unanswerable.
 */
export function isFailedRunStatus(status: string): boolean {
  return status === "failed" || status === "cancelled" || status === "interrupted"
}

function isRunning(status: string): boolean {
  return status === "running" || status === "queued" || status === "waiting"
}

/**
 * Whether a first line is structure rather than something a person wrote.
 *
 * A line with no letter and no digit anywhere in it is punctuation: `{`, `[`,
 * `---`, `|`, `-`, `"""`, `##`. A line with either is an answer, however short
 * — "ok" is two characters and is the whole result of a routine that checked
 * four queues and found nothing.
 *
 * This replaced a minimum LENGTH, which got both of its own examples wrong:
 * `---` is three characters and passed the threshold, so a routine emitting
 * YAML front matter got a row reading `---` with the content on line two, and
 * "ok" is two and failed it. Length was standing in for the real question and
 * could not be tuned to answer it — three characters has to be both too short
 * for `---` and short enough for "ok", and no number is both.
 *
 * NOT handled, on purpose: a fenced code block's opener carries a language
 * (```` ```json ````), so it has letters and reads as a sentence here. The row
 * then says "```json" rather than what the block contains. Fixing it means
 * naming fences specifically, which is a rule about markdown rather than about
 * punctuation, and no routine in the wild has produced it yet — recorded here
 * rather than pre-solved.
 */
function isStructuralLead(lead: string): boolean {
  return !/[\p{L}\p{N}]/u.test(lead)
}

/**
 * The one line of an output worth putting on a row.
 *
 * Two shapes of output arrive here and they want opposite treatment:
 *
 *   a routine that prints a sentence and then its details —
 *     "3 tickets classified\nENG-7, ENG-8, ENG-9"
 *   wants the FIRST LINE. Folding the details in makes every row long and the
 *   part that differs between rows scrolls off the end.
 *
 *   a routine that returns pretty-printed JSON —
 *     "{\n  \"ok\": true\n}"
 *   has `{` as its first line. A row reading "{" says nothing while the answer
 *   sits on line two, so the whole blob is COLLAPSED into one line and the clip
 *   keeps its readable start.
 *
 * The rule that tells them apart is isStructuralLead: a first line carrying no
 * letter and no digit is punctuation, and the content is elsewhere.
 */
function firstLine(s: string): string {
  const collapsed = s.replace(/\s+/g, " ").trim()
  const lead = s.split("\n").map((l) => l.trim()).find((l) => l !== "") ?? ""
  return isStructuralLead(lead) ? collapsed : lead
}

function clip(s: string): string {
  return s.length <= MAX_HEADLINE ? s : `${s.slice(0, MAX_HEADLINE - 1).trimEnd()}…`
}

function formatSeconds(ms: number): string {
  return ms >= 10_000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`
}

// ---------------------------------------------------------------------------
// Hours.
// ---------------------------------------------------------------------------

export interface RunHourBucket {
  /**
   * This hour's identity: unique across the returned list, and stable for as
   * long as the hour is.
   *
   * Separate from `label` because the label is prose and prose repeats — a
   * routine on a daily 09:00 cron produces one bucket per day and every one of
   * them is labelled "09:00". Keyed on the label, React saw one hour thirty
   * times over. Derived from the hour INSTANT, which is what the bucketing
   * already keys on, so two buckets share a key only if they are the same hour.
   */
  key: string
  /** "14:03 — 14:51", or "14:03" for a single run, or "undated". */
  label: string
  /**
   * The bucket's calendar day, "Mon 10 Aug" — present ONLY when the buckets in
   * this list cover more than one local day, and never on `undated`.
   *
   * Conditional because the two readers want opposite things. On a per-minute
   * routine every header is today and a date on each is thirty repetitions of
   * something the reader knew before they opened the page, crowding out the
   * times that actually differ — which is why the label is bare in the first
   * place. On a daily cron the date is the ONLY thing that differs, and without
   * it the page is thirty headers reading "09:00".
   *
   * Kept out of `label` rather than folded into it so the label keeps its
   * promise — the span the runs cover, and nothing else — and so the page can
   * give the date its own weight instead of a longer mono string.
   */
  day?: string
  /** "12 runs · all ok" — enough to skip the hour without reading it. */
  summary: string
  /** Whether anything in this hour went wrong, so the header can be coloured. */
  failed: boolean
  runs: DigestRun[]
}

/** The bucket for runs whose stamp cannot be placed on a clock. */
const UNDATED = "undated"

/**
 * Groups runs into the reader's local hours, newest first.
 *
 * LOCAL, not UTC: the reader is comparing against their own morning. An hour
 * boundary drawn in UTC lands in the middle of their 15:37 and puts two runs a
 * minute apart under different headers.
 *
 * The label is the span the runs actually cover, not the hour's definition —
 * "14:03 — 14:51" rather than "14:00 — 15:00". The second is a fact about
 * clocks; the first is a fact about this routine. The DAY that span belongs to
 * rides alongside it in `day`, and only when there is more than one of them to
 * tell apart; see RunHourBucket.
 *
 * A run whose start cannot be parsed goes to an `undated` bucket at the end
 * rather than being dropped. It ran; only its clock is unreadable, and a
 * silently omitted row makes a count disagree with the list under it.
 */
export function groupRunsByHour(runs: DigestRun[]): RunHourBucket[] {
  if (runs.length === 0) return []

  const byKey = new Map<string, { at: number | null; runs: DigestRun[] }>()
  for (const r of runs) {
    const t = Date.parse(r.started_at)
    if (Number.isNaN(t)) {
      pushInto(byKey, UNDATED, null, r)
      continue
    }
    const d = new Date(t)
    d.setMinutes(0, 0, 0)
    pushInto(byKey, String(d.getTime()), d.getTime(), r)
  }

  const buckets = [...byKey.values()].map((b) => {
    // Newest first inside the hour, matching the order of the hours themselves.
    // An undated run sorts last here for the same reason its bucket does.
    const sorted = [...b.runs].sort((x, y) => startOf(y) - startOf(x))
    return {
      // The hour instant, which is already this bucket's identity in `byKey`.
      // Prefixed so it can never collide with the literal "undated".
      key: b.at == null ? UNDATED : `hour:${b.at}`,
      label: b.at == null ? UNDATED : spanLabel(sorted),
      summary: summarise(sorted),
      failed: sorted.some((r) => isFailedRunStatus(r.status)),
      runs: sorted,
      at: b.at,
    }
  })

  buckets.sort((a, b) => {
    if (a.at == null) return 1
    if (b.at == null) return -1
    return b.at - a.at
  })

  // One day among the dated buckets means the reader is looking at a single
  // day and already knows which; two or more means the date is the only thing
  // telling two "09:00" headers apart. The undated bucket is not counted: its
  // day is unknown, not different, and letting it tip the count would stamp a
  // date on every other header on the strength of one unreadable stamp.
  const days = new Set(buckets.filter((b) => b.at != null).map((b) => localDayKey(b.at as number)))
  return buckets.map(({ at, ...rest }) => ({
    ...rest,
    day: at != null && days.size > 1 ? dayLabel(at) : undefined,
  }))
}

/** Which local calendar day an instant falls on — for counting them, not display. */
function localDayKey(at: number): string {
  const d = new Date(at)
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}

/**
 * The day a header wears, "Mon 10 Aug".
 *
 * The weekday earns its space here: the reader of a daily cron is asking "did
 * it run on Monday", and a bare "10 Aug" makes them count. The year does not —
 * a page holding fifty runs of one routine does not reach back a year, and if
 * it ever does the month names repeating is the smallest of that page's
 * problems.
 */
function dayLabel(at: number): string {
  return new Date(at).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" })
}

function pushInto(
  m: Map<string, { at: number | null; runs: DigestRun[] }>,
  key: string,
  at: number | null,
  run: DigestRun,
) {
  const existing = m.get(key)
  if (existing) existing.runs.push(run)
  else m.set(key, { at, runs: [run] })
}

/** Sortable start, with an unreadable stamp pushed to the end. */
function startOf(r: DigestRun): number {
  const t = Date.parse(r.started_at)
  return Number.isNaN(t) ? Number.NEGATIVE_INFINITY : t
}

function spanLabel(sortedNewestFirst: DigestRun[]): string {
  const newest = hhmm(sortedNewestFirst[0])
  const oldest = hhmm(sortedNewestFirst[sortedNewestFirst.length - 1])
  // One run, or several inside the same minute: one time, not a span of zero.
  return newest === oldest ? newest : `${oldest} — ${newest}`
}

function hhmm(r: DigestRun): string {
  const d = new Date(Date.parse(r.started_at))
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
}

/**
 * The hour in one phrase.
 *
 * Failures are named before in-flight work: a broken hour is the one the reader
 * must open, and a header reading "1 running" over an hour that also holds two
 * failures would send them past it.
 */
function summarise(runs: DigestRun[]): string {
  const n = runs.length
  const failed = runs.filter((r) => isFailedRunStatus(r.status)).length
  const running = runs.filter((r) => isRunning(r.status)).length
  const head = `${n} ${n === 1 ? "run" : "runs"}`
  if (failed > 0) return `${head} · ${failed} failed`
  if (running > 0) return `${head} · ${running} running`
  return `${head} · all ok`
}

/**
 * The median duration of the runs that FINISHED, for the slow comparison.
 *
 * Finished only: an in-flight run's duration_ms is a partial value rewritten at
 * every step boundary, so including it drags the median toward whatever the
 * live run has reached so far. Failed runs are excluded too — a run that died
 * at step one is fast for a reason that says nothing about how long the work
 * takes.
 */
export function medianRunDuration(runs: DigestRun[]): number | undefined {
  const done = runs
    .filter((r) => !isFailedRunStatus(r.status) && !isRunning(r.status) && r.duration_ms > 0)
    .map((r) => r.duration_ms)
    .sort((a, b) => a - b)
  if (done.length === 0) return undefined
  const mid = Math.floor(done.length / 2)
  return done.length % 2 === 1 ? done[mid] : Math.round((done[mid - 1] + done[mid]) / 2)
}

/**
 * The longest run that has FINISHED, for the "Slowest" cell.
 *
 * In-flight runs are excluded for exactly the reason the median excludes them:
 * their duration_ms is a partial value rewritten at every step boundary, so a
 * max taken over them reports "40s" for a run that is 40s IN, not 40s long, and
 * the number shrinks when the page is reloaded.
 *
 * Failures are NOT excluded, and that is where this parts company with the
 * median. The median leaves them out because a run that died at step one is
 * fast for a reason that says nothing about how long the work takes — and that
 * reason cannot reach a maximum, since a fast run is never the maximum. What
 * does reach it is the 30-second timeout, which is both a real elapsed time and
 * the single slowest thing this routine did. Dropping it would leave the cell
 * quietly under-reporting the worst case, which is the only case it exists for.
 *
 * Undefined rather than 0 when nothing qualifies: 0ms is a claim about a run
 * that happened, and the strip renders an em dash for "no answer yet".
 */
export function slowestRunDuration(runs: DigestRun[]): number | undefined {
  const done = runs.filter((r) => !isRunning(r.status) && r.duration_ms > 0).map((r) => r.duration_ms)
  return done.length === 0 ? undefined : Math.max(...done)
}
