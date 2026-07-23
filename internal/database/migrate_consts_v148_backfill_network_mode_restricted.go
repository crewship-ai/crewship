package database

// v148: backfill every legacy crews.network_mode='free' row to 'restricted'.
//
// New crews created through the app already default to 'restricted'
// (DefaultCrewNetworkMode in crew_defaults.go, written explicitly by every
// create path). But the v18 column DEFAULT is still 'free' and pre-v18 rows
// were backfilled to 'free', so grandfathered crews + any seed drift kept
// unrestricted egress. Tightening them closes that gap: 'free' was never an
// intentional per-crew choice for those rows, so we fail them closed to
// 'restricted' (DECISION 2026-07-23, issue #1366). A restricted crew still
// reaches every DefaultAllowedDomains host (the LLM/CLI provider APIs), plus
// whatever an operator adds to that crew's allowed_domains.
//
// Operators who deliberately want a specific crew unrestricted can re-open it
// to 'free' (or add the domains it needs) after upgrade — see the changelog
// note shipped with this migration.
//
// Scope note: this migration deliberately does NOT rebuild the crews table to
// flip the column DEFAULT to 'restricted'. crews is a wide, actively-evolving
// FK parent (25+ child references); the SQLite recreate-and-swap dance on a
// populated FK parent is the exact hazard migration v89 documented (the DROP
// queues a deferred FK violation that COMMIT refuses), and a full-column
// rebuild risks silently dropping a column a later migration added. The
// backfill above removes the actual exposure (existing 'free' rows); every
// app insert path already writes network_mode explicitly, so the residual
// raw-insert-omitting-the-column case is out of scope here.
const migrationBackfillNetworkModeRestricted = `
UPDATE crews SET network_mode = 'restricted' WHERE network_mode = 'free';
`
