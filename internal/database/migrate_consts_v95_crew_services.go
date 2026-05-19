package database

// migrationAddCrewServices (v95) adds a JSON column that stores
// sidecar service declarations for a crew — Redis, Postgres, MySQL,
// MongoDB, etc. The shape mirrors what `crewship apply` accepts
// under `spec.services` and what the docker provider reads at
// EnsureCrewRuntime time.
//
// Why a single JSON column instead of a child table:
//
//   - Services are a closed list per crew, never queried by name
//     across crews, and read-after-write atomically together with
//     the rest of the crew's provisioning config. A normalised
//     child table would only add join cost.
//   - The same shape is already serialised by manifest apply and
//     ingested by the docker provider, so the column doubles as
//     the on-disk artefact for the manifest round-trip.
//   - Migration to a child table later remains cheap (one-time
//     unnest into rows) if a future feature (e.g. workspace-wide
//     "shared Redis" reuse) needs cross-crew indexing.
//
// Schema impact: additive only. The column is NULLABLE and treated
// as "no services declared" when NULL or empty-string, so every
// existing crew keeps its current behaviour. Downgrade (SQLite <
// 3.35 can't DROP COLUMN) is one-way; v95 is harmless when
// ignored.
//
// Validation: the application layer parses services_json with
// internal/manifest.Service shape at create/update time and
// rejects malformed bodies before the row lands. The DB column
// itself stores opaque JSON so a future schema iteration doesn't
// require a migration.
const migrationAddCrewServices = `
ALTER TABLE crews
    ADD COLUMN services_json TEXT;
`
