#!/usr/bin/env python3
"""Owned fixtures only: no server, model, credential or provider calls."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("crew_links_receipt", HERE / "crew-links-receipt.py")
receipt = importlib.util.module_from_spec(spec)
spec.loader.exec_module(receipt)


def fixture():
    code, tag = "VERIFIED-fixture", "XLINK-fixture"
    summary = "Private fixture, never stored: žluťoučký "+"x" * 260 + " " + code + "\n\n"
    candidate = {"assignment_id": "assignment", "outcome": "SUCCEEDED", "created_activity_id": "created",
                 "completed_activity_id": "completed", "target_agent_id": "morgan"}
    run = {"id": "assignment", "agent_slug": "morgan", "agent_id": "morgan", "run_id": "distinct-journal",
           "status": "COMPLETED", "result_summary": "preview without code"}
    full = {"status": "COMPLETED", "outcome": "SUCCEEDED", "result_summary": summary, "untrusted_extra": "SYNTHETIC-CANARY"}
    source = {"id": "run-start:chat-run", "entry_type": "run.started", "trace_id": "chat-run", "agent_id": "alex",
              "summary": "SYNTHETIC-CANARY", "payload": {"chat_id": "owner-chat"}}
    refused = {"id": "refused", "entry_type": "assignment.refused", "actor_type": "agent", "trace_id": "chat-run",
               "actor_id": "alex", "agent_id": "alex", "crew_id": "eng", "summary": "SYNTHETIC-CANARY",
               "refs": {"chat_id": "owner-chat"},
               "payload": {"reason": "crews_not_connected", "http_status": 403, "source_crew_id": "eng",
                           "target_crew_id": "ops", "target_slug": "morgan", "task_sha256": "a" * 64}}
    return code, tag, candidate, run, full, source, refused


class ProjectionTests(unittest.TestCase):
    def setUp(self):
        self.code, self.tag, self.candidate, self.run, self.full, self.source, self.refused = fixture()

    def positive(self):
        return receipt.positive("ENG-1", "assignment", self.code, self.tag, [self.candidate], [self.run], self.full)

    def negative(self):
        return receipt.chat_refusal("owner-chat", "eng", "ops", "alex", "a" * 64, [self.refused], [self.source])

    def build(self, positive=None, negative=None):
        with mock.patch.dict(os.environ, {"SCAN_RUN_ID": "fixture-run", "STAGE_RUN_ID": "fixture-run", "CREWSHIP_WORKSPACE": "workspace"}), \
             mock.patch.object(receipt, "source_provenance", return_value={"sha": "a" * 40, "files_sha256": {}}):
            return receipt.receipt(positive or self.positive(), negative or self.negative(), "ops-record", "OPS-1", "eng", "ops", "2026-11-04T19:00:00", "9", "0", "0")

    def test_hashes_exact_full_utf8_not_preview_and_never_serializes_text(self):
        proof = self.positive()
        self.assertEqual(proof["full_result_utf8_sha256"], hashlib.sha256(self.full["result_summary"].encode()).hexdigest())
        self.assertEqual(proof["full_result_utf8_bytes"], len(self.full["result_summary"].encode()))
        stored = json.dumps(self.build())
        for raw in [self.code, self.tag, "SYNTHETIC-CANARY", "Private fixture", "preview without code"]:
            self.assertNotIn(raw, stored)
        self.assertNotEqual(proof["assignment_id"], proof["journal_run_id"])

    def test_positive_requires_assignment_join_full_code_and_completed_outcome(self):
        for section, key, value in [
            (self.run, "id", "distinct-journal"), (self.run, "status", "FAILED"),
            (self.run, "agent_slug", "alex"), (self.full, "status", "FAILED"),
            (self.full, "outcome", "PARTIAL"), (self.full, "result_summary", "preview without code"),
            (self.candidate, "assignment_id", "unrelated"),
        ]:
            old = section[key]
            with self.subTest(key=key, value=value):
                section[key] = value
                with self.assertRaises(receipt.NotMatched):
                    self.positive()
            section[key] = old

    def test_refusal_is_joined_to_actual_server_actor_trace_crews_and_403(self):
        proof = self.negative()
        self.assertEqual(proof["refusal"]["evidence_source"], "server_journal")
        self.assertEqual((proof["chat_id"], proof["source_run_id"], proof["run_started_entry_id"]), ("owner-chat", "chat-run", "run-start:chat-run"))
        for section, key, value in [
            (self.refused["refs"], "chat_id", "other-chat"),
            (self.refused["payload"], "task_sha256", "b" * 64),
            (self.source, "trace_id", "other-run"), (self.source, "agent_id", "other"),
            (self.source["payload"], "chat_id", "other-chat"), (self.source, "entry_type", "run.completed"),
            (self.refused, "trace_id", "other"), (self.refused, "actor_id", "other"),
            (self.refused, "agent_id", "other"), (self.refused, "crew_id", "other"),
            (self.refused["payload"], "target_crew_id", "other"),
            (self.refused["payload"], "source_crew_id", "other"),
            (self.refused["payload"], "http_status", 400),
            (self.refused["payload"], "reason", "other"),
            (self.refused["payload"], "target_slug", "other"),
        ]:
            old = section[key]
            with self.subTest(key=key, value=value):
                section[key] = value
                with self.assertRaises(receipt.NotMatched):
                    self.negative()
            section[key] = old
        with self.assertRaises(receipt.NotMatched):
            receipt.chat_refusal("owner-chat", "eng", "ops", "alex", "a" * 64, [], [self.source])
        with self.assertRaises(receipt.NotMatched):
            receipt.chat_refusal("owner-chat", "eng", "ops", "alex", "a" * 64, [self.refused], [])
        with self.assertRaises(ValueError):
            receipt.chat_refusal("owner-chat", "eng", "ops", "alex", "not-a-hash", [self.refused], [self.source])

    def test_final_projection_rejects_extra_fields_changed_bindings_or_unbounded_text(self):
        for proof, key, value in [
            (self.positive(), "raw_response", "SYNTHETIC-CANARY"),
            (self.positive(), "outcome", "SYNTHETIC-CANARY"),
            (self.positive(), "full_result_utf8_sha256", "not-a-hash"),
        ]:
            proof[key] = value
            with self.assertRaises(ValueError):
                self.build(positive=proof)
        proof = self.negative()
        proof["refusal"]["actor_id"] = "wrong-actor"
        with self.assertRaises(ValueError):
            self.build(negative=proof)

    def test_cli_framing_projects_without_raw_stdout_or_error_snippets(self):
        values = ["ENG-1", "assignment", self.code, self.tag, json.dumps([self.candidate]), json.dumps([self.run]), json.dumps(self.full)]
        payload = b"\0".join(value.encode() for value in values) + b"\0"
        result = subprocess.run([sys.executable, str(HERE / "crew-links-receipt.py"), "positive"], input=payload, capture_output=True)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout), self.positive())
        self.assertEqual(result.stderr, b"")
        self.assertNotIn(self.code.encode(), result.stdout)
        bad = subprocess.run([sys.executable, str(HERE / "crew-links-receipt.py"), "positive"], input=b"SYNTHETIC-CANARY", capture_output=True)
        self.assertEqual(bad.returncode, 2)
        self.assertNotIn(b"SYNTHETIC-CANARY", bad.stdout + bad.stderr)


class PersistenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.root.chmod(0o700)
        self.value = {"schema_version": 1, "fixture": "only synthetic metadata"}

    def final(self):
        return self.root / "test-crew-links/success-receipt.v1.json"

    def test_private_modes_and_exclusive_atomic_publication(self):
        receipt.persist(str(self.root), self.value)
        self.assertEqual(json.loads(self.final().read_text()), self.value)
        self.assertEqual(stat.S_IMODE(self.final().stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.final().parent.stat().st_mode), 0o700)
        original = self.final().read_bytes()
        with self.assertRaises(FileExistsError):
            receipt.persist(str(self.root), {"replacement": True})
        self.assertEqual(self.final().read_bytes(), original)
        self.assertEqual(len(list(self.final().parent.iterdir())), 1)

    def test_symlinked_root_parent_suite_or_final_is_refused(self):
        alias = self.root / "alias"
        alias.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(OSError):
            receipt.persist(str(alias), self.value)
        (self.root / "test-crew-links").symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(OSError):
            receipt.persist(str(self.root), self.value)
        (self.root / "test-crew-links").unlink()
        self.final().parent.mkdir(mode=0o700)
        canary = self.root / "canary"
        canary.write_text("unchanged")
        self.final().symlink_to(canary)
        with self.assertRaises(FileExistsError):
            receipt.persist(str(self.root), self.value)
        self.assertEqual(canary.read_text(), "unchanged")
        self.assertTrue(self.final().is_symlink())

    def test_public_permissions_or_wrong_owner_are_refused(self):
        self.root.chmod(0o755)
        with self.assertRaises(ValueError):
            receipt.persist(str(self.root), self.value)
        self.root.chmod(0o700)
        with mock.patch.object(receipt.os, "geteuid", return_value=os.geteuid() + 1):
            with self.assertRaises(ValueError):
                receipt.persist(str(self.root), self.value)
        self.assertFalse(self.final().exists())

    def test_fsync_failure_leaves_no_success_file_or_temporary(self):
        self.final().parent.mkdir(mode=0o700)
        for effects in [[OSError("fixture write failure")], [None, OSError("fixture directory sync failure")]]:
            with self.subTest(effects=len(effects)), mock.patch.object(receipt.os, "fsync", side_effect=effects):
                with self.assertRaises(OSError):
                    receipt.persist(str(self.root), self.value)
            self.assertEqual(list(self.final().parent.iterdir()), [])

    def test_actual_writer_cli_source_binding_permissions_and_failure_exit(self):
        code, tag, candidate, run, full, source, refused = fixture()
        positive = receipt.positive("ENG-1", "assignment", code, tag, [candidate], [run], full)
        negative = receipt.chat_refusal("owner-chat", "eng", "ops", "alex", "a" * 64, [refused], [source])
        checkout = self.root / "fixture-repo"
        here = checkout / "scripts/test-harness"
        here.mkdir(parents=True)
        for name in ["run-all.sh", "lib.sh", "test-crew-links.sh", "crew-links-evidence.sh", "crew-links-receipt.py"]:
            (here / name).write_bytes((HERE / name).read_bytes())
        environment = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                       "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid",
                       "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"}
        def git(*args):
            return subprocess.check_output(["git", "-C", str(checkout), *args], env=environment, stderr=subprocess.DEVNULL)
        git("init", "--quiet")
        git("add", "scripts")
        git("commit", "-m", "test: synthetic receipt source fixture")
        sha = git("rev-parse", "HEAD").decode().strip()
        environment.update(INGEST_SHA=sha, SCAN_RUN_ID="fixture-run", STAGE_RUN_ID="fixture-run", CREWSHIP_WORKSPACE="fixture-workspace")
        parts = [json.dumps(positive), json.dumps(negative), "record-id", "OPS-1", "eng", "ops", "2026-11-04T19:00:00", "9", "0", "0"]
        payload = b"\0".join(part.encode() for part in parts) + b"\0"
        result = subprocess.run([sys.executable, str(here / "crew-links-receipt.py"), "write", str(self.root)],
                                input=payload, capture_output=True, env=environment)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, b"")
        stored = self.final().read_bytes()
        value = json.loads(stored)
        self.assertEqual(value["source"]["sha"], sha)
        self.assertEqual(value["run"]["workspace_sha256"], receipt.digest("fixture-workspace"))
        for canary in [code.encode(), tag.encode(), b"Private fixture", b"SYNTHETIC-CANARY"]:
            self.assertNotIn(canary, stored)
        # A duplicate must fail rather than replace any existing receipt.
        duplicate = subprocess.run([sys.executable, str(here / "crew-links-receipt.py"), "write", str(self.root)],
                                   input=payload, capture_output=True, env=environment)
        self.assertEqual(duplicate.returncode, 2)
        self.assertEqual(self.final().read_bytes(), stored)
        # A dirty harness cannot claim to be the runtime's bound commit.
        other = self.root / "other-private-root"
        other.mkdir(mode=0o700)
        (here / "lib.sh").write_text("uncommitted synthetic fixture change")
        dirty = subprocess.run([sys.executable, str(here / "crew-links-receipt.py"), "write", str(other)],
                               input=payload, capture_output=True, env=environment)
        self.assertEqual(dirty.returncode, 2)
        self.assertEqual(list(other.iterdir()), [])


class ProvenanceTests(unittest.TestCase):
    def test_source_requires_same_commit_and_exact_harness_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            here = root / "scripts/test-harness"
            here.mkdir(parents=True)
            names = ["run-all.sh", "lib.sh", "test-crew-links.sh", "crew-links-evidence.sh", "crew-links-receipt.py"]
            committed = {}
            for name in names:
                committed[name] = ("synthetic file " + name).encode()
                (here / name).write_bytes(committed[name])
            def git(command, **kwargs):
                if command[-2:] == ["rev-parse", "HEAD"]:
                    return ("a" * 40 + "\n").encode()
                return committed[command[-1].split("/")[-1]]
            with mock.patch.object(receipt, "__file__", str(here / "crew-links-receipt.py")), \
                 mock.patch.dict(os.environ, {"INGEST_SHA": "a" * 40}), \
                 mock.patch.object(receipt.subprocess, "check_output", side_effect=git):
                # rev-parse text=True, show bytes: emulate the actual API types.
                def typed_git(command, **kwargs):
                    value = git(command, **kwargs)
                    return value.decode() if kwargs.get("text") else value
                with mock.patch.object(receipt.subprocess, "check_output", side_effect=typed_git):
                    self.assertEqual(receipt.source_provenance()["sha"], "a" * 40)
                    (here / "lib.sh").write_text("uncommitted fixture change")
                    with self.assertRaises(ValueError):
                        receipt.source_provenance()
                with mock.patch.dict(os.environ, {"INGEST_SHA": "b" * 40}):
                    with mock.patch.object(receipt.subprocess, "check_output", side_effect=typed_git):
                        with self.assertRaises(ValueError):
                            receipt.source_provenance()


if __name__ == "__main__":
    unittest.main()
