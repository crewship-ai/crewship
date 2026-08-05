# Design — model-scoped credentials

Status: draft · Research 2026-07-28 · Slots into the credential-system work

A credential currently answers *"may this agent talk to Anthropic?"*. It should answer
*"may this agent talk to Anthropic **as `claude-haiku-4-5`**?"*.

---

## 1. Why this is worth doing

**The cost lever is 10×.** After the rate-card fix (`internal/paymaster/pricing.go`), the
spread inside a single provider is:

| Model | $/M input | $/M output |
|---|---|---|
| `claude-fable-5` | 10.00 | 50.00 |
| `claude-opus-5` | 5.00 | 25.00 |
| `claude-sonnet-5` | 3.00 | 15.00 |
| `claude-haiku-4-5` | 1.00 | 5.00 |

One agent reaching for Fable instead of Haiku burns ten agents' worth of budget, and today
**nothing prevents it**. The egress allowlist is domain-scoped
(`crews.allowed_domains`, `internal/egressallow/allowlist.go:168`), so `api.anthropic.com`
is a single permission covering every model behind it.

**It is a security control, not just a cost control.** Model choice determines capability.
A crew scoped to summarisation does not need a frontier reasoning model, and constraining
it shrinks what a compromised or prompt-injected agent can do with the key it was handed.
That is why this belongs on the credential rather than in a billing config.

**The enforcement point already exists.** `reverseProxyToProvider`
(`internal/sidecar/proxy.go`) already terminates the request, reads it, and injects the real
key. Adding a check costs one function call in a code path that is already there.

## 2. Scope — and the honest limit

Enforcement is possible **exactly where credential injection already works**, and blind in
exactly the same place cost accounting is blind. This is not a gap to fix later; it is a
consequence of a deliberate architectural choice and must be documented, not papered over.

| Path | Model policy | Why |
|---|---|---|
| Reverse-proxy, API-key mode — `/v1`, `/openai/v1`, `/gemini` (`proxy.go:592,596,605`) | **Enforceable** | Sidecar terminates the request and can read it |
| CONNECT tunnel, OAuth/subscription mode (`proxy.go:467`) | **Not enforceable** | *"we deliberately do NOT decrypt or inspect the tunnel"* (`proxy.go:492-497`) |

The same TLS-passthrough decision that makes the sidecar *"structurally blind"* for cost
(`internal/chatbridge/cost.go:37-47`) makes it blind for model policy. The UI must therefore
say **"model policy not enforced on this credential"** for OAuth-mode credentials rather
than displaying an allowlist that silently does nothing — a policy that appears active but
isn't is worse than none.

*(NetBird's Agent Network can enforce on every request because it terminates TLS. That is
the one real capability difference found, and adopting it would mean reversing the
no-MITM decision — a separate, larger discussion.)*

## 3. Where the policy lives

**On the credential.** `sidecar.Credential` (`internal/sidecar/credstore.go:28`) already
carries per-credential policy that travels in the boot payload — `LeaseExpiresAt` set that
precedent for #1373. Model scope is the same shape:

```go
type Credential struct {
    ID             string       `json:"id"`
    Provider       ProviderType `json:"provider"`
    Token          string       `json:"token"`
    Priority       int          `json:"priority"`
    LeaseExpiresAt string       `json:"lease_expires_at,omitempty"`

    // AllowedModels restricts which models this credential may be spent on.
    // Empty/absent = unrestricted, preserving every credential's behaviour
    // from before this field existed.
    AllowedModels []string `json:"allowed_models,omitempty"`
}
```

Mirrored in the server-side boot builder at `internal/orchestrator/exec_sidecar.go:686`
(`sidecarCred`), whose comment already warns the tags must match.

Why the credential and not `SidecarNetworkPolicy` (`exec_sidecar.go:663`):

- The key is what gets spent. Two Anthropic keys in one crew can legitimately have different
  scopes — a cheap key for routine work, an unrestricted one for a research crew.
- Delivery already works: crew-wide `CredStore`, boot payload, lease semantics.
- Precedent: LiteLLM's virtual keys carry exactly this (`models: [...]` checked against the
  request's model at the auth layer) — see §7.

If a crew-level narrowing is wanted later, intersect it with the credential's list; the
credential is the ceiling, the crew a further restriction. **Not in v1.**

## 4. Extraction — one rule per provider, and three traps

The naive implementation ("parse JSON, read `model`") fails open on three of these.

| Provider | Endpoint | Rule |
|---|---|---|
| **Anthropic** | `/v1/messages`, `/v1/messages/count_tokens` | JSON body, top-level `model` |
| **Anthropic batches** | `/v1/messages/batches` | **No top-level `model`.** Iterate `requests[]`, read `.params.model` per entry — one batch may mix models |
| **OpenAI** | `/v1/chat/completions` **and** `/v1/responses` | JSON body, top-level `model`. Codex uses Responses by default, Chat Completions when a provider sets `wire_api="chat"` — handle both |
| **Gemini** | `/v1beta/models/<model>:generateContent`, `:streamGenerateContent` | **URL path, not body.** Substring between `/models/` and the next `:` |
| **MiniMax** | `/v1/chat/completions` or `/anthropic/v1/messages` | Follows the OpenAI / Anthropic convention exactly |

**Trap 1 — Gemini has no `model` in the body at all.** A single JSON-based extractor
returns "not found" for every Gemini call. If "not found" means allow, Gemini is entirely
ungated.

**Trap 2 — Anthropic batches nest the model.** A gate checking only `body.model` sees
nothing and, if it fails open, passes a batch that may name any model thousands of times.

**Trap 3 — Anthropic `fallbacks` can serve a different model than the request names.**
With the `server-side-fallback-2026-06-01` / `-2026-07-01` beta, a request naming an allowed
model can be *served* by the fallback model when safety classifiers decline. The request
body still says the allowed model, so **request-side inspection cannot catch this**.

v1 answer: **strip `fallbacks` from the body and the `server-side-fallback-*` beta header**
on any credential that carries an `AllowedModels` list. A scoped credential loses server-side
fallback; that is the correct trade and must be documented. (The alternative — inspecting the
response's `fallback` content blocks and `usage.iterations` — is detection after the fact,
not prevention.)

## 5. Where the check goes

In `reverseProxyToProvider` (`internal/sidecar/proxy.go`), **before `injectCredential`**.

Today the order is: select credential → inject key → cap body → clone → forward. Injecting
first means stamping the real key onto a request that is about to be rejected. It never
leaves the sidecar, but the ordering is wrong and fragile under refactor.

```
select credential
  └─ if cred.AllowedModels is empty  → today's behaviour, unchanged
  └─ else
       extract model (per §4)
         ├─ extraction failed        → 403, DENY   (see §6)
         └─ model not in allowlist   → 403, DENY
       strip fallbacks + beta header
inject credential
forward
```

### Body handling

`http.MaxBytesReader` is already applied (`proxy.go:414`, `maxRequestBodyBytes = 10 MB`).
Reading the body for inspection means replacing it:

```go
buf, err := io.ReadAll(r.Body)          // already capped by MaxBytesReader
r.Body = io.NopCloser(bytes.NewReader(buf))
r.ContentLength = int64(len(buf))       // a chunked inbound leg becomes Content-Length
r.GetBody = func() (io.ReadCloser, error) {
    return io.NopCloser(bytes.NewReader(buf)), nil   // transport retries/redirects
}
```

Two consequences to accept:

- **Latency.** A streaming reverse proxy normally forwards the request body while still
  reading it. A gate that must see `model` before deciding loses that overlap. JSON key
  order is not guaranteed, so peeking the first few KB is not safe.
- **Gemini needs no buffering** — the model is in the path. Skip the buffer entirely there.

> **Possible existing issue, worth checking separately:** the 10 MB cap may already be
> truncating legitimate traffic. Anthropic accepts up to **32 MB** per request (base64 PDFs,
> images), and agents attach documents. If a large attachment currently fails, this is the
> reason. Not caused by this feature, but this feature makes the buffer load-bearing.

## 6. Fail closed

Every ambiguous case denies: JSON unparseable, `model` absent, body over the cap,
unrecognised path shape, provider not in the extraction table.

This matches the codebase's existing posture for security controls — `leaseEpochSentinel`
(`credstore.go:60-65`) reasons that *"for a security control the safe reading of 'I cannot
tell when this expires' is 'it already did'"*. Same logic: *"I cannot tell which model this
is"* reads as *"not an allowed one"*.

Kong's AI gateway takes the same line — a body model that doesn't match the route's pinned
model returns **HTTP 400** rather than being parsed permissively.

Denials must be observable: emit a `network.egress`-style journal event with the attempted
model and the credential id, so an operator can see *why* an agent stalled. A silent 403 is
a support ticket.

## 7. Prior art

| System | Mechanism |
|---|---|
| **LiteLLM Proxy** | Virtual keys carry `models: [...]`; the proxy parses `data["model"]` and checks it at request-validation time, before dispatch. Closest match to this design. |
| **Kong AI Gateway** | Model pinned per route (`config.model.name`); mismatched body model → HTTP 400. Fail-closed posture worth copying. |
| **Portkey** | `override_params` / `drop_params` can force or block a model per target; no pure allowlist primitive. |
| **NetBird Agent Network** | Provider object with optional allowed-models list + per-model pricing, enforced at an L7 proxy that terminates TLS. Beta, June 2026. AGPLv3 (`management/`), so not vendorable. |
| **Cloudflare AI Gateway** | No evidence of body-level model gating; routes by URL path. |

## 8. Work

**Server side**
1. `credentials` table: add `allowed_models` (JSON array, nullable = unrestricted). Migration.
2. Surface it in the credential CRUD API and `crewship credential` CLI.
3. Populate `sidecarCred.AllowedModels` in `exec_sidecar.go:686`.

**Sidecar**
4. `Credential.AllowedModels` in `credstore.go:28`.
5. `internal/sidecar/modelgate` — extraction per §4 plus the allowlist check. Pure functions,
   table-driven tests, no I/O.
6. Wire into `reverseProxyToProvider` before `injectCredential`; buffer per §5; strip
   `fallbacks` + beta header when the credential is scoped.
7. Journal event on denial.

**Tests (first, per project rule)**
8. Extraction: one case per provider, plus a batch with mixed models, plus a Gemini path.
9. Fail-closed: malformed JSON, missing `model`, unknown path, oversized body.
10. Ordering: assert no credential is injected on a denied request.
11. Unrestricted credential: byte-identical behaviour to today.

**Docs**
12. `docs/guides/*.mdx` — the field, and explicitly that it is **not enforced in OAuth mode**.

## 9. Open questions

- **Q1** Match exactly, or allow prefix/glob (`claude-haiku-*`)? Exact is safer; globs are
  friendlier as the catalog rotates. Dated snapshots argue for reusing
  `stripDateSuffix` (`pricing.go`) before comparing.
- **Q2** Deny → 403 to the agent, or a softer failure the CLI can retry against an allowed
  model? A hard 403 surfaces as an opaque CLI error.
- **Q3** Should a scoped credential losing `fallbacks` (§4, trap 3) be a warning at
  credential-creation time rather than a silent behaviour change?
- **Q4** Does the crew-level intersection (§3) ever get built, or is the credential the only
  scope? Only worth it if two crews genuinely need different subsets of one key.

## References

- [Anthropic Messages API](https://platform.claude.com/docs/en/api/messages) — `model` top-level; batches nest under `requests[].params`
- [Anthropic model migration / `fallbacks`](https://platform.claude.com/docs/en/about-claude/models/migration-guide.md) — served model can differ from requested
- [Gemini `generateContent`](https://ai.google.dev/api/generate-content) — `{model=models/*}:generateContent` path grammar
- [Codex config reference](https://learn.chatgpt.com/docs/config-file/config-advanced) — `wire_api` responses vs chat
- [LiteLLM virtual keys](https://docs.litellm.ai/docs/proxy/virtual_keys) · [LiteLLM proxy configs](https://docs.litellm.ai/docs/proxy/configs)
- [Kong `ai-proxy`](https://developer.konghq.com/plugins/ai-proxy/) — fail-closed on model mismatch
- [MiniMax Anthropic-compatible API](https://platform.minimax.io/docs/api-reference/text-anthropic-api)
- NetBird Agent Network — `agent-network/README.md`, blog 2026-06-28 (AGPLv3 server components)

**Unverified:** a `flash_fallback` silent-downgrade mechanism was mentioned in earlier
research on the Gemini CLI but **could not be confirmed** in current Gemini API docs or the
`google-genai` SDK. The response carries `modelVersion` (the model that actually served the
request) — read that if confirmation is needed. Do not build logic assuming `flash_fallback`
exists without verifying first.
