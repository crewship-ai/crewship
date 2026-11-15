/**
 * The instance administrator turned Word or Sheet off, or it is draining
 * (#5381). The server answers 503 with `reason` `app_draining` or
 * `app_disabled` and the `application`; the editor shows reading only.
 */
const LABEL: Record<string, string> = { wordcraft: "Word", gridcraft: "Sheet" }

export type NativeAppOffReason = "app_draining" | "app_disabled"

export class NativeAppOffError extends Error {
  constructor(readonly reason: NativeAppOffReason, readonly application: string) { super(reason) }
  /** "Word" or "Sheet". */
  get label(): string { return LABEL[this.application] ?? "This application" }
}

/** The application's product name, or null for anything else. */
export const nativeAppLabel = (application: unknown): string | null => typeof application === "string" ? LABEL[application] ?? null : null

/** What the editor says while an administrator has the application draining or off. */
export const appOffText = (label: string) => `An administrator turned ${label} off on this server. You can read this document; editing is not available.`

/** A 503 refusal body from a Craft route, as an error; null for any other answer. */
export async function nativeAppOffFrom(response: Response, fallbackApplication?: string): Promise<NativeAppOffError | null> {
  if (response.status !== 503) return null
  const value: unknown = await response.clone().json().catch(() => null)
  if (!value || typeof value !== "object" || Array.isArray(value)) return null
  const { reason, application } = value as Record<string, unknown>
  if (reason !== "app_draining" && reason !== "app_disabled") return null
  const app = typeof application === "string" && LABEL[application] ? application : fallbackApplication
  return app && LABEL[app] ? new NativeAppOffError(reason, app) : null
}

/**
 * Whether an availability answer says this document's application is draining
 * or off (#5381): the label, or null. The server sets `edit_refusal` when this
 * person could otherwise edit; an answer that still claims `can_edit` while the
 * application's mode is not open is treated the same, so editing the server
 * would refuse is never offered.
 */
export function nativeAppOff(fields: Record<string, unknown>): string | null {
  const label = nativeAppLabel(fields.application)
  if (!label || fields.enabled !== true) return null
  const modes = fields.applications && typeof fields.applications === "object" ? fields.applications as Record<string, unknown> : {}
  const mode = modes[fields.application as string]
  const refused = fields.edit_refusal === "app_draining" || fields.edit_refusal === "app_disabled"
  return refused || fields.can_edit === true && (mode === "draining" || mode === "closed") ? label : null
}

/** What the person holding the lease reads when a hand-over is refused during the drain. */
export const appDrainHolderText = (label: string) => `An administrator turned ${label} off on this server, so editing cannot be handed over. You can keep editing; your changes keep saving until it closes.`
