# Isolated App runtime primitive spike

Issue #3632. This standalone runner exercises the production bootstrap and
MessagePort channel in fresh Firefox and WebKit contexts. It uses only
`about:blank` and opaque sandboxed `srcdoc` for negative cases. A separate
same-origin worker control uses the exact loopback URL
`http://127.0.0.1:65530/artifacts-runtime-spike-control`, fulfilled entirely by
Playwright without a server/socket request. Each controlled fixture registers
its exact generated Blob URL with the harness before constructing its worker.
Only those exact URLs may be fulfilled with the known constant worker payload,
once each;
unknown URLs and all other HTTP requests are aborted. Registration records the
fixture label/frame URL; reports retain registered Blob URLs and fulfilled local worker provenance.
The CSP-denied fixture must never reach Blob request routing. This exception
handles WebKit exposing local Blob loads to routing; it permits no HTTP load.
This is synthetic local route delivery and does not qualify native Blob loader
compatibility.
It does not use the repository's authenticated Playwright global setup, start a
server, access an application session, or change production browser support.

Run from this checkout through the managed resource queue with an external
process deadline:

```sh
crewship-heavy timeout 90s node e2e/artifacts-runtime-spike/sandbox-primitives.mjs
```

When dependencies are already installed in a reviewed shared dependency tree,
pass `--modules /absolute/path/to/node_modules`. The runner reads that tree;
it installs nothing. Optional `--firefox-executable PATH` and
`--webkit-executable PATH` probe explicitly selected existing managed browsers.
Record any mismatch between their revisions and the locked Playwright package;
a mismatched probe does not qualify the locked environment.

JSON on stdout names the exact source commit and SHA256, package/runtime/browser
versions, executable, executed checks and failures. Exit 1 includes unavailable
browsers; unavailable is never a skipped PASS. Preserve run output outside the
tracked repository. Each new browser needs a fresh run; no UA spoofing is used.

The checks cover opaque parent DOM/storage denial, CSP fetch/worker denial,
bootstrap source/origin/shape validation, single-port admission, the existing
closed RPC allowlist, abort on channel close, and suppression of closed-port
requests and late replies. A fresh channel represents a new connection only;
it does not pretend an application generation/capability protocol exists.

Worker observations distinguish a synchronous `SecurityError`, constructor
return, asynchronous error/CSP violation, actual worker message and deadline.
Silence at the deadline fails. A separate credential-free same-origin positive
control permits only `blob:` workers and must execute the same deterministic
payload; the negative fixture retains `worker-src 'none'` and must never execute
it. An additional opaque blob-allowed observation reports engine behavior; an
explicit engine rejection there is not a sandbox bypass or a required worker
capability. Completed named checks survive later control failures.
Individual DOM/storage/worker observations are retained even when an assertion
fails, including worker error details and every intercepted/aborted request URL.
Channel checks run before positive-control qualification, so a failed worker
control cannot hide independently executed channel results. All ten named
checks alone do not override a failed required positive control.
The original remote failure from run `39171348217` remains evidence of
the initial aggregate assertion; it is not retroactively relabelled PASS.

These local primitives do **not** establish cross-site browser process
isolation, runaway-script stop/restart, HTTP response headers, production Go
serving, authenticated human/runtime grants, transaction-time revocation,
receipts after lost ACK, old-generation server rejection, actual App creation,
or release browser support. Current production Apps intentionally permit only
desktop Chromium. New runtime 1.1 capabilities require their own Go authority
and exact browser qualification. No production support gate is widened here.

Initial environment probe, 2026-10-18: Playwright 1.64.1 expected Firefox revision
1555 and WebKit revision 2370, which were absent. Older existing cache probes
could not launch: Firefox 1543 lacked `libgtk-3.so.0` (and logged user-namespace
EPERM); WebKit 2359 lacked `liblcms2.so.2`. No browser checks or browser-version
claims were produced by those failed launches. Fixing host dependencies belongs
to the managed infrastructure workflow; this spike does not install them.
