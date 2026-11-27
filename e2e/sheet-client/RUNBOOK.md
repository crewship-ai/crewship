# Sheet client path (E2E): runbook

The specs in this folder test the client's path through a Sheet in a browser:
a workbook in a new chat, the Sheet beside the chat, editing, saving, downloads,
reloads, local recovery, fidelity_loss, the bar and the composer. They run on a
Linux host with Docker, against a throwaway server, and never touch a shared
environment. Every workbook is synthetic and generated in
`workbooks.ts` from a fixed seed.

| Spec | Covers |
| --- | --- |
| `open-edit-download.spec.ts` | Auto-open of a ~5 MB workbook (sheets, SUMIFS, PivotTable, conditional formats, validations, merged cells) and the time to values and to editing; edit, autosave, new version, Download .xlsx contains the change, Download original = version 1 (sha256) |
| `new-chat-send-now.spec.ts` | A new chat, a workbook, a message sent right away (and once the Sheet shows): the upload names the chat (X-Craft-Chat), the first message carries artifact_refs (browser frame and the server's `/chats/{id}/artifact-refs`), the chat is named after the file, the composer shows the workbook once. No agent runs (no model): "chat run check 100" can only be ruled out with a real agent. Needs `SHEET_E2E_FLAGS=artifact_agent_access_modes SHEET_E2E_AGENTS=1` |

## What you need

- A server binary with the UI embedded, built from the commit under test.
  It is built like this:
  `pnpm install --frozen-lockfile --ignore-scripts && pnpm exec prisma generate && pnpm build && scripts/embed-web-out.sh sync && go build -o crewship ./cmd/crewship`
- The Word and Sheet packages for the commit's pins, in one directory with
  `catalog.txt`. Its content must equal `tools/craft-components/catalog.txt`;
  the runner refuses any other pins.
- Docker. The runner uses `sudo -n docker` when the user is not in the docker group.

## Run

```sh
# in a clean checkout of the commit under test
scripts/ci/sheet-client-e2e.sh <crewship binary> <package dir> <output dir> [playwright args…]
# for example, one spec only:
scripts/ci/sheet-client-e2e.sh ./crewship ./pkg ./out -g composer
```

The container is the Playwright image that the lockfile pins. Inside it, the
runner does these steps:

1. Starts a throwaway server (`crewship start --no-docker`) on 127.0.0.1:18490.
   It gets its own data directory and HOME, random secrets, and no sidecar or
   container runtime, so no agent run can start.
2. Seeds the offline demo. The account is `demo@crewship.ai` and the agent is
   the first one in the seeded list (`SHEET_E2E_AGENT` overrides it).
3. Enables the workspace flags in `SHEET_E2E_FLAGS` (space-separated; the
   result is in `flags.txt`), then installs and enables the two packages.
4. Runs `playwright.sheet-client.config.ts` under xvfb with software WebGL.

`SHEET_E2E_ROWS` sets the size of the large workbook (145 000 data rows by
default, about 5 MB).

## Results

Results go to `<output dir>`:

- `report.json` and `html/`: the Playwright report.
- `playwright/<test>/shots/*.png`: one screenshot for each step (synthetic data only).
- `playwright/<test>/timings.json`: panel, values, editing, take-over and save
  times in ms, and the workbook size. The same numbers appear as test annotations.
- `server.log`, `seed.log`, `applications.txt`: logs of the throwaway server.

A failed test is a finding. Report it to the owner of the code with the spec
line and the screenshots. Do not retry it until it passes.
