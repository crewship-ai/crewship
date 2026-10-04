# Runbooks — operational procedures

**Purpose.** Repeatable procedures an operator follows to run, verify,
publish or recover something. A runbook is written against the machinery as
it exists; when the machinery changes, the runbook changes with it in the
same PR.

**Rule for a new file.** Imperative, stepwise, and honest about what is
verified versus assumed. Name the exact gates, artifact identities and
credential requirements an operator needs. Session narratives ("what I did
today on dev2") are not runbooks — those belong in
[private working context](../development/private-context.md). Generated
reports are ignored build output, not an operational procedure.

[Documentation map](../README.md) · [Contributor guides](../development/README.md)

## Contents

- [Staged runtime start](staged-runtime-start.md): activate, recover and explicitly roll back the Docker egress pilot while preserving data.

- [`ci-cd-implementation-2026-09-11.md`](ci-cd-implementation-2026-09-11.md) —
  the CI/CD publication runbook: CI Result composition, image and binary
  publication, exact-SHA gates, nightly/smoke verification, signing and
  release identities. Referenced from
  [CONTRIBUTING.md](../../CONTRIBUTING.md) and
  [RELEASING.md](../../RELEASING.md).
