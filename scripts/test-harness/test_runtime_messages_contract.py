"""Driver contract tests only; these do not claim actual agent runtime coverage."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

PATH = Path(__file__).with_name("test-runtime-messages.py")
SPEC = importlib.util.spec_from_file_location("runtime_messages", PATH)
driver = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(driver)


def rows():
    return [dict(id="assignment-lead", run_id="run-lead", agent_id="agent-lead", mission_id="issue-id",
                 source="mention", status="COMPLETED", task="FIXTURE:nonce:lead"),
            dict(id="assignment-peer", run_id="run-peer", agent_id="agent-peer", mission_id="issue-id",
                 source="delegation", status="COMPLETED", task="FIXTURE:nonce:peer")]


def events(selected):
    return [dict(entry_type=kind, crew_id="crew-id", agent_id=r["agent_id"],
                 actor_id="agent-lead" if r["source"] == "delegation" else "human",
                 payload=dict(assignment_id=r["id"], task=r["task"], target_id=r["agent_id"]))
            for r in selected for kind in ("assignment.created", "assignment.running", "assignment.completed")]


class Contract(unittest.TestCase):
    def test_rejects_wrong_source_issue_actor_and_duplicate(self):
        for field, value in (("source", "mention"), ("mission_id", "other-issue"),
                             ("agent_id", "other-agent"), ("status", "FAILED"), ("run_id", "")):
            with self.subTest(field=field):
                data = rows()
                data[1][field] = value
                with self.assertRaises(driver.Failure):
                    driver.select_runs(data, "issue-id", "agent-lead", "agent-peer", "nonce")
        with self.assertRaises(driver.Failure):
            driver.select_runs(rows() + [rows()[1]], "issue-id", "agent-lead", "agent-peer", "nonce")

    def test_lifecycle_requires_side_effects_and_assigner(self):
        row = rows()[1]
        for mutation in ("missing-running", "wrong-actor", "wrong-target", "wrong-task"):
            with self.subTest(mutation=mutation):
                data = events([row])
                if mutation == "missing-running":
                    data = [e for e in data if e["entry_type"] != "assignment.running"]
                elif mutation == "wrong-actor":
                    data[0]["actor_id"] = "unrelated-agent"
                elif mutation == "wrong-target":
                    data[0]["payload"]["target_id"] = "unrelated-agent"
                else:
                    data[0]["payload"]["task"] = "unrelated-task"
                with self.assertRaises(driver.Failure):
                    driver.lifecycle(data, row, "crew-id", "agent-lead")

    def fixture_cli(self, directory, mode="pass"):
        binary = Path(directory) / "crewship-fixture"
        config = Path(directory) / "cli-config.yaml"
        (Path(directory) / "data").mkdir()
        config.write_text("server: http://127.0.0.1:18089\ntoken: synthetic-test-token\nworkspace: workspace-id\n")
        data = dict(runs=rows(), events=events(rows()), mode=mode)
        (Path(directory) / "contract.json").write_text(json.dumps(data))
        binary.write_text('''#!/usr/bin/env python3
import hashlib, json, os, pathlib, sys
p=pathlib.Path(__file__).parent
d=json.loads((p/'contract.json').read_text())
a=sys.argv[1:]
assert not any(os.environ.get(k) for k in ('CREWSHIP_SERVER','CREWSHIP_TOKEN','CREWSHIP_WORKSPACE','CREWSHIP_PROFILE'))
with (p/'calls.jsonl').open('a') as f: f.write(json.dumps(a)+'\\n')
if a[:2]==['issue','comment']: (p/'dispatched').touch(); print('Comment added')
elif a[:2]==['issue','runs']: print(json.dumps(d['runs'] if (p/'dispatched').exists() else []))
elif a[:2]==['issue','result']:
 assert a[3] in ('assignment-lead','assignment-peer')
 role=a[3].split('-')[-1]
 print(json.dumps(dict(status='COMPLETED',result_summary=role.upper()+'_DONE_nonce',outcome='completed')))
elif a[0] in ('activity','journal'): print(json.dumps(d['events']))
elif a[:3]==['memory','versions','list']:
 content='nonce\\n' if d['mode']=='pass' else 'unrelated persisted memory\\n'
 entries=[] if not (p/'dispatched').exists() else [dict(sha256=hashlib.sha256(content.encode()).hexdigest())]
 print(json.dumps(dict(path=a[3],entries=entries,count=len(entries))))
elif a[:3]==['memory','versions','show']: print('nonce' if d['mode']=='pass' else 'unrelated persisted memory')
else: raise SystemExit(2)
''')
        binary.chmod(0o755)
        manifest = Path(directory) / "manifest.json"
        manifest.write_text(json.dumps(dict(name="fixture", state="running", instance_id="synthetic-owned-id",
                                           prefix="crewship-tw-fixture-owned", port="18089", pid=str(os.getpid()),
                                           binary=str(binary), data_dir=str(Path(directory)/"data"),
                                           database=str(Path(directory)/"data"/"crewship.db"))))
        return binary, config

    def test_cli_boundary_accepts_persisted_content_and_rejects_echo_only(self):
        for mode in ("pass", "echo-only"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                binary, config = self.fixture_cli(directory, mode)
                cli = driver.CLI(binary, config, Path(directory)/"manifest.json")
                args = driver.parser().parse_args([
                    "--binary", str(binary), "--config", str(config), "--manifest", str(Path(directory)/"manifest.json"), "--issue", "FIX-1",
                    "--lead", "lead", "--peer", "peer", "--crew", "fixture", "--nonce", "nonce",
                    "--memory-path", "crew:fixture/CREW.md"])
                values = (cli, args, {"id": "issue-id"}, {"id": "crew-id"},
                          {"id": "agent-lead"}, {"id": "agent-peer"})
                if mode == "pass":
                    result = driver.verify(*values)
                    self.assertFalse(result["runtime_acceptance_passed"])
                    self.assertEqual(result["status"], "cli state verified")
                    self.assertEqual(result["peer_assignment"], "assignment-peer")
                    self.assertEqual(result["deferred_queue_draining"], "not tested")
                else:
                    with self.assertRaisesRegex(driver.Failure, "contents do not contain nonce"):
                        driver.verify(*values)
                calls = [json.loads(x) for x in (Path(directory)/"calls.jsonl").read_text().splitlines()]
                self.assertIn(["issue", "comment", "FIX-1", "--mention", "lead", "--body", "FIXTURE:nonce:lead"], calls)
                self.assertTrue(any(c[:3] == ["memory", "versions", "show"] for c in calls))

    def test_scenario_credentials_never_enter_agent_tool_arguments(self):
        args = driver.parser().parse_args([
            "--binary", "/unused", "--config", "/unused", "--manifest", "/unused", "--issue", "FIX-1",
            "--lead", "lead", "--peer", "peer", "--crew", "fixture", "--nonce", "nonce",
            "--memory-path", "crew:fixture/CREW.md", "--model", "fixture-model",
            "--memory-write-tool", "verified_write", "--memory-read-tool", "verified_read"])
        script = driver.scenario(args, {"id": "agent-lead"}, {"id": "agent-peer"})
        self.assertNotIn(script["ExpectedKey"], json.dumps(script["Script"]))
        bash = [s["ToolArgs"]["command"] for s in script["Script"] if s.get("ToolName") == "Bash"]
        self.assertTrue(all("set -eu" in command for command in bash))
        delegate = next(c for c in bash if "/assign" in c)
        self.assertIn('-K /dev/fd/3 3<<AUTH', delegate)
        self.assertNotIn('-H "Authorization', delegate)
        self.assertIn("RequireResult", script["Script"][-1])

    def test_live_profile_and_stage_are_refused_before_cli_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            binary, config = self.fixture_cli(directory)
            for text in ("server: http://127.0.0.1:8084\n", "server: https://example.com\n",
                         "current: stage\nservers: {}\nserver: http://127.0.0.1:18089\n"):
                config.write_text(text)
                with self.assertRaises(driver.Failure):
                    driver.CLI(binary, config)
            self.assertFalse((Path(directory)/"calls.jsonl").exists())

    def test_arbitrary_local_config_and_tampered_ownership_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            binary, config = self.fixture_cli(directory)
            manifest = Path(directory) / "manifest.json"
            original = json.loads(manifest.read_text())
            with self.assertRaisesRegex(driver.Failure, "ownership manifest"):
                driver.CLI(binary, config)
            for field, value in (("port", "18090"), ("state", "stopped"),
                                 ("data_dir", "/tmp"), ("instance_id", ""),
                                 ("prefix", "crewship-stage")):
                with self.subTest(field=field):
                    manifest.write_text(json.dumps(dict(original, **{field: value})))
                    with self.assertRaises(driver.Failure):
                        driver.CLI(binary, config, manifest)
            self.assertFalse((Path(directory)/"calls.jsonl").exists())


if __name__ == "__main__":
    unittest.main()
