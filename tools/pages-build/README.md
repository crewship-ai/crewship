# Pages preview profile

An opt-in runtime for custom React dashboards. The Go server stores the
source draft, starts one short-lived Docker compiler, and returns immutable
JavaScript/CSS. There is no per-Page web server or Vite development server.
Draft Git checkpoints, reviewed publication/rollback and declared routine actions
are implemented. Integrated file backup and retention are implemented; browser and operational release gates remain. See [the delivery PRD](../../docs/prd/pages-apps-v1.md).

Users continue to open `/pages/{slug}` on Studio. The runtime hostname is one
installation setting shared by Pages, not a customer-facing Page URL or a domain
per Page. The same Go listener serves it. Automated DNS/TLS provisioning is not
yet implemented. An internal deployment may use internal DNS and trusted TLS,
provided every viewer's browser can reach the runtime. A server-local hosts entry
or a container alias does not configure remote viewers; localhost is their machine.

## Operator setup

Build the trusted profile from this directory (network access is needed during
image installation, never during a Page build):

```sh
docker build -t crewship-pages-build:local tools/pages-build
docker image inspect crewship-pages-build:local --format '{{.Id}}'
```

Set `CREWSHIP_PAGE_BUILD_IMAGE` to the resulting `sha256:…` image ID, or to a
locally available `repository@sha256:…` digest. Mutable tags are rejected and
the worker never pulls. The image and server must come from the same release:
the CLI embeds the same dependency lock and SDK profile.

Configuration:

- `CREWSHIP_PAGE_PROJECTS_PATH`: absolute protected directory outside crew storage;
  do not mount it into agent containers.
- `CREWSHIP_PAGE_BUILD_IMAGE`: pinned, locally installed tools image.
- `CREWSHIP_PAGE_RUNTIME_ORIGIN`: dedicated HTTP(S) origin on a **different site**
  from Studio; e.g. Studio `https://studio.example.com`, runtime
  `https://apps.example.net`. `apps.example.com` or another port is insufficient.
- `CREWSHIP_PAGE_STUDIO_ORIGIN`: the exact public browser Studio origin. Defaults
  to `auth.nextjs_url` / `CREWSHIP_NEXTJS_URL` for compatibility. Set explicitly
  when internal IPC/token sync needs a loopback URL: do not send internal-token
  traffic through the public proxy merely to configure a Page iframe.

### Reviewed-code development demo on the Studio origin

For a controlled development installation, set
`CREWSHIP_PAGE_RUNTIME_DEVELOPMENT_SAME_ORIGIN=true` and set the runtime origin
exactly equal to the public Studio origin. This is off by default and only the
literal environment value `true` enables it. It permits the same origin, not
arbitrary sibling subdomains or ports. The server advertises the setting to the
trusted host UI; Page source code cannot enable it. HTTPS, the exact bootstrap
path, `sandbox="allow-scripts"`, CSP, publication review and all API/RBAC gates
remain enforced. Startup logs a warning.

This mode does **not** guarantee browser process isolation: a stuck application
may freeze the Studio tab. Use only reviewed development code. It is not a
resolution of the Firefox/WebKit stop-loop failures or a production default.
Dev3 uses it for the Operations Lab demo without adding DNS infrastructure.

Point the runtime DNS/proxy alias at the same Crewship Go listener. Preserve the
runtime `Host`, provide HTTPS when Studio uses HTTPS, and route
`/api/v1/pages/runtime/bootstrap` to Go. Expose only this bootstrap path on the
runtime alias; other paths can return 404 at the reverse proxy. It is public
constant HTML with no user data, tokens, sources or artifacts. Only the configured
Studio parent can initialize it. Do not host unrelated applications on this alias.

The browser uses a sandboxed iframe with only `allow-scripts`, a strict CSP and
a dedicated MessagePort. A separate site passed the Chromium infinite-loop
isolation test where `srcdoc` froze the Studio tab. This is not a portable
hard CPU/memory quota: Firefox/WebKit stop-control tests currently fail; see the
[handoff](../../docs/prd/pages-apps-handoff.md) before claiming browser support.
CSP blocks fetch, external subresources and workers, but is not a complete
anti-exfiltration boundary (an iframe can navigate itself). Opening custom code
still requires trusted/reviewed source; never pass secrets as panel data.

The Go process needs Git (CLI plumbing, no checkout/hooks/remotes) for source
checkpoints and access to Docker for builds. Published artifacts remain readable
when the build worker is disabled. Each build runs as UID 1001 with no host
mounts, network, capabilities or secrets, read-only rootfs, 1 CPU, 1 GiB RAM,
128 PIDs, bounded tmpfs/output and a 120-second in-container deadline. One build
runs per instance; a concurrent request returns 429, with no waiting queue.
Restart marks unfinished DB jobs `interrupted`; the old container's deadline
still applies. Installation with Docker privileges remains an operator trust
boundary, not a multi-tenant microVM isolation guarantee.

Back up SQLite **and the complete projects directory including `artifacts/`**
with writes paused. Current DB backup includes metadata only. Automatic file
backup/restore and retention remain production release gates. Quotas per workspace:
256 source snapshots / 128 MiB, Git archives / 128 MiB, 256 artifacts / 128 MiB
and 512 build records. Each Page allows at most 512 source revisions and 512
publications.
There is no automatic GC yet; quota exhaustion is explicit, not silent deletion.

## Agent / editor workflow

For an existing Page `mysql-health`, using an authorized Crewship CLI session:

```sh
crewship page project init --dir mysql-app
# Edit mysql-app/src/main.tsx and mysql-app/src/style.css.
crewship page project pack mysql-app > source.yaml
crewship page project set mysql-health --file source.yaml --revision 0
crewship page project build mysql-health --revision 1
crewship page project preview mysql-health
crewship page export mysql-health > mysql-health.yaml
crewship page import mysql-health.yaml --slug mysql-health-copy
```

Use the returned revision for subsequent saves/builds. A stale revision returns
409. The Studio Pages toolbar has **App preview**, **Build preview**, status/logs
and **Stop preview** for users with edit authority. **Source history** can restore
an earlier Git checkpoint as a new draft, with revision CAS; it never republishes.
**Check application** verifies Git/source/artifact integrity and current bindings.
It does not prove browser behavior or code trust. Owners/admins can publish after
review; Page write permission alone does not grant publication authority.

```sh
crewship page project history mysql-health
crewship page project get mysql-health --revision 1
crewship page project check mysql-health --build BUILD_ID --revision 1
crewship page project publish mysql-health --build BUILD_ID --revision 1 --expected-publication 0 --reviewed-code
crewship page project application mysql-health
crewship page action mysql-health/health refresh --publication 1 --idempotency-key my-check
crewship page project action-status mysql-health PENDING_ID
crewship page project rollback mysql-health --publication 1 --expected-publication 2 --reviewed-code
```

Use the actual returned IDs/versions. Exact publication retries return the original
receipt without moving a later publication. Rollback publishes a new release from
an old artifact/definition; it neither resets the source draft nor reverses routine
effects. An already-open reader explicitly loads a new version so an update does
not silently reset their form. Changing the underlying Page declaration outside
publication fences action execution until the declaration/publication agree.
 The UI imports v1 JSON and
v2 YAML/JSON; exports with source use a single `.bundle.yaml`. Import creates an
inert draft and does not run code or routines.

The supported profile pins React 19.2.8, ReactDOM 19.2.8, TypeScript 7.0.2 and
Vite 8.2.2. Keep the supplied dependency maps and lockfile unchanged. Full React,
TypeScript and custom CSS are supported; arbitrary npm dependencies, build
plugins, package scripts and custom Vite/PostCSS/Tailwind config are not.
The entry is `src/main.tsx`, mounted into `#root`. The trusted runtime owns the
HTML shell; source `index.html` is portable source, not executed HTML. Import
assets from source so Vite can inline them. There is no public asset directory,
SSR, dynamic server, direct database connection or unrestricted Crewship API.

```tsx
import { usePageSnapshot, usePanel } from '@crewship/pages'

function DatabaseHealth() {
  const panel = usePanel('mysql-health')
  return <pre>{JSON.stringify(panel?.data ?? null, null, 2)}</pre>
}
```

The read SDK exposes `usePageSnapshot`, `usePanel`, `getSnapshot`, and `subscribe`.
Snapshots contain only the panels visible to the current Studio user; sealed
panels, authoring actions and identity fields are excluded. Data retains the
producer timestamp. Delivery is bounded to 1 MiB, at most 10 messages/second,
one unacknowledged message and one coalesced latest snapshot. Oversized data
stops the preview visibly. The SDK can acknowledge snapshots and call only two host methods:
`runAction(panelId, actionId, inputs, { idempotencyKey })` and
`getActionStatus(pendingId)`. No arbitrary HTTP/API proxy is exposed. Preview
rejects actions. Published calls use a Studio-owned confirmation dialog and the
existing RBAC, routine governance and queue. One RPC can be pending per frame,
requests are capped at 32 KiB and spaced at least 250 ms apart. Closing the frame
cancels a pending confirmation; it cannot undo an already-submitted request.
Receipts and debounce/idempotency are scoped to the viewer and publication.
The SDK never retries writes automatically. A timeout is an unknown outcome,
not proof that execution stopped. Queue status is separate from actual run status.
The source Git commit does not pin the backend routine's implementation: the
existing routine executor still executes its current definition.

See [the MySQL/Ansible pilots](../../examples/pages-apps/README.md) for source,
producer and routine examples. The Page's normal producer/realtime path remains in use. Published application
checks use ETag to avoid retransmitting unchanged code; the server rechecks
permission before every 304, and no public artifact cache bypasses that check.

## Reproduce verification

```sh
CREWSHIP_TEST_PAGE_BUILD_IMAGE="$(docker image inspect crewship-pages-build:local --format '{{.Id}}')" \
  CREWSHIP_TEST_PAGE_ARTIFACT_OUT=/tmp/pages-artifact.json \
  go test ./internal/pagebuild -run TestDockerPreviewBuildIntegration -count=1 -v
node e2e/pages-preview-smoke.mjs /tmp/pages-artifact.json
```

The browser check needs the repository Playwright Chromium installation. It
starts temporary loopback servers (including the actual Go runtime handler),
checks React rendering, SDK updates, denied parent DOM/storage/fetch access and
Studio responsiveness while the child loops. It does not touch a live instance.

## Authoring from chat

The existing `crewship-routines` MCP server advertises `page_project` after the
updated sidecar is installed. Create the Page with `save_page`, then use
`init → read → save → build → status → check`. `read` includes the exact SDK
source embedded from this profile so agents do not have to guess its methods.
The tool edits drafts under the acting agent's owner-crew/policy gates; reviewed
publication remains a separate user action. See the [tool workflow](../../docs/cli/page.mdx#agent-chat-custom-page-sources).

Exercise real agent and CLI transports against isolated fixtures:

```sh
CREWSHIP_TEST_PAGE_BUILD_IMAGE=sha256:YOUR_LOCAL_IMAGE_ID \
  go test ./internal/sidecar -run TestPageProjectMCPDockerIntegration -count=1 -v
PAGES_TEST_BUILD_IMAGE=sha256:YOUR_LOCAL_IMAGE_ID \
  go test ./cmd/crewship -run TestAcceptance_PageProjectGitHistoryRestore -count=1 -v
```

The CLI test intentionally uses `PAGES_TEST_BUILD_IMAGE`: that package scrubs
ambient `CREWSHIP_*` variables before tests to protect the running installation.
Both tests require an immutable image ID/digest, not a mutable tag.

## Lifecycle storage and history

Workspace backups include referenced source, Git objects and compiled artifacts
inside the normal encrypted backup. Restore validates a temporary namespace
before changing the target database; missing objects are an error. Keep
`page_projects_path` configured during backup and restore, including on a host
where builds are disabled. Backup/restore and retention coordinate with workspace
file leases. Do not copy a live SQLite file and assume that includes Page code.

Hourly maintenance retains the newest 64 source revisions, 64 completed builds
and 32 publications per Page, plus current draft/live and running-build roots.
It removes unreferenced sources, artifacts and Git checkpoints. Git ancestors
remain immutable and count toward the 128 MiB workspace Git quota; retention is
not unlimited history. Backup before deliberate long-term archival is required.

The SDK includes `getPanelHistory(panelId, {limit, before})`, bounded to 20 items
and 1 MiB per response, with current viewer access and publication checks. It is
available only in published applications, like the action RPC surface.


## Workspace colors and motion

Studio Settings → General → Pages appearance stores six optional `#RRGGBB`
colors through the existing owner/admin workspace update API (`pages_theme`).
The host forwards only normalized palette tokens to the opaque Page frame.
New builds of this SDK apply `--crewship-page-accent`, `-background`, `-surface`,
`-text`, `-muted`, `-border`, and derived `--crewship-page-on-accent` on the root.
`usePageTheme()` exposes the same palette to React. The starter uses these CSS
variables with fallbacks; arbitrary custom layouts remain supported.

Colors are shared per workspace, not saved as authority in a Page export.
`workspace.updated`, reconnect and focus refresh the shared host settings; palette
changes need no application rebuild once the app uses this SDK and its tokens.
Existing published artifacts keep their old SDK until rebuilt and reviewed.
Agent `page_project read` returns `pages_theme` beside the SDK source so the agent
can use the established company palette. Animation belongs in app CSS and must
respect `prefers-reduced-motion`; Operations Lab demonstrates bounded entrance
and button feedback without permanent animation or additional API polling.
