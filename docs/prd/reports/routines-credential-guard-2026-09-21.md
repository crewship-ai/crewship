# Routines credential guard — audit S1

Issue #2639. An explicit HTTP credential reference previously tolerated a
missing resolver, empty result or resolver error and still sent the request.
The required-credential preflight also treated a vault probe failure as proof
of availability. These were documented availability choices; the audit found
they could downgrade a credential-bound write to an anonymous request.

## Change and compatibility

An explicit HTTP `credential_ref` now requires successful, nonempty resolution
before the request is sent. Public calls omit the reference. Resolution details
are not included in the stored step error. Existing authenticated calls and
HTTP steps with no reference retain their behavior.

A declared `credentials_required` gate returns HTTP 503 on unknown availability
(no DB or failed probe), and still returns 422 with `missing_credentials` for
confirmed absence. In-process child calls and scheduled dispatch use the same
preflight refusal. The runtime HTTP guard also covers vault failure after the
initial check. Secret templates outside `credential_ref` retain their existing
optional/missing-value behavior; declaring requirements remains important there.

This is an intentional behavior change, documented in the changelog and guide.
No schema migration or credential data rewrite is needed.

## Evidence

- Red: the new no-resolver / empty-value / resolver-error tests each failed
  against the old runtime because the request was allowed.
- Green: HTTP step and factory credential-injection tests passed. The real
  factory/vault regression confirms an unmatched credential prevents a request.
- HTTP 503 and child preflight refusal verified for nil and closed databases;
  the no-requirement fast path remains allowed. Existing required-credential
  tests passed, including present credentials and Anthropic fallback.
- Full pipeline package passed in 29.537 s; `go vet ./...` and strict docs
  inventory passed. OpenAPI regenerated without a diff.
- Full repository tests and independent review remain tracked in the PR.
- Local logs: `/srv/crewship/backups/crewship_1/routines-credentials-20260921/`.

This is a source/test validation, not a fresh live multi-account security audit
of DEV1. It does not establish per-script isolation inside a crew container.
