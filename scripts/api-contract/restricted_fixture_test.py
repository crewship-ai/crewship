#!/usr/bin/env python3
"""Unit tests for restricted_fixture.py — provisioning + proof logic on stubs.

The contract under test: the fixture exits 0 and writes a token ONLY when the
restricted member positively reaches the routes AND the owner still gets the
designed 404; every way that can go wrong is a named failure, never a skip.
"""
import contextlib
import io
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import restricted_fixture  # noqa: E402

WS = "ws1"
LISTS = (f"/api/v1/workspaces/{WS}/restricted-routines", f"/api/v1/workspaces/{WS}/restricted-routine-runs")


class Stub:
    def __init__(self, answers):
        self.answers = answers
        self.calls = []

    def get(self, path):
        self.calls.append(("GET", path, None))
        return self.answers[("GET", path)]

    def send(self, method, path, payload=None):
        self.calls.append((method, path, payload))
        return self.answers[(method, path)]


def provisioning_answers(mode="trusted", invite=200, signup=202):
    access = f"/api/v1/workspaces/{WS}/members/m1/access"
    return {
        ("POST", f"/api/v1/workspaces/{WS}/invitations?workspace_id={WS}"): (invite, {}),
        ("POST", "/api/v1/auth/signup"): (signup, {}),
        ("GET", f"/api/v1/admin/users?workspace_id={WS}"): (200, [{"id": "u1", "email": restricted_fixture.EMAIL}]),
        ("GET", f"/api/v1/workspaces/{WS}/members"): (200, [{"id": "m1", "user_id": "u1"}]),
        ("GET", access): (200, {"id": "m1", "revision": 3, "mode": mode, "rights": []}),
        ("PUT", access): (200, {"id": "m1", "revision": 4, "mode": "restricted", "rights": []}),
    }


class Provision(unittest.TestCase):
    def test_places_member_as_manager_and_returns_policy_path(self):
        stub = Stub(provisioning_answers())
        path = restricted_fixture.provision(stub, WS)
        self.assertEqual(path, f"/api/v1/workspaces/{WS}/members/m1/access")
        invite = next(c for c in stub.calls if "invitations" in c[1])
        self.assertEqual(invite[2]["role"], "MANAGER")  # manualPolicy needs OWNER/ADMIN/MANAGER

    def test_existing_invitation_409_is_fine(self):
        restricted_fixture.provision(Stub(provisioning_answers(invite=409)), WS)

    def test_invite_failure_is_fatal(self):
        with self.assertRaises(SystemExit):
            restricted_fixture.provision(Stub(provisioning_answers(invite=500)), WS)

    def test_signup_refused_names_the_flag(self):
        with self.assertRaises(SystemExit) as ctx:
            restricted_fixture.provision(Stub(provisioning_answers(signup=403)), WS)
        self.assertIn("CREWSHIP_ALLOW_SIGNUP", str(ctx.exception))

    def test_member_missing_from_roster_is_fatal(self):
        answers = provisioning_answers()
        answers[("GET", f"/api/v1/admin/users?workspace_id={WS}")] = (200, [])
        with self.assertRaises(SystemExit):
            restricted_fixture.provision(Stub(answers), WS)


class SetMode(unittest.TestCase):
    def test_replaces_policy_at_the_revision_just_read_with_explicit_empty_rights(self):
        stub = Stub(provisioning_answers(mode="trusted"))
        restricted_fixture.set_mode(stub, f"/api/v1/workspaces/{WS}/members/m1/access", "restricted")
        put = next(c for c in stub.calls if c[0] == "PUT")
        self.assertEqual(put[2], {"id": "m1", "revision": 3, "mode": "restricted", "rights": []})

    def test_already_in_mode_does_not_write(self):
        stub = Stub(provisioning_answers(mode="restricted"))
        restricted_fixture.set_mode(stub, f"/api/v1/workspaces/{WS}/members/m1/access", "restricted")
        self.assertFalse(any(c[0] == "PUT" for c in stub.calls))

    def test_put_not_leaving_the_mode_is_fatal(self):
        answers = provisioning_answers(mode="trusted")
        answers[("PUT", f"/api/v1/workspaces/{WS}/members/m1/access")] = (200, {"mode": "trusted"})
        with self.assertRaises(SystemExit):
            restricted_fixture.set_mode(Stub(answers), f"/api/v1/workspaces/{WS}/members/m1/access", "restricted")

    def test_stale_revision_409_is_fatal_not_retried(self):
        answers = provisioning_answers(mode="trusted")
        answers[("PUT", f"/api/v1/workspaces/{WS}/members/m1/access")] = (409, {"error": "stale"})
        with self.assertRaises(SystemExit):
            restricted_fixture.set_mode(Stub(answers), f"/api/v1/workspaces/{WS}/members/m1/access", "restricted")


def proof_answers(member_lists=200, owner_lists=404, member_absent=404):
    member = {("GET", LISTS[0]): (member_lists, []), ("GET", LISTS[1]): (member_lists, []),
              ("GET", LISTS[1] + "/no-such-run"): (member_absent, {"error": "x"})}
    owner = {("GET", LISTS[0]): (owner_lists, {}), ("GET", LISTS[1]): (owner_lists, {})}
    return Stub(owner), Stub(member)


def prove(owner, member):
    failures = []
    with contextlib.redirect_stdout(io.StringIO()):
        restricted_fixture.prove(owner, member, WS, failures)
    return failures


class Prove(unittest.TestCase):
    def test_all_proofs_pass(self):
        owner, member = proof_answers()
        self.assertEqual(prove(owner, member), [])

    def test_member_404_on_a_list_is_a_failure(self):
        owner, member = proof_answers(member_lists=404)
        self.assertEqual(len(prove(owner, member)), 2)

    def test_member_202_is_not_a_200(self):
        owner, member = proof_answers(member_lists=202)
        self.assertEqual(len(prove(owner, member)), 2)

    def test_owner_getting_200_means_the_negative_is_gone(self):
        owner, member = proof_answers(owner_lists=200)
        self.assertEqual(len(prove(owner, member)), 2)

    def test_absent_run_answering_200_fails(self):
        owner, member = proof_answers(member_absent=200)
        self.assertEqual(len(prove(owner, member)), 1)

    def test_non_list_body_is_a_failure(self):
        owner, member = proof_answers()
        member.answers[("GET", LISTS[0])] = (200, {"routines": []})
        self.assertEqual(len(prove(owner, member)), 1)


class Main(unittest.TestCase):
    def run_main(self, failures):
        with tempfile.TemporaryDirectory() as tmp:
            token_file = os.path.join(tmp, "restricted.token")
            order = []
            with mock.patch.object(restricted_fixture.fixture_probes, "Api", side_effect=lambda *a: Stub({})), \
                    mock.patch.object(restricted_fixture, "provision", return_value="/p"), \
                    mock.patch.object(restricted_fixture, "set_mode", side_effect=lambda a, p, m: order.append(m)), \
                    mock.patch.object(restricted_fixture, "mint_member_token", side_effect=lambda *a: order.append("mint") or "tok-123"), \
                    mock.patch.object(restricted_fixture, "prove", side_effect=lambda o, m, w, f: f.extend(failures)), \
                    contextlib.redirect_stdout(io.StringIO()):
                rc = restricted_fixture.main(["x", "http://127.0.0.1:1", "owner", WS, "crewship", token_file])
            written = Path(token_file).read_text() if os.path.exists(token_file) else None
            mode = stat.S_IMODE(os.stat(token_file).st_mode) if written is not None else None
            return rc, written, mode, order

    def test_token_written_0600_only_after_proofs_pass(self):
        rc, written, mode, _ = self.run_main([])
        self.assertEqual((rc, written, mode), (0, "tok-123", 0o600))

    def test_failed_proof_exits_1_and_writes_no_token(self):
        rc, written, _, _ = self.run_main(["boom"])
        self.assertEqual((rc, written), (1, None))

    def test_token_is_minted_while_trusted_then_restricted(self):
        _, _, _, order = self.run_main([])
        self.assertEqual(order, ["trusted", "mint", "restricted"])


if __name__ == "__main__":
    unittest.main()
