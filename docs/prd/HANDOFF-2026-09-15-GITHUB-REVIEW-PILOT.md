# GitHub review pilot — #2569

Follow-up to merged #2558. Goal: a real GitHub PR delivery triggers a Crewship
routine, an agent reviews the diff, and the result appears on that PR. This is
an isolated pilot; CodeRabbit and existing merge protections remain in place.

## Implemented preparation

`examples/github-review-pilot/` contains a routine builder, deterministic guard,
tests and setup instructions. Configuration binds one repository ID/name, one
PR and a base branch. The generated routine has 12 steps: event validation,
current PR/review lookup, duplicate and revision checks, immutable comparison,
bounded diff preparation, agent review, fresh PR/review lookup, publication
validation, COMMENT publication, and verification of the actual returned review.
Successful publication emits an explicit SUCCEEDED checkpoint; an existing
review emits NO_CHANGE. The agent does not choose the destination or receive the
GitHub API token. Concurrent publication is not atomic; see the example's limits.

On dev2 a dedicated MINIMAL agent `github-review-pilot` exists in crew `test`.
Its Anthropic credential was granted through a two-hour lease. The complete
routine is saved as a **draft**, awaiting a dedicated GitHub credential. The
normal save gate correctly refused its missing CLI_TOKEN with HTTP 422.

## Real observations, and the substitutions

- A private sandbox repository and draft PR with an intentional `sum(prices)` →
  `prices[0]` regression were created. No PR code was executed.
- The real Claude agent independently identified the wrong total and empty-list
  IndexError. Its direct smoke run exited 0.
- **Direct GitHub → dev2 failed.** GitHub's ping and PR delivery records report
  502, `failed to connect to host`. Public DNS (Google DNS-over-HTTPS, not just
  this machine's resolver) returns `192.168.1.201` for
  `crewship-dev2.unifylab.cz`; there is no AAAA answer. Earlier HTTPS calls from
  this server did not demonstrate public reachability.
- A temporary Cloudflare Quick Tunnel forwarded only POST `/github-pilot` to
  this one endpoint through a loopback relay. The relay required the endpoint's
  GitHub HMAC; unsigned requests returned 401 and other POST paths 404. No
  authenticated management API was forwarded. The downloaded cloudflared
  2026.9.1 binary matched its official release SHA-256.
- Changing the GitHub hook URL without resupplying its secret produced an
  unsigned delivery, rejected with 401 by the relay. Explicitly configuring the
  signing secret again produced a real GitHub delivery with HTTP 202.
- The ingress probe uses a GitHub comparison captured by the **operator** for
  one immutable PR snapshot. It does not fetch the private comparison via the
  routine's still-missing credential. Its guards compare the signed event to
  that captured snapshot before allowing the agent to run.
- Probe v1 completed but recorded outcome FAILED, `no outcome reported`. This
  was a definition error, not a green end-to-end result. Probe v2 explicitly
  records that it generated a draft, not that it published anything.
- Probe v2's real GitHub-triggered run completed with SUCCEEDED:
  `run_cmu36v8r10009543fa948` (13.435s). The agent found both intended defects.
- The **operator validation harness** checked the current PR again and used
  the existing host `gh` session to publish a COMMENT review. Readback verified
  review `5215986734`, publisher, and commit
  `a11c520ab793114ecc1a24b657e5f8895c1de26d`. No GitHub host credential was copied
  into Crewship or handed to the agent. The review body states this provenance.
- Real GitHub redelivery returned 202 DEDUPED with the same run ID and receipt
  `cmu36v8r1005b7a0d05ca`. There remained two historical probe runs (v1 and v2),
  one successful v2 run, and exactly one published pilot review.

This proves real ingress, agent execution and a real published review with
operator-assisted fetch/publication. It **does not prove unattended operation of
the complete generated routine**.

## Remaining prerequisites and tests

1. A durable publicly reachable receiving address; the private dev2 hostname
   cannot receive GitHub deliveries directly. The temporary tunnel is a test,
   not a production deployment or DNS fix.
2. A repository-limited GitHub credential in the author crew's vault. The host's
   broad GitHub credential is deliberately not persisted in the application.
3. Publish the reviewed draft, install its signed hook, and run the complete
   automatic workflow with actual credential resolution and no operator bridge.
4. Repeat the real stale-head-during-review test and verify publication refusal.
   Stale head/base, replay, spoofed marker, truncated diff, destination binding
   and successful-publication outcome are currently covered by deterministic
   tests; the live stale-head scenario has not been performed.

The temporary GitHub hook and Crewship endpoint were disabled after the test;
the tunnel and relay were stopped. The draft PR and review remain inspectable.
Private operational artifacts are under `/tmp/crewship-github-pilot/`; files
whose names include `private` contain webhook material and must not be committed
or copied into public reports. Safe evidence is archived separately under
`/srv/crewship/backups/crewship_2/github-review-pilot-2026-09-15/`.

Validation at this entry: 10 Python guard tests passed; the generated 12-step
routine passes the real CLI validator; real checks are enumerated above. The
full Go/vet verification was started separately and is still running at this
entry; no result is inferred from the unchanged application source.
