#!/usr/bin/env python3
"""Restricted-member fixture for the #1815 restricted-workflow operations.

GET /workspaces/{id}/restricted-routines, .../restricted-routine-runs and
.../restricted-routine-runs/{runId} answer a uniform 404 to anyone who is not
a `restricted` workspace member (internal/restrictedworkflow: Catalog and
ResultsForActor deny unless Membership.Mode == "restricted"; Result needs a
receipt owned by the caller). The gate's token is the seeded OWNER, so even a
fully wired restricted runtime grades only the 404 — registration alone proves
nothing. This script installs the missing precondition for real, through the
product's own surfaces, never a global auth bypass:

  1. invites an address into the workspace as MANAGER (the catalog route
     also demands manualPolicy: OWNER/ADMIN/MANAGER or the routine-run
     capability — restricted mode narrows what that role may do, it does not
     replace the role check), signs it up (the invitation is
     redeemed inside the signup transaction) — needs CREWSHIP_ALLOW_SIGNUP;
  2. mints that user's own CLI token with the shipped `crewship login` +
     `crewship token create` (token minting is self-scoped, so the member
     must sign in as itself — and it must happen BEFORE step 3, because
     restricted mode denies that route);
  3. replaces the member's access policy with mode "restricted" and an
     explicit empty rights array (PUT .../members/{id}/access at the revision
     just read; a stale revision fails with 409);
  4. proves, with real HTTP: restricted token -> 200 JSON list on both list
     routes and 404 for an absent runId; OWNER token -> 404 on both list routes
     (the designed negative — it is what the gate graded before this fixture).

Only after every proof passes is the restricted token written (0600) to
--token-file, where run.sh uses it for the three restricted operations. A run
that cannot establish the member FAILS; it never skips.

NOT proven here, by design: a positive {runId} receipt. A receipt exists only
after an admitted restricted routine run, which executes inside the restricted
runner image; this fixture does not start that image. The {runId} operation is
therefore graded against the designed not-found answer for an absent run.

Idempotent: a second invocation reuses the invitation (409), the account, lifts the
restriction only long enough to mint a fresh token, and restores it.
"""
import json
import os
import subprocess
import sys
import tempfile
import urllib.parse

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import fixture_probes  # noqa: E402

EMAIL = "restricted-fixture@crewship.local"
FULL_NAME = "Restricted Fixture"
# Dev-only credential for a throwaway CI server, same convention as the
# seeded RBAC users (cmd_seed_data_users.go). Never use against a real instance.
PASSWORD = "restricted-fixture-pass1"


def provision(api, workspace, email=EMAIL):
    """Place `email` in the workspace; return the member's access-policy path."""
    q = urllib.parse.quote(workspace)
    status, body = api.send("POST", f"/api/v1/workspaces/{q}/invitations?workspace_id={q}",
                            {"email": email, "role": "MANAGER"})
    if status not in (200, 201, 409):
        raise SystemExit(f"invite {email}: HTTP {status}: {body}")
    status, body = api.send("POST", "/api/v1/auth/signup",
                            {"email": email, "full_name": FULL_NAME, "password": PASSWORD})
    if status not in (200, 201, 202):
        raise SystemExit(
            f"signup {email}: HTTP {status}: {body} (is CREWSHIP_ALLOW_SIGNUP=true on the server?)")
    status, users = api.get(f"/api/v1/admin/users?workspace_id={q}")
    user_id = next((u.get("id") for u in users or [] if str(u.get("email", "")).lower() == email), None) \
        if status == 200 and isinstance(users, list) else None
    if not user_id:
        raise SystemExit(f"{email} is not in the workspace roster after invite+signup (HTTP {status}); "
                         "the account may predate this fixture without the invitation being redeemed")
    status, members = api.get(f"/api/v1/workspaces/{q}/members")
    member = next((m for m in members or [] if m.get("user_id") == user_id), None) \
        if status == 200 and isinstance(members, list) else None
    if not member:
        raise SystemExit(f"no membership row for {email} (HTTP {status})")
    return f"/api/v1/workspaces/{q}/members/{urllib.parse.quote(member['id'])}/access"


def set_mode(api, path, mode):
    """Replace the member's policy at the revision just read (stale = 409)."""
    status, policy = api.get(path)
    if status != 200 or not isinstance(policy, dict):
        raise SystemExit(f"GET {path}: HTTP {status}: {policy}")
    if policy.get("mode") == mode:
        return
    status, body = api.send("PUT", path, dict(policy, mode=mode, rights=[]))
    if status != 200 or not isinstance(body, dict) or body.get("mode") != mode:
        raise SystemExit(f"PUT {path}: expected 200 with mode {mode}, got HTTP {status}: {body}")


def mint_member_token(crewship, base, workspace, email=EMAIL, password=PASSWORD):
    """Sign in as the member with the shipped CLI and mint its own token."""
    with tempfile.TemporaryDirectory(prefix="restricted-fixture-") as tmp:
        env = {k: v for k, v in os.environ.items() if not k.startswith("CREWSHIP_")}
        env.update(CREWSHIP_CONFIG=os.path.join(tmp, "config.json"), CREWSHIP_PASSWORD=password,
                   HOME=tmp)
        login = subprocess.run([crewship, "login", "--server", base, "--email", email],
                               env=env, capture_output=True, text=True, timeout=60)
        if login.returncode != 0:
            raise SystemExit(f"member login failed: {login.stderr.strip() or login.stdout.strip()}")
        mint = subprocess.run([crewship, "token", "create", "api-contract-restricted-fixture",
                               "--quiet", "--server", base, "--workspace", workspace],
                              env=env, capture_output=True, text=True, timeout=60)
        token = mint.stdout.strip()
        if mint.returncode != 0 or not token:
            raise SystemExit(f"member token mint failed: {mint.stderr.strip()}")
        return token


def prove(owner, member, workspace, failures):
    """Positive for the restricted member, designed 404 negative for the owner."""
    q = urllib.parse.quote(workspace)
    lists = (f"/api/v1/workspaces/{q}/restricted-routines", f"/api/v1/workspaces/{q}/restricted-routine-runs")
    print("restricted fixture probes:")
    for path in lists:
        status, body = member.get(path)
        if status == 200 and isinstance(body, list):
            print(f"  ok   restricted member GET {path.rsplit('/', 1)[1]} -> 200 list of {len(body)}")
        else:
            failures.append(f"restricted member GET {path}: expected 200 with a JSON list, got HTTP {status}: {body}")
        status, body = owner.get(path)
        if status == 404:
            print(f"  ok   owner GET {path.rsplit('/', 1)[1]} -> 404 (designed negative)")
        else:
            failures.append(f"owner GET {path}: expected the designed 404, got HTTP {status}: {body}")
    absent = f"{lists[1]}/no-such-run"
    status, body = member.get(absent)
    if status == 404:
        print("  ok   restricted member GET restricted-routine-runs/{absent} -> 404 (no receipt; positive receipt NOT covered)")
    else:
        failures.append(f"restricted member GET {absent}: expected 404 for an absent run, got HTTP {status}: {body}")


def main(argv):
    if len(argv) != 6:
        raise SystemExit("usage: restricted_fixture.py <base-url> <owner-token> <workspace-id> <crewship-binary> <token-file>")
    base, owner_token, workspace, crewship, token_file = argv[1:]
    owner = fixture_probes.Api(base, owner_token, workspace)
    policy_path = provision(owner, workspace)
    # Mint FIRST, restrict SECOND: restricted mode denies the (unintegrated)
    # token-minting route, so a restricted member can no longer sign in to
    # make a token. A re-run therefore lifts the restriction briefly, mints,
    # and restores it before anything is probed.
    set_mode(owner, policy_path, "trusted")
    member_token = mint_member_token(crewship, base, workspace)
    set_mode(owner, policy_path, "restricted")
    member = fixture_probes.Api(base, member_token, workspace)
    failures = []
    prove(owner, member, workspace, failures)
    if failures:
        for f in failures:
            print(f"RESTRICTED FIXTURE FAIL: {f}")
        return 1
    fd = os.open(token_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as fh:
        fh.write(member_token)
    print("restricted fixture: all passed")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
