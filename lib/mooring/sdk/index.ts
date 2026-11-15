/** Experimental Mooring SDK entry point for Craft application authors. No
 * external application uses it and there is no compatibility promise; names
 * and shapes may change in any release. Guide: docs/development/mooring-sdk.md,
 * boundary: docs/specs/mooring-profiles.md.
 *
 * - `editor`   the in-frame protocol-1 surface (the same exports as `lib/mooring`).
 * - `lease`    reads the server's single-writer lease and asks for transitions.
 * - `save`     vocabulary for the host's single-editor save lifecycle.
 * - `recovery` compare/copy offers over a journal handle the host supplies.
 * - `versions` version history over a transport the host supplies.
 *
 * This entry point does not export the host's save lifecycle factory, the
 * private draft journal opener or the native admission client: the host
 * constructs those and hands them to the application. Import it explicitly as
 * `lib/mooring/sdk`; the root `lib/mooring` does not re-export it. The Go
 * backend remains the only authority for rights, leases, versions and audit. */
export * from "./editor"
export * from "./lease"
export * from "./save"
export * from "./recovery"
export * from "./versions"
