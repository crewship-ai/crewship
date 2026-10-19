# Private empty Board initialization

A human can initialize an existing empty, private, human-owned Page as a
Board, then reopen its confirmed empty scene. This boundary persists the
Board attachment and initialization receipt in SQLite. Drawing writes,
agent requests, collaborative editing, pins and Dashboard data are separate
features. The response reports `can_write: false` and `write_scope: unavailable`.

Board support defaults to disabled. Set `CREWSHIP_BOARD_ENABLED=true` (or
`board_enabled: true` in configuration) to register its routes. Disabled routes
return 404; enabling the boundary does not enable a drawing editor.

`InitBoard` serves `POST /api/v1/pages/{slug}/board` with exactly
`{"mutation_id":"caller-stable-id"}`. The Page custodian needs a live human
credential, trusted membership and `pages:write` CLI scope. Repeating the same
request returns the same confirmed scene; a different initialization ID or
actor conflicts. The attachment, receipt and content-type checks share one
transaction with a SQLite writer reservation. Existing content is refused.

`GetBoard` serves `GET /api/v1/pages/{slug}/board`. The custodian and a trusted
workspace OWNER can read; another human's OWNER access must be audited before
content is released. Other principals receive 404. Both operations recheck
exact credential validity and live membership; grants and folders do not
expand this private audience. Responses use `Cache-Control: no-store`.

The response contains immutable `page_id`, pinned `format`, `seq: 0`, empty
`records`, `placements` and `placement_tombstones`. An absent Board returns
404. A nonzero sequence or unsupported profile returns 409, preserving stored
state instead of reporting a successful empty scene. A `@PageID` reference
never follows a deleted Page's reused slug.

The CLI commands are `crewship page board init @PageID --mutation-id ID`
and `crewship page board show <slug-or-@id>`. The caller retains the original ID
when a response is lost. Initialization refuses slugs, so a deleted Page
replaced under the same slug cannot receive a retry. Endpoint registration requires `pages:write` for the
initialization route; ordinary Page creation retains its existing scope.

The focused tests drive the real handlers against SQLite, exact credentials,
audited OWNER reads, denied authority, durable retries and type/custody guards.
`TestBoardHumanActualCLI` builds the actual CLI and exercises both operations
over HTTP with real credential middleware, using the production Router with its explicit Board option and migrated database.
The backup regressions exercise encrypted archive creation and same-workspace
and fork restoration, including original retry receipts and custody.
These checks do not qualify the Board editor or full Board acceptance.
