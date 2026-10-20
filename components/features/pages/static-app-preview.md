# Static App preview boundary

`StaticAppPreview` presents a confirmed `static-html-v1` publication supplied
by the existing Page authority. Its input contains workspace ID, immutable Page
ID, positive revision, profile and HTML. It performs no fetch, publication,
mutation, grant or runtime broker operation. It does not replace the existing
interactive Pages Apps profile.

Mount it after an authorized read. On access loss or an unverified read, pass
`null` immediately or unmount it. Remount with a new key after a fresh authorized
read, including a regrant or Page change. A slug is not identity. The caller
must fence stale responses and revalidate live authorization; the component
cannot establish server authority from HTML or a client prop.

The opened revision stays pinned when another publication arrives. The reader
explicitly loads the offered revision; its frame is replaced and temporary
preview state is discarded. Null authority, identity mismatch or an unsupported
profile removes the old frame synchronously. Revoked content cannot reappear
from the component's retained publication after a regrant.

Original HTML is never inserted into the host DOM. A per-call DOMPurify instance
removes active elements, navigation and external resource attributes. CSS is
restricted; only bounded canonical PNG/JPEG data images pass container and native
decode validation. The result has a restrictive CSP and a no-referrer policy,
inside an iframe with an empty sandbox. Author JavaScript is always disabled.
Preparation failures expose a text status. Browser runtime 1.1 qualification is
separate from this static boundary.

Provenance: `lib/pages/artifact-static.ts`, `artifact-raster.ts` and their
existing unit fixtures were extracted unchanged from the human static HTML
flow in commit `22022bbfb1b4199cb1f56fec050e6e3932edf6cf` (PR #3267).

Status: not mounted. No endpoint returns publication HTML today (Page content
descriptors report `html`/`static-html-v1` only), so nothing in `app/` renders
this component and it stays dark behind `NEXT_PUBLIC_STATIC_APP_PREVIEW=true`
(`staticAppPreview()` in `lib/feature-flags.ts`).

Privacy: a private App the viewer may not read answers 404. The caller maps
403/404 to `publication={null}`; the component's message is identical for both
and names nothing about the Page.

Unit vitest runs under happy-dom, whose parser is not spec-faithful for DOMPurify,
so DOM-level sanitizer vectors run in real browsers in the harness below.

Focused checks:

```sh
crewship-heavy pnpm exec vitest run components/features/pages/static-app-preview.test.tsx lib/pages/__tests__/artifact-static.test.ts lib/pages/__tests__/artifact-raster.test.ts lib/pages/__tests__/artifact-static-vectors.test.ts
crewship-heavy node e2e/static-app-preview.browser.mjs
```

The browser harness bundles the actual component and sanitizer, serves them on
an isolated OS-selected loopback port, and launches Chromium, Firefox and WebKit.
Missing browser binaries fail the command. It checks the static part of
T-AX-05 (execution, host isolation, requests and navigation), and the component
part of T-AX-04 (explicit revision load and removal of inaccessible content).
These checks are component evidence. They do not prove Page API authorization,
durable publication, card integration, saved source editing or runtime 1.1.
