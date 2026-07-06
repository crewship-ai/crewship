package database

// DefaultCrewNetworkMode is the fail-safe egress default for a NEWLY-created
// crew: "restricted" with an empty allowlist (LLM/CLI providers still reach via
// the sidecar's DefaultAllowedDomains; arbitrary egress is denied). Every
// crew-creation path writes this explicitly at insert time so a new path can't
// silently reintroduce allow-all "free" by omitting the column.
//
// This is the single source of the CREATE default. It intentionally does NOT
// touch the v18 schema column default ('free') — that stays for back-compat and
// grandfathers existing crews (their stored value is untouched); only new rows
// get this value.
const DefaultCrewNetworkMode = "restricted"
