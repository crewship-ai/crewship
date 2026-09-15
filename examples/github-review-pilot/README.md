# GitHub review pilot

This example prepares a **single repository / single PR** routine. It reads an
immutable GitHub comparison, asks a read-only agent for findings, checks the PR
again, and posts a `COMMENT` review tied to the reviewed commit. It neither
approves nor merges a PR, and does not replace the repository's review gate.

## Prerequisites

- A dedicated test repository and PR, with no fork or untrusted execution.
- A publicly reachable HTTPS webhook endpoint. A domain resolving to an RFC1918
  address is not reachable by GitHub even when requests from your own server work.
- A dedicated `MINIMAL` agent in the author crew, with its model credential
  bound. Do not bind the GitHub credential to that agent. Built-in tool profiles
  do not disable independently configured MCP tools; use a dedicated agent with
  no external action integrations. The review prompt requests no tool calls.
- A repository-limited GitHub credential in that crew's vault, type `CLI_TOKEN`.
  HTTP steps resolve credentials by **type**, not provider or name: dedicate the
  crew/type slot and verify that it selects the intended credential. Do not use
  a personal organization-admin token for this example.
- The GitHub token needs repository **Pull requests: write** for reading and
  publishing reviews and **Contents: read** for the immutable comparison. Hook
  installation separately requires **Webhooks: write**; that installation
  permission need not be retained by the routine's token. See GitHub's
  [review API](https://docs.github.com/en/rest/pulls/reviews),
  [comparison API](https://docs.github.com/en/rest/commits/commits#compare-two-commits),
  and [webhook API](https://docs.github.com/en/rest/repos/webhooks).

## Prepare the routine

Create a non-secret `config.json` with the exact repository ID (from GitHub), PR
number, base branch, and the login under which the publishing token acts:

```json
{
  "repo": "example-org/review-sandbox",
  "repository_id": 123,
  "pr_number": 1,
  "base_ref": "main",
  "reviewer_login": "example-reviewer"
}
```

```sh
python3 examples/github-review-pilot/build_routine.py \
  --config config.json --agent review-pilot > routine.json
crewship routine validate routine.json
crewship crew files save review-crew shared/scripts/github_review_guard.py \
  --file examples/github-review-pilot/review_guard.py
crewship routine save --name github-review-pilot --author-crew review-crew \
  --author-agent review-pilot --definition routine.json
```

Set up the GitHub credential through the vault UI or the CLI's secure stdin
input. Never put it in this configuration, the routine definition, prompts,
shell arguments, source control, or a conversation. Save refuses a missing
required credential. Review and approve the routine if governance proposes it.

In Integrations → Incoming, create an endpoint for the routine. Choose the
GitHub PR signature profile in Advanced settings. Copy the URL and signing
secret into the repository webhook configuration (`application/json`, only
pull-request events, TLS verification enabled). These are the **webhook signing
secret**, not the GitHub API credential. Configure the endpoint with a low rate
limit and pin the reviewed routine version.

The event's repository ID/name, PR number, base branch, and same-repository head
are checked before network steps. Network destinations come from operator
configuration, never an event-provided URL. The diff is fetched using both
explicit SHAs. No PR code is checked out or executed.

## Validate against reality

1. Use an intentionally incorrect, small text change in the sandbox PR.
2. Trigger a real `opened`, `reopened`, or `synchronize` event. Confirm a 202 in
   **GitHub's delivery record**, not just a locally constructed signed request.
3. Follow its receipt/run ID in Crewship to a completed agent step and review.
4. Read the review back from GitHub and confirm its `commit_id`, publisher and
   concrete finding. The model's final text alone does not prove publication.
5. Redeliver the same GitHub delivery. Confirm the same receipt/run and no
   second agent invocation/review.
6. Change the PR head while the agent is running. Publication must refuse the
   stale snapshot. A subsequent new event may review the new revision.
7. Disable/delete temporary hooks and stop any temporary relay after testing.

Run deterministic guard tests with:

```sh
python3 -m unittest discover -s examples/github-review-pilot -v
```

## Deliberate limits

- This is a bounded pilot, not a general replacement for a code review service.
  It refuses fork PRs, more than 20 changed files, missing/truncated patches,
  oversized diffs, truncated comparison histories and 100-or-more prior reviews.
- Prior reviews are checked before and after the LLM call. This prevents ordinary
  repeats but is **not an atomic exactly-once publishing guarantee**: concurrent
  distinct deliveries can pass both checks. Ingress deduplication independently
  handles replay of the same signed payload. Serialize pilot triggers.
- A PR may change between the last GET and POST. GitHub receives an explicit
  `commit_id`; the review remains attached to that revision, and is never an
  approval. There is no claim of an atomic "still latest" compare-and-swap.
- A temporary tunnel can establish test connectivity. It is not a durable
  deployment, and does not repair the dev domain's DNS. Cloudflare describes
  [Quick Tunnels](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/)
  as a testing facility; expose only the intended authenticated ingress.
- If a local harness fetches the diff or posts the result with `gh`, record that
  explicitly. It proves those operations worked, **not** that the complete
  configured routine ran unattended.
