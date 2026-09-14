# spike-river — River over SQLite, evaluated

Harness for [`docs/prd/SPIKE-RIVER-SQLITE-1-0.md`](../../docs/prd/SPIKE-RIVER-SQLITE-1-0.md).
The verdict it produced is [`docs/prd/ADR-QUEUE-RIVER-SQLITE-2026-09-10.md`](../../docs/prd/ADR-QUEUE-RIVER-SQLITE-2026-09-10.md):
**reject for 1.0**.

It is a **separate Go module** on purpose. River is not in the main `go.mod`, so
`go build ./...`, `go vet ./...` and the image build never see it, and rejecting the
library did not leave a dependency behind. Run it from this directory.

```bash
cd tools/spike-river
go run . -scenario all -pool 5 -sync FULL -ops 200 -workers 32 -hold 5s \
   -json ../../docs/prd/reports/spike-river-sqlite-results.json
```

Every scenario opens a fresh SQLite file under `os.MkdirTemp` using the DSN copied from
`internal/database/database.go`. Nothing touches a live database, and the worker is a mock
with no credentials and no external effects.

| `-scenario` | Question |
|---|---|
| `tx` | Do a delivery row and a River enqueue in one `*sql.Tx` commit and roll back together, and can a worker see the job before the commit? |
| `dup` | Do N concurrent identical deliveries collapse to one work item with one stable receipt? |
| `fencing` | Does River's own completion predicate reject a completion from a superseded attempt? (It does not — this is the finding that decided the ADR.) |
| `pool` | Does a second connection taken inside an open transaction starve at River's recommended `SetMaxOpenConns(1)`? |
| `contention` | Under a deliberately held write lock: does a context deadline bound the acceptance path, and can it produce a false `202`? |
| `durability` | What does `synchronous=FULL` cost per commit versus `NORMAL`? |

`tx`, `dup`, `fencing` and `pool` are the correctness bar the in-house queue has to clear
as well, which is why the harness is kept rather than deleted with the rejected dependency.

Exit code is non-zero if any scenario fails. The numbers in the ADR were taken on
crewship-dev (12 vCPU, ext4, non-rotational); re-record the environment if you re-run
them elsewhere, and note that this harness does not reproduce the server's
`WithManagedWAL()` + dedicated checkpointer.
