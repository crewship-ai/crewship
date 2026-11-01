/** Mooring SDK entry (docs/development/mooring-sdk.md). The in-frame editor
 * surface, `./sdk/editor`, has no Crewship auth, HTTP or UI dependencies; an
 * isolated editor bundle imports that module directly. The lease, save,
 * recovery and versions modules are host-side and wrap the host's admitted
 * transport and private draft journal. */
export * from "./sdk"
export type { SaveStatus } from "./editor-sdk"
