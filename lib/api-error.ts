/**
 * One reader for the two error shapes crewshipd actually emits.
 *
 * The API answers a failed request in one of two ways, and which one you
 * get depends on the handler, not on the status:
 *
 *   replyError    -> {"error": "..."}                     (helpers.go)
 *   writeProblem  -> {"detail": "...", "title", "status"} (RFC 7807)
 *
 * A client that reads only one of them shows a generic fallback for
 * roughly half the surface, and the repo had drifted into reading both
 * in ~20 places with two different precedences — some `error ?? detail`,
 * some `detail ?? error`. Since a given response carries exactly one of
 * the fields, the precedence never mattered; the inconsistency is a
 * signal that nobody could tell, which is the argument for having one
 * function say it once.
 *
 * `??` alone is also not enough. writeProblem always SETS `detail`, so a
 * handler that passes an empty string produces `{"detail": ""}` — and
 * `body?.detail ?? body?.error` returns `""`, i.e. an empty toast rather
 * than the fallback. Blank is treated as absent here for that reason.
 */

/** Reads a server error message out of a parsed JSON body.
 *
 * `body` is deliberately `unknown`: it is whatever `res.json()` produced,
 * including `null` when the response had no body or failed to parse, and
 * callers should not have to assert a shape before asking this question.
 */
export function apiErrorMessage(body: unknown, fallback: string): string {
  if (body && typeof body === "object") {
    const b = body as Record<string, unknown>
    for (const key of ["detail", "error"] as const) {
      const v = b[key]
      if (typeof v === "string" && v.trim() !== "") return v
    }
  }
  return fallback
}

/** Reads the error message off a failed Response, consuming its body.
 *
 * The `.catch(() => null)` is load-bearing: an error response is not
 * guaranteed to carry JSON (a proxy 502, an upstream HTML error page),
 * and a throw here would replace the server's refusal with a parse
 * error — reporting the wrong failure to the user.
 *
 * Only call this on a response you have already decided is a failure;
 * it consumes the body.
 */
export async function readApiError(res: Response, fallback: string): Promise<string> {
  return (await readApiErrorDetail(res, fallback)).message
}

/**
 * The refusal's sentence AND the body it came in.
 *
 * Some refusals carry more than prose. `POST /api/v1/pages/import` answers a
 * 422 with an `unresolved` array naming every reference it could not bind, and
 * a caller that can only see the sentence has to ask the person to re-read a
 * paragraph instead of showing them the list they need to act on. The body was
 * always parsed here and then dropped on the floor; this returns it.
 *
 * `readApiError` stays the one-line form because that is what almost every
 * call site wants, and widening it would make every one of them handle a
 * value they have no use for.
 */
export async function readApiErrorDetail(
  res: Response,
  fallback: string,
): Promise<{ message: string; body: unknown; code?: string; field?: string }> {
  const body = await res.json().catch(() => null)
  return { message: apiErrorMessage(body, fallback), body, ...apiErrorCodeAndField(body) }
}

/**
 * The machine-readable half of a refusal (#2862).
 *
 * Both envelopes may carry an optional `code` (e.g. `agent_slug_taken`) and
 * the request `field` it concerns (e.g. `slug`): `replyErrorCode` puts them
 * next to `error`, `writeProblemCode` next to `detail`. Branch on `code`,
 * never on the sentence — the sentence is for people and may be reworded.
 */
export function apiErrorCodeAndField(body: unknown): { code?: string; field?: string } {
  const out: { code?: string; field?: string } = {}
  if (body && typeof body === "object") {
    const b = body as Record<string, unknown>
    if (typeof b.code === "string" && b.code.trim() !== "") out.code = b.code
    if (typeof b.field === "string" && b.field.trim() !== "") out.field = b.field
  }
  return out
}

/**
 * A refused request, as one error type for every caller (#2862): the
 * server's sentence as `message`, plus the status, the parsed body and the
 * optional machine `code` and `field`. `ApiMutationError` (use-api-mutation)
 * is this type too, so a dialog can handle a refusal the same way whichever
 * helper made the request.
 */
export class ApiError extends Error {
  readonly status: number
  readonly body: unknown
  readonly code?: string
  readonly field?: string
  constructor(message: string, status: number, body?: unknown) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.body = body
    const { code, field } = apiErrorCodeAndField(body)
    this.code = code
    this.field = field
  }
}

/** Reads a failed Response into an ApiError, consuming its body.
 *
 * A JSON body gives the sentence, code and field. A body that is not JSON
 * keeps the readable behaviour the callers this replaced had
 * (`new Error(await res.text())`): a short plain-text refusal is the message,
 * while an HTML error page or an empty body falls back to `fallback`.
 */
export async function toApiError(res: Response, fallback: string): Promise<ApiError> {
  const text = await res.text().catch(() => "")
  let body: unknown = null
  try {
    body = text ? JSON.parse(text) : null
  } catch {
    const plain = text.trim()
    const readable = plain !== "" && plain.length <= 300 && !plain.startsWith("<")
    return new ApiError(readable ? plain : fallback, res.status, null)
  }
  return new ApiError(apiErrorMessage(body, fallback), res.status, body)
}
