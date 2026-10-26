# C1 capacity and recovery regressions

These tests assert the desired behavior. They intentionally FAIL on
`8789ef07fd785f838b0159d8758f2713a94309e4`; failures must not be converted to
expected failures, skipped, or made green by reducing their targets. This is a
repair handoff, not a passing release gate. Do not merge it before the fixes.

Run from an isolated checkout through `crewship-heavy`. All state is disposable
test-fixture state. No test accepts a development URL or contacts an environment.

## Individual tests

Each Go name below is an exact `-run` selector. Run the untagged tests with:

```sh
crewship-heavy go test ./internal/api -run '^TEST_NAME$' -count=1 -timeout=5m
```

| Case | File under `internal/api/` | TEST_NAME | Expected failure on the baseline |
| --- | --- | --- | --- |
| a | `mooring_command_checkpoint_quota_red_test.go` | `TestC1CheckpointRetentionKeepsWritingBeyondFiftyLargeChanges` | Mutation 51/64 refused; 51 retained checkpoints, 66,421,424 bytes. Real SQL, native-port fixture with 1.25 MiB text. |
| e1 | `mooring_command_cache_red_test.go` | `TestC1RetiredDocumentEntriesDoNotExhaustSixteenCacheSlots` | 17th document refused after 16 retired cache entries. |
| e2 | same | `TestC1DefinitiveOpenFailureIsRecoverableAfterRuntimeCleanup` | Recovered startup provider never retried; starts=1. |
| e3 | same | `TestC1RetiredViewsDoNotExhaust1024AdmissionSlots` | HTTP 503 after 1,024 retired views. Actual handler/current authority, native-port fixture. |

For native cases, first build the pinned helper and host using the existing
[engine instructions](README.md). Pass absolute paths to matching artifacts:

```sh
crewship-heavy env \
  CREWSHIP_C1_HOST_BINARY=/absolute/mooring-wordcraft-command-host \
  CREWSHIP_C1_ENGINE_BINARY=/absolute/wordcraft-c1-engine \
  go test ./internal/api -tags wordcraft_c1_integration,wordcraft_c1_durability \
  -run '^TEST_NAME$' -count=1 -timeout=45m
```

Missing binaries FAIL rather than skip. These are actual private native
processes and fixture SQLite, not browser/Saved/real-person acceptance evidence.

| Case | File under `internal/api/` | TEST_NAME | Expected failure on the baseline |
| --- | --- | --- | --- |
| b | `mooring_command_seen_256_red_linux_test.go` | `TestC1Seen256CheckpointRemainsWritableAfterColdRestart` | Revision 256/seen 256 refuses a new intent both before and after cold host/helper restart. Direct trusted test committer isolates the seen bound from SQL retention. |
| c | `mooring_command_durability_prepared_linux_test.go` | `TestCommandNative34ChangesThirtyThreeColdRestarts` | Cold open 33 fails after 32 changes/sessions (32nd restart). Each session persists its new generation. |
| 5,000 | same | `TestCommandNative5000ChangesTenColdRestarts` | Change 54/5,000 in session 1 hits retained-checkpoint quota; subsequent restarts are not reached. |
| g1 | `mooring_command_two_writers_red_linux_test.go` | `TestC1HumanAndConfirmedAgentConcurrentIntentsBothPersist` | Only one of two simultaneous independent revision-zero intents persists. Uses confirmed agent mandate and actual native/SQL, not CLI. |

The 5,000-change target uses one document, Unicode text and 11 sessions (10
cold native-process/factory restarts). After each confirmed write it compares
the entire text; after each restart it verifies text, revision, canonical SQL
checkpoint and every original operation receipt. It never resets the document,
prunes history, changes an op_id/base revision, or replays a mutation. It does
not restart the whole Crewship server/OS. The separate c test crosses 32 opens;
ten restarts alone cannot do so.

For the CLI prerequisite, build the actual CLI from the same checkout:

```sh
crewship-heavy go build -o /absolute/crewship-c1-test ./cmd/crewship
crewship-heavy env \
  CREWSHIP_C1_HOST_BINARY=/absolute/mooring-wordcraft-command-host \
  CREWSHIP_C1_ENGINE_BINARY=/absolute/wordcraft-c1-engine \
  CREWSHIP_C1_CLI_BINARY=/absolute/crewship-c1-test \
  go test ./internal/api -tags wordcraft_c1_integration \
  -run '^TestC1ConfirmedAgentCanOpenWritableDocumentThroughCLI$' -count=1 -timeout=5m
```

Expected: confirmed agent CLI writable-open fails HTTP 403. This calls the real
handler through a local HTTP fixture with the existing post-auth agent context;
it does not test the full authentication middleware. The C1 handler requires
an original human credential. A concurrent human/CLI-agent acceptance scenario
cannot start until a supported agent ingress exists. Do not impersonate the
agent as a human to turn this test green.

Run the two client tests individually:

```sh
crewship-heavy pnpm exec vitest run lib/mooring/wordcraft-settled-256.red.test.ts
crewship-heavy pnpm exec vitest run lib/mooring/wordcraft-409-recovery.red.test.ts
```

Case d expects `canEdit()` true after 256 confirmed changes; baseline returns
false. This is a transport unit test, not an executed iframe crash. Case f
expects automatic read-only outcome lookup after a 409 and recovery upon an
authentic already-committed receipt; baseline makes zero lookups. It asserts
exactly one submit. It does not demand unsafe retries or infer a definitive
rejection from an unknown outcome.
