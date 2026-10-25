import type { AskAnswerError } from "@/lib/ask-validate"

/** A refused server submission, carried back to the still-open form. */
export class AskSubmissionError extends Error {
  constructor(message: string, readonly errors: AskAnswerError[] = []) {
    super(message)
    this.name = "AskSubmissionError"
  }
}

export function askSubmissionError(body: unknown): AskSubmissionError {
  const data = body && typeof body === "object" ? body as Record<string, unknown> : {}
  const errors = Array.isArray(data.ask_errors) ? data.ask_errors.filter(
    (item): item is AskAnswerError => !!item && typeof item === "object" &&
      typeof item.field === "string" && typeof item.code === "string" && typeof item.message === "string",
  ) : []
  const message = typeof data.error === "string" ? data.error : errors[0]?.message ?? "The form could not be sent. Try again."
  return new AskSubmissionError(message, errors)
}
