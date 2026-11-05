#!/usr/bin/env python3
"""Synthetic subprocess timeout/owned metadata only; no product/model/server."""
import importlib.util
import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import tempfile
import time
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent


def module():
    spec = importlib.util.spec_from_file_location("interrupted", HERE / "crew-links-interrupted.py")
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


class MetadataTests(unittest.TestCase):
    def test_owned_only_allowlist_and_bounded_rows(self):
        helper = module()
        calls = []
        def command(argv, _deadline):
            calls.append(argv)
            ident = argv[-3]
            if "get" in argv:
                return json.dumps({"id": "owned-id", "identifier": ident, "description": "SECRET-CANARY"}).encode(), "ok"
            return json.dumps([{"id": "assignment", "run_id": "different-journal", "agent_id": "actor", "agent_slug": "alex",
                                "status": "FAILED", "outcome": "FAILED", "result_summary": "SECRET-CANARY",
                                "error_message": "SECRET-CANARY"}]).encode(), "ok"
        with mock.patch.object(helper, "command", side_effect=command):
            result = helper.collect(["ENG-1"], ["fixture-cli"], time.monotonic()+12)
        self.assertEqual(len(calls), 2)
        self.assertTrue(all("ENG-1" in x for x in calls))
        self.assertNotIn("SECRET-CANARY", json.dumps(result))
        self.assertNotEqual(result[0]["runs"][0]["assignment_id"], result[0]["runs"][0]["journal_run_id"])
        self.assertEqual(result[0]["runs"][0]["error_class"], "not_available_from_metadata")
        with mock.patch.object(helper, "command", return_value=(json.dumps([{"id": "x"}]*17).encode(), "ok")):
            self.assertFalse(helper.collect(["ENG-1"], ["fixture"], time.monotonic()+12)[0]["complete"])

    def test_command_timeout_and_output_limit_kill_owned_group(self):
        helper = module()
        began = time.monotonic()
        _, reason = helper.command(["python3", "-c", "import time;time.sleep(30)"], began+0.15)
        self.assertEqual(reason, "timeout")
        self.assertLess(time.monotonic()-began, 2)
        _, reason = helper.command(["python3", "-c", "print('x'*300000)"], time.monotonic()+2)
        self.assertEqual(reason, "oversized")

    def test_exited_leader_keeps_identity_until_descendant_stdout_cleanup(self):
        helper = module()
        # The direct leader exits immediately, but its owned descendant keeps
        # stdout open. Timeout must group-kill BEFORE reaping that leader.
        program = "import subprocess,sys;p=subprocess.Popen([sys.executable,'-c','import time;time.sleep(30)']);print(p.pid,flush=True)"
        real_kill = helper.os.killpg
        def anchored_kill(leader, sig):
            # WNOWAIT proves the exact direct leader is STILL our unreaped
            # exited child at the group-kill boundary, not a recyclable PID.
            owned = os.waitid(os.P_PID, leader, os.WEXITED | os.WNOHANG | os.WNOWAIT)
            self.assertIsNotNone(owned)
            self.assertEqual(owned.si_pid, leader)
            return real_kill(leader, sig)
        with mock.patch.object(helper.os, "killpg", side_effect=anchored_kill) as kill:
            raw, reason = helper.command(["python3", "-c", program], time.monotonic()+0.5)
        self.assertEqual(reason, "timeout")
        self.assertEqual(kill.call_count, 1)
        descendant = int(raw.strip())
        def descendant_state():
            try:
                return Path("/proc", str(descendant), "stat").read_text().split()[2]
            except FileNotFoundError:
                return None  # Successful init reap is also a passing cleanup.
        until = time.monotonic()+1
        while descendant_state() not in {None, "Z"} and time.monotonic() < until:
            time.sleep(0.01)
        self.assertIn(descendant_state(), {None, "Z"})

    def test_atomic_private_noreplace_and_symlink_refusal(self):
        helper = module()
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            root.chmod(0o700)
            helper.persist(str(root), {"fixture": True})
            dest = root/"test-crew-links/interruption-receipt.v1.json"
            self.assertEqual(stat.S_IMODE(dest.stat().st_mode), 0o600)
            with self.assertRaises(FileExistsError):
                helper.persist(str(root), {"changed": True})
            self.assertEqual(json.loads(dest.read_bytes()), {"fixture": True})
            dest.unlink()
            dest.symlink_to(root/"unowned")
            with self.assertRaises(FileExistsError):
                helper.persist(str(root), {"changed": True})
            self.assertFalse((root/"unowned").exists())


class TimeoutProcessTests(unittest.TestCase):
    def fixture(self, directory, pause_before_signal_mask=False):
        # Optional baseline path lets the SAME assertion demonstrate RED on the
        # previous source without weakening the expected retained proof.
        source = Path(os.environ.get("HARNESS_FIXTURE_SOURCE", str(HERE)))
        root = directory/"repo"
        harness = root/"scripts/test-harness"
        harness.mkdir(parents=True)
        for name in ("run-all.sh", "lib.sh", "test-crew-links.sh", "crew-links-evidence.sh", "crew-links-receipt.py", "crew-links-interrupted.py"):
            if (source/name).exists():
                (harness/name).write_bytes((source/name).read_bytes())
        # Other suites are deliberately inert: the timeout must not advance to
        # any suite AFTER the interrupted real crew-links implementation.
        for name in ("test-memory.sh", "test-delegation.sh"):
            (harness/name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (harness/"test-crew-lifecycle.sh").write_text('printf "next-suite\\n" >> "$TRACE"\n')
        fixture_source = (HERE.parent/"test-harness-lib-test.sh").read_text()
        fixture_source = fixture_source[fixture_source.index("xlink_cli() {"):fixture_source.index("\nrun_crew_links()")]
        cli = directory/"crewship"
        subprocess.run(["bash", "-c", fixture_source+'\nxlink_cli "$1"', "fixture", str(cli)], check=True)
        text = cli.read_text()
        text = text.replace('  *" crew connect "*) rm -f "$state/unlinked"; exit 0 ;;', '''  *" crew connect "*)
    if [ -e "$state/unlinked" ]; then
      [ -f "$HARNESS_DETAIL_DIR/test-crew-links/interruption-receipt.v1.json" ] && printf 'receipt-before-restore\\n' >> "$TRACE"
      printf 'restore\\n' >> "$TRACE"
    fi
    rm -f "$state/unlinked"; exit 0 ;;''')
        text = text.replace('    tag="$(grep -oE', '''    if [ -e "$state/unlinked" ]; then
      printf 'blocked\\n' >> "$TRACE"
      printf '%s\\n' "$$" > "$state/blocked-pid"
      sleep 60
    fi
    tag="$(grep -oE''')
        text = text.replace('  *" issue comments "*|*" issue get "*) exit 0 ;;', '''  *" issue get "*)
    id="${args##* issue get }"; id="${id%% *}"
    printf '{"id":"owned-%s","identifier":"%s","description":"SECRET-CANARY"}\\n' "$id" "$id"; exit 0 ;;
  *" issue comments "*) exit 0 ;;''')
        cli.write_text(text)
        cli.chmod(0o700)
        if pause_before_signal_mask:
            # Pause the actual EXIT handler before its signal mask. The real
            # timeout signals the group and run-all also forwards to its child;
            # make that legal second delivery deterministic, not a timing bet.
            suite = harness/"test-crew-links.sh"
            text = suite.read_text()
            needle = "  trap - EXIT\n"
            self.assertEqual(text.count(needle), 1)
            suite.write_text(text.replace(needle, needle + '''  printf 'ready\\n' > "$XLINK_STATE/exit-ready"
  while [[ ! -f "$XLINK_STATE/exit-continue" ]]; do sleep 0.01; done
'''))
        # A genuine committed fixture exercises the real source-binding helper.
        for argv in (["git", "init", "-q", str(root)], ["git", "-C", str(root), "add", "scripts"],
                     ["git", "-C", str(root), "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"]):
            subprocess.run(argv, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        sha = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
        detail = directory/"detail"
        detail.mkdir(mode=0o700)
        state = directory/"state"
        state.mkdir()
        owned_tmp = directory/"owned-tmp"
        owned_tmp.mkdir()
        trace = directory/"trace"
        env = {**os.environ, "CREWSHIP": str(cli), "CREWSHIP_SERVER": "http://fixture.invalid", "SERVER": "http://fixture.invalid",
               "XLINK_STATE": str(state), "XLINK_MODE": "landed", "TRACE": str(trace), "HARNESS_DETAIL_DIR": str(detail),
               "INGEST_SHA": sha, "SCAN_RUN_ID": "fixture", "STAGE_RUN_ID": "fixture", "CREWSHIP_WORKSPACE": "fixture",
               "ASK_TIMEOUT": "3", "POLL_INTERVAL": "1", "WITH_OLLAMA": "0", "TMPDIR": str(owned_tmp)}
        return root, harness, state, trace, detail, env

    def test_repeated_term_during_exit_preserves_receipt_before_restore(self):
        # First calibrate the barrier under one TERM; only a second TERM differs.
        for second_term in (False, True):
            with self.subTest(second_term=second_term), tempfile.TemporaryDirectory() as temp:
                root, harness, state, trace, detail, env = self.fixture(Path(temp), pause_before_signal_mask=True)
                child = subprocess.Popen(["bash", str(harness/"test-crew-links.sh")], env=env,
                                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
                try:
                    until = time.monotonic()+5
                    while not (state/"blocked-pid").exists() and time.monotonic() < until:
                        time.sleep(0.01)
                    self.assertTrue((state/"blocked-pid").exists(), "fixture never reached the owned negative request")
                    os.killpg(child.pid, signal.SIGTERM)
                    until = time.monotonic()+5
                    while not (state/"exit-ready").exists() and time.monotonic() < until:
                        time.sleep(0.01)
                    self.assertTrue((state/"exit-ready").exists(), "EXIT handler barrier not reached")
                    if second_term:
                        os.kill(child.pid, signal.SIGTERM)
                    # A durable sentinel cannot be missed if the child has not
                    # entered its wait yet. No FIFO open/no-reader race. The
                    # existing20s parent wait/finally bounds only this owned group.
                    (state/"exit-continue").touch()
                    self.assertEqual(child.wait(timeout=20), 143)
                    receipt = detail/"test-crew-links/interruption-receipt.v1.json"
                    self.assertTrue(receipt.is_file(), "receipt missing after repeated TERM at EXIT entry")
                    value = json.loads(receipt.read_bytes())
                    self.assertEqual(value["original_exit_status"], 143)
                    self.assertTrue(value["complete"])
                    self.assertEqual(value["owned_issue_count"], 3)
                    self.assertEqual(trace.read_text().splitlines(), ["blocked", "receipt-before-restore", "restore"])
                    self.assertFalse((state/"unlinked").exists())
                finally:
                    if child.poll() is None:
                        os.killpg(child.pid, signal.SIGKILL)
                        child.wait()

    def test_actual_timeout_preserves_receipt_restore_reset_order_and124(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            root, harness, state, trace, detail, env = self.fixture(directory)
            result = subprocess.run(["timeout", "--signal=TERM", "--kill-after=20s", "5s", "bash", str(harness/"run-all.sh")],
                                    env=env, capture_output=True, timeout=27)
            with trace.open("a") as output:
                output.write("reset-marker\n")
            self.assertEqual(result.returncode, 124)
            self.assertIn("blocked", trace.read_text())  # real negative path was reached
            receipt = detail/"test-crew-links/interruption-receipt.v1.json"
            self.assertTrue(receipt.is_file(), "interrupted owned issues must survive before reset")
            value = json.loads(receipt.read_bytes())
            self.assertEqual(value["original_exit_status"], 143)
            self.assertTrue(value["complete"])
            self.assertEqual(value["owned_issue_count"], 3)
            self.assertEqual(value["capture_stage"], "before_topology_restore")
            self.assertNotIn("SECRET-CANARY", receipt.read_text())
            events = trace.read_text().splitlines()
            self.assertEqual(events, ["blocked", "receipt-before-restore", "restore", "reset-marker"])
            self.assertFalse((state/"unlinked").exists())
            self.assertEqual(list((directory/"owned-tmp").glob("harness-state.*")), [])
            self.assertNotIn(b"SECRET-CANARY", result.stdout+result.stderr)
            blocked_pid = int((state/"blocked-pid").read_text())
            self.assertFalse(Path("/proc", str(blocked_pid)).exists(), "owned interrupted CLI must not be orphaned")
            self.assertEqual(stat.S_IMODE(receipt.stat().st_mode), 0o600)
            self.assertFalse((detail/"test-crew-links/success-receipt.v1.json").exists())

    def test_direct_interrupt_preserves130_and_restore_even_capture_failure(self):
        for broken_capture, use_parent in ((False, False), (True, False), (False, True)):
            with self.subTest(broken_capture=broken_capture, use_parent=use_parent), tempfile.TemporaryDirectory() as temp:
                directory = Path(temp)
                root, harness, state, trace, detail, env = self.fixture(directory)
                if broken_capture:
                    # Wrong owner-mode is a genuine persistence refusal; it must
                    # not prevent the original topology restoration or130.
                    detail.chmod(0o755)
                child = subprocess.Popen(["bash", str(harness/("run-all.sh" if use_parent else "test-crew-links.sh"))], env=env,
                                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
                try:
                    until = time.monotonic()+5
                    while time.monotonic() < until and not (state/"blocked-pid").exists():
                        time.sleep(0.02)
                    self.assertTrue((state/"blocked-pid").exists())
                    os.killpg(child.pid, signal.SIGINT)
                    self.assertEqual(child.wait(timeout=20), 130)
                    events = trace.read_text().splitlines()
                    self.assertIn("restore", events)
                    self.assertFalse((state/"unlinked").exists())
                    self.assertEqual(list((directory/"owned-tmp").glob("harness-state.*")), [])
                    receipt = detail/"test-crew-links/interruption-receipt.v1.json"
                    self.assertEqual(receipt.exists(), not broken_capture)
                    if receipt.exists():
                        self.assertEqual(json.loads(receipt.read_bytes())["original_exit_status"], 130)
                finally:
                    if child.poll() is None:
                        os.killpg(child.pid, signal.SIGKILL)
                        child.wait()


if __name__ == "__main__":
    unittest.main()
