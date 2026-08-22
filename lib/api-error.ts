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
): Promise<{ message: string; body: unknown }> {
  const body = await res.json().catch(() => null)
  return { message: apiErrorMessage(body, fallback), body }
}
