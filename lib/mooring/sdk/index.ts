/** Mooring SDK v1: the one surface Craft applications and artifact types build
 * on. Guide: docs/development/mooring-sdk.md.
 *
 * - `editor`   runs inside the isolated editor iframe (protocol 1).
 * - `lease`    the server's single-writer lease: holder, generation, actions.
 * - `save`     the single-editor save lifecycle: statuses, receipts, reconcile.
 * - `recovery` local drafts after a crash or handoff: compare/copy only.
 * - `versions` version history; restore creates a new version.
 *
 * Every module wraps the existing host implementation. The Go backend remains
 * the only authority for rights, leases, versions, quota and audit. */
export * from "./editor"
export * from "./lease"
export * from "./save"
export * from "./recovery"
export * from "./versions"
