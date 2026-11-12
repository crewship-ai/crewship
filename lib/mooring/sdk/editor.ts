/** Experimental Mooring SDK · in-frame editor. Exactly the root `lib/mooring`
 * surface, which runs inside the isolated, credential-less editor iframe and
 * has no Crewship authentication, HTTP, storage or UI dependencies. An editor
 * bundle imports `lib/mooring` itself, never this whole SDK. */
export * from "../index"
export type { SaveStatus as EditorSaveStatus } from "../index"
