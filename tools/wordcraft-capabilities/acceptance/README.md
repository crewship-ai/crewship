# Command denial acceptance: CLI and MCP

This suite is written ahead of product C1 integration. It sends actual CLI and
MCP requests to the same explicitly selected disposable test authority. It never
starts, deploys, resets or restarts a server. Do not use an existing user document.
Missing inputs, authentication, routes, tools or protocol support fail the test;
there are no skipped or expected-failure cases.

```sh
crewship-heavy env \
  WORDCRAFT_CE_BASE_URL="$TEST_SERVER_URL" \
  WORDCRAFT_CE_PAGE="$OWNED_TEST_PAGE" \
  WORDCRAFT_CE_CLI="$TEST_CLI_BINARY" \
  WORDCRAFT_CE_TOKEN="$TEST_TOKEN" \
  WORDCRAFT_CE_MCP_URL="$TEST_MCP_URL" \
  WORDCRAFT_CE_MCP_TOOL="$TEST_COMMAND_TOOL" \
  pnpm exec vitest run --config tools/wordcraft-capabilities/acceptance/vitest.config.ts
```

Keep credentials out of logs and shell history. The CLI binary and MCP tool must
be production entrypoints, not stub drivers that synthesize denial receipts. The
URL must name the intended fixture; MCP must have the same authority. The test
performs authenticated control reads through HTTP and CLI, validates that both
observe the same WordCraft state, initializes MCP and discovers the command tool
before attempting any negative command.

The test fixture must permit ordinary writing for its principal while leaving
`format.color` unqualified. It must expose `file.open` as permanently forbidden.
Both commands are disabled in the initial UI catalog. No local file is opened by
the test: `forbidden-acceptance.docx` is a synthetic forbidden parameter.

Integration contract (proposed; it is not an existing server guarantee):

- `GET /api/v1/pages/{page}/mooring` returns the WordCraft `app_id` and confirmed
  `state: {rev, generation, snapshot}`. The CLI `mooring read` returns the same.
- CLI `mooring command {page} --file -` receives the current request envelope
  `{mutation_id, base_rev, generation, command, arguments}`. `--server` selects
  the fixture. Failed commands preserve HTTP `403` and the machine denial code
  in CLI diagnostics; missing structured error support is an integration gap.
- The discovered MCP command tool accepts `{page, request}` with the same
  request envelope. `tools/call` returns a tool-level `isError: true` and
  `structuredContent: {status: 403, code, command}`. HTTP transport success alone
  is not command success. JSON or SSE JSON-RPC responses are supported.
- `format.color` returns `command_not_qualified`; `file.open` returns
  `command_forbidden`. Generic auth errors, unknown commands, invalid packets,
  timeouts, 404s and JSON-RPC method errors do not count as catalog rejection.
- Confirmed revision, generation and snapshot remain identical after each call.

If final C1 transport uses a different versioned envelope, update this adapter
against the actual ingress and keep the same assertions. Do not turn missing
integration into a skipped or passing test. These checks are an independent
explicit acceptance command; they do not claim normal offline CI executed a
live server scenario or qualify a feature group.
