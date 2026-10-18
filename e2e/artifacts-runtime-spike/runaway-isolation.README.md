# Bounded runtime stop/restart lifecycle spike

Issue #3668. This independent cut tests necessary AX-08 stop/restart behavior;
it does not declare full R1 acceptance or enable Firefox/WebKit production Apps.
Existing `supportsPageApplications` remains desktop-Chromium-only. Author code
receives the exact Go `pagebuild.ServeRuntime` response, including its unchanged
`worker-src 'none'`, expected-parent binding, nonce and isolation headers.

The small Go fixture uses a response recorder; the Node TLS fixture replays those
actual headers/body at `https://runtime.artifacts.test` for a trusted local host
`https://studio.crewship.test`. A loopback-only CONNECT tunnel admits just these
two exact hosts. No DNS, hosts-file, certificate trust-store or host package
changes occur. A disposable self-signed test certificate is accepted only in
this fresh credential-free browser context. This does not qualify installed
production TLS, the public Router, authentication, leases or product serving.
TLS tunnel code reuses `ddaebc0ba:e2e/artifact-runtime/fixture.ts`; lifecycle cases
extend the existing Chromium-only `component.spec.ts` stop-loop pattern. No
historical component, renderer or workflow is modified.

Each actual Firefox/WebKit run requires an actual infinite iframe-loop marker,
responsive host, clickable Stop, browser-reported frame/context detachment,
closed host-side old port, actual fresh execution after Restart, fresh port
response and no stale-port response during the bounded observation. A separately
labelled **trusted-host-owned Worker control** requires actual infinite-loop
execution followed by `terminate()` and the browser Worker `close` event.
This Worker control grants no author capability: the author runtime CSP stays
worker-denying. WebKit may route the control's Blob URL; its exact generated URL
is registered before construction, and only that registered URL is fulfilled
once with the constant test payload. Unknown URLs are blocked and fail the run.
Synthetic local delivery does not qualify native WebKit Blob-loader behavior.

Evidence proves browser-context lifecycle and observed host responsiveness.
**It does not prove OS-process isolation, cessation of all CPU consumption,
CPU/memory limits, transactional capability revocation, generation rejection,
receipts, live permission enforcement, App creation or full R1 acceptance.**
A heartbeat is not process evidence. A detached frame alone is not CPU proof.
No server generation/receipt protocol is invented by the test's fresh port.

One focused managed source/compile job:

```sh
crewship-heavy sh -c 'go test ./e2e/artifacts-runtime-spike/stop-fixture -count=1 && go build -o /tmp/my-stop-fixture ./e2e/artifacts-runtime-spike/stop-fixture'
```

The separate optional CI workflow installs locked Firefox/WebKit only on its
ephemeral runner and runs them sequentially. Browser tests have a 25-second
per-browser watchdog, explicit case/cleanup deadlines and an external 110-second
process deadline. Cleanup kills only this runner's BrowserServer children and
its own TLS sockets/temp certificate. Timeout, unknown cleanup, unavailable
browser or missing/malformed evidence fail closed. Failed reports preserve
completed named checks; strict JSON includes source hashes/browser versions and
`runtimeAcceptance:false`. Original primitive-spike evidence remains separate.
Captured reports belong outside tracked source. No shared-host browser install
or authenticated/foreign session is used.
