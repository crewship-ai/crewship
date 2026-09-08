# Additive team Chat demo seed — 2026-09-07

`crewship seed team-chat` adds the catalog's six fictional humans, actual roles,
bundled PNG avatars, a shared **Team lounge · demo** channel and independently
authored DMs to the existing owner. `crewship seed --with-team-chat` calls the
same function. The legacy four-user `--with-users` fixture is unchanged.
`./dev.sh seed` opts into the new pack by default.

Accounts use owner provisioning with `create_only=true`, account setup tokens,
and normal credential login. A preflight requires the new create-only property
in the server's OpenAPI schema: old servers can ignore unknown fields, so merely
sending the flag would not be a safety boundary. There is no signup, email,
direct database write, printed password, owner modification or automatic agent job.

Emails include a stable workspace suffix. Locally owned account state is scoped
by server/workspace under a private directory outside Git (0700), with atomic
0600 file replacement and an exclusive invocation lock. State holds random
passwords, setup progress and IDs. A collision without owned state or a role
change fails before provisioning. Reruns authenticate normally and do not reset
passwords, rename personalized accounts or replace an existing avatar. A missing
membership can be restored only after successful authentication and matching
identity; deleted owned demo channels can be recreated. Ambiguous lost creation
responses fail closed instead of creating duplicates.

Identity uses the actual bearer-capable `/api/v1/auth/cli-token/validate` handler.
Avatar/name values come from the real nested workspace roster; `/users/me` has
no GET route and is used only for an initially missing name via PATCH. All six
roles can participate in Chat under the current policy, including VIEWER; the
fixture does not misrepresent VIEWER as unable to send chat messages.

Twelve auth POSTs for six fresh people exceed the default ten-request burst.
The isolated auth transport retries only 429 reset/credential requests, respects
Retry-After up to 60 seconds, preserves request bodies, observes cancellation,
and stops after three attempts. Non-auth writes are not retried automatically.

Automated verification covers six distinct authenticated authors, exact roles,
stable DM/channel messages, secure file modes, random secret non-disclosure,
rerun idempotency, preserved human edits, unowned collisions, role drift,
changed-password refusal, owned membership/channel recovery, setup response
recovery, concurrent invocation lock, old-server capability refusal, and bounded
429 retries/cancellation. A real migrated router integration exercises normal
setup/login, role membership, PNG upload, persisted human messages and rerun for
one representative account; the full six-person scenario has separate tests.

Live Dev2 verification is recorded by the parent deployment report; these are
implementation/test results, not a claim that this file's author deployed.
