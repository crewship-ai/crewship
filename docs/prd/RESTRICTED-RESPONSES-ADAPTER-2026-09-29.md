# Restricted Responses adapter — 2026-09-29

Continuation of #2711, after merged #2721 (`272b43f58`). This is a provider
transport boundary, not acceptance of restricted application model execution.

## Contract

`HTTPGrant.Responses` explicitly selects a stateless, text-only OpenAI Responses
operation under `brokered-http-v2`. The trusted authority fixes the model and
per-request output-token ceiling (1–32768), credential revision/account and exact
`https://api.openai.com/v1/responses` POST endpoint. One such grant per attempt
avoids ambiguous SDK routing. Model/policy changes fence the running attempt;
delegation can only lower the token ceiling, never remove the policy.

The UID-1002 broker exposes `POST /v1/responses` only when that grant exists.
Both this alias and `/v1/operations/{id}` pass the same host-side body gate.
The agent receives an attempt-local token; the real provider bearer remains on
the host. Caller headers cannot select another provider account or project.
No models, conversation, compact, upload, WebSocket or credential endpoint is
exposed. Older runner binaries reject the new configuration field and fail closed.

The body must contain the exact model, `store:false`, `stream:true`, and either
literal text or self-contained text messages. Optional instructions are text.
The broker supplies the ceiling when `max_output_tokens` is absent; an explicit
larger limit is denied. Unknown fields, duplicate keys (also escaped equivalents),
invalid UTF-8, case variants and excessive nesting are denied. Canonical JSON
is forwarded only after validation and a second size check, before credential
resolution or DNS. Input/response bytes, stream duration and revocation remain
bounded by the existing broker controls.

This deliberately rejects remote tools, files/images, item references,
`previous_response_id`, conversations, prompt templates, encrypted reasoning,
background execution and caller-supplied cache keys. Those features require
resource ownership/provenance contracts before they can be enabled. This is
not a native Codex tool-loop adapter or a ChatGPT subscription-login adapter.

Design reference: the official [Responses create reference](https://developers.openai.com/api/reference/cli/resources/responses/methods/create)
documents conversation attachment, previous-response chaining, prompt templates,
input items and tools. A fixed HTTPS destination does not constrain those
resource references; the closed body schema is a separate authorization gate.

## Verification

Tests cover the positive text path, schema rejection, delegation and fingerprint
changes, and a real HTTPS exchange. The owned Docker fixture checks two clients
of one agent through the SDK route, credential secrecy from UID 1001 and `/proc`,
UID-1002 broker directory permissions, invalid model/resource/path/token requests,
and revocation of one client while the other continues. Upstream is synthetic;
no production credential or paid model request is used.

Record execution results with their source revision in private context; the
scenario description alone does not establish application acceptance.

## Still required before application enablement

The application authority currently prepares offline commands. It still needs
an immutable provider binding selected from the concrete agent's current grants,
credential refresh/account fencing, scoped prompt/history/recall selection,
budget reservation and usage accounting, output persistence/audience checks and
real chat/CLI/routine dispatch. This adapter's token ceiling is per request,
not a total-spend quota. SSE framing is transport verification, not semantic
validation that a provider emitted a completed response.

No shared crew fallback, new public route, Settings switch, migration or direct
credential delivery is introduced. Persistent disk quotas and host-reboot
acceptance remain separate outstanding release gates.
