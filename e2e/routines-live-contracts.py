#!/usr/bin/env python3
"""Run deterministic recipes against a real server through the installed CLI.

Uses a disposable workspace; never changes the active CLI profile. No models,
external HTTP services or private repository access. See --help for execution.
"""
import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import parse_qs, urlsplit
import time
import uuid


class DependencyBlocked(Exception):
    pass


def transform(value, expression=".", step_id="result", **extra):
    return dict(id=step_id, type="transform", transform=dict(input=json.dumps(value), expression=expression), **extra)


def recipes():
    cases = []
    def add(title, steps, expected, **dsl):
        cases.append(dict(title=title, definition=dict(agentless=True, steps=steps, **dsl), expected=str(expected)))
    for expression, value, expected in [
        (".a", {"a": 4}, "4"), (".a.b", {"a": {"b": "nested"}}, "nested"),
        (".[1]", [2, 7], "7"), (".a[0].b", {"a": [{"b": 11}]}, "11"),
        (".[0][1]", [[3, 9]], "9"), (".dashed-key", {"dashed-key": 13}, "13")]:
        add("path " + expression, [transform(value, expression)], expected)
    for expression, value, expected in [
        ("length", [1, 2, 3], "3"), ("length", {"a": 1, "b": 2}, "2"),
        ("length", "hello", "5"), ("keys", {"b": 2, "a": 1}, '["a","b"]'),
        ("tostring", True, "true"), ("@json", {"b": 2, "a": 1}, '{"a":1,"b":2}')]:
        add("builtin " + expression, [transform(value, expression)], expected)
    for i in range(8):
        expression = f"CREWSHIP_INPUT_NUMBER >= {i}"
        add("expr typed input comparison " + str(i), [dict(id="result", type="code", code=dict(runtime="expr", code=expression))], "true", inputs=[dict(name="number", type="integer", default=i)])
    for i in range(8):
        add("cel arithmetic " + str(i), [dict(id="result", type="code", code=dict(runtime="cel", code=f"{i} * 3 + 2"))], i * 3 + 2)
    for i in range(8):
        values = list(range(i))
        each = dict(id="result", type="foreach", foreach=dict(items="{{ inputs.values }}", **{"as": "item"}, parallelism=(i % 3) + 1, steps=[dict(id="echo", type="transform", transform=dict(input="{{ inputs.item }}", expression="."))]))
        add("foreach ordered size " + str(i), [each], json.dumps(values, separators=(",", ":")), inputs=[dict(name="values", type="array", default=values)])
    for i in range(8):
        join = dict(id="result", type="code", needs=["left", "right"], code=dict(runtime="cel", code="{{ steps.left.output }} + {{ steps.right.output }}"))
        add("DAG fork and join " + str(i), [transform(i, step_id="left"), transform(8-i, step_id="right"), join], 8)
    for i in range(6):
        # The output exposes whether the gated step actually ran.
        gated = transform(i, step_id="gate", **{"if": "inputs.go == true"})
        result = dict(id="result", type="transform", transform=dict(input="{{ steps.gate.output }}", expression="."))
        add("conditional " + str(i), [gated, result], i if i % 2 else "", inputs=[dict(name="go", type="boolean", default=bool(i % 2))])
    assert len(cases) == 50
    return cases


class Suite:
    def __init__(self, args):
        self.args = args
        self.prefix = "t-rtn-" + uuid.uuid4().hex[:10]
        self.workspace = ""
        self.crew = ""
        self.results = []
        self.request_evidence = []
        self.slug_by_id = {}
        self.last_evidence = {}
        self.pending = []
        self.deferred = []
        self.member_config = None
        self.member_tmp = None
        self.extra_workspaces = []
        self.test_user_id = None
        self.member_password = None
        self.output = Path(args.output)
        self.output.mkdir(parents=True, exist_ok=False)

    def cli(self, *args, data=None, member=False):
        argv = [self.args.binary, "--server", self.args.server, "-f", "json"]
        if self.args.profile and not member:
            argv += ["--profile", self.args.profile]
        if self.workspace:
            argv += ["--workspace", self.workspace]
        env = dict(os.environ)
        if member:
            assert self.member_config is not None, "test member not initialized"
            env["CREWSHIP_CONFIG"] = str(self.member_config)
            env.pop("CREWSHIP_PROFILE", None)
            env.pop("CREWSHIP_TOKEN", None)
        try:
            return subprocess.run(argv + list(args), input=data, capture_output=True, text=True, timeout=45, env=env)
        except FileNotFoundError:
            raise DependencyBlocked("Crewship CLI binary is not installed") from None

    def api(self, method, path, body=None, expected=(200,), key=None, query=None, member=False):
        args = ["api", "request", method, path, "--include"]
        if method != "GET":
            args += ["--yes"]
        if body is not None:
            args += ["--input", "-"]
        for name, value in (query or {}).items():
            args += ["--query", f"{name}={value}"]
        if key:
            args += ["--idempotency-key", key]
        proc = self.cli(*args, data=json.dumps(body) if body is not None else None, member=member)
        if proc.returncode:
            # CLI deliberately emits no response JSON on errors. Only record the
            # status; raw bodies may contain sensitive workspace data.
            match = re.search(r"\b(4\d\d|5\d\d)\b", proc.stderr)
            status = int(match.group()) if match else 0
            self.request_evidence.append(dict(method=method, path=path, status=status, actor="fixture_member" if member else "owner"))
            if status in expected:
                return None
            if status == 401:
                raise DependencyBlocked("CLI authentication is unavailable for this server/profile")
            raise AssertionError(f"{method} {path}: CLI exit {proc.returncode}, HTTP {status}; check authentication, server binding and permissions")
        response = json.loads(proc.stdout)
        self.request_evidence.append(dict(method=method, path=path, status=response["status"], actor="fixture_member" if member else "owner"))
        assert response["status"] in expected, f"{method} {path}: HTTP {response['status']}"
        return response["body"]

    def record(self, test_id, title, fn):
        start = time.monotonic()
        self.request_evidence = []
        entry = dict(test_id=test_id, title=title, execution_mode="REAL", result="NOT_RUN", expected_current="Public execution and persistence contract", expected_desired=title)
        try:
            entry["evidence"] = fn() or {}
            self.last_evidence[test_id] = entry["evidence"]
            entry["result"] = "PASS"
        except DependencyBlocked as exc:
            entry.update(result="BLOCKED", reason=str(exc))
        except (AssertionError, KeyError, ValueError, subprocess.TimeoutExpired) as exc:
            entry.update(result="FAIL", error=str(exc))
        entry["requests"] = list(self.request_evidence)
        entry["duration_ms"] = round(1000 * (time.monotonic() - start))
        self.results.append(entry)
        self.persist()
        print(test_id, entry["result"], flush=True)

    def persist(self):
        report = dict(server=self.args.server, prefix=self.prefix, workspace_id=self.workspace, scenarios=self.results,
                      coverage_note=("Role and workspace isolation checks with a synthetic account; models excluded; browser checks optional." if self.args.bucket == "access" else ("Complex foreach and deferred priority contention; no agent or external business effects." if self.args.bucket == "workflows" else "50 deterministic recipes plus repetitions and admission checks. Models, browser, integrations, credentials, RBAC and other API domains require separate buckets.")))
        if self.test_user_id:
            report["retained_test_account"] = self.test_user_id
        successful = [x for x in self.results if x["result"] == "PASS"]
        verified = set()
        for entry in successful:
            evidence = entry.get("evidence", {})
            if evidence.get("run_id"):
                verified.add(evidence["run_id"])
            verified.update(evidence.get("run_ids", []))
            verified.update(run["run_id"] for run in evidence.get("runs", []))
        report["verified_unique_runs"] = len(verified)
        warm = sorted(x["evidence"]["duration_ms"] for x in successful if x["test_id"].startswith("RTN-REPEAT-") and "duration_ms" in x.get("evidence", {}))
        if warm:
            report["warm_execution_ms"] = dict(samples=len(warm), p50=warm[(len(warm)-1)//2], p95=warm[max(0, (95*len(warm)+99)//100-1)])
        report["not_measured"] = ["queue latency", "Inbox delivery latency", "UI display latency"]
        tmp = self.output / "results.json.tmp"
        tmp.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        tmp.replace(self.output / "results.json")

    def save(self, slug, definition, expected=(201, 200)):
        definition = dict(definition, name=slug)
        saved = self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/save",
                         dict(slug=slug, name=slug, definition=definition, author_crew_id=self.crew, skip_test_gate=True), expected)
        if saved and saved["status"] == "proposed":
            # Review the exact known recipe before approving its governance
            # card. Never skip delivery evidence just to make a run proceed.
            assert definition["agentless"] is True
            def review(steps):
                for step in steps:
                    assert step["type"] in ("transform", "code", "foreach")
                    if step["type"] == "code":
                        assert step["code"]["runtime"] in ("expr", "cel")
                    if step["type"] == "foreach":
                        review(step["foreach"]["steps"])
            review(definition["steps"])
            source = "routineprop:" + self.workspace + ":" + slug
            feed = self.api("GET", "/api/v1/inbox", query={"state":"all", "limit":500})
            cards = [row for row in feed["rows"] if row.get("source_id") == source]
            assert len(cards) == 1 and cards[0]["state"] != "resolved", "routine review card missing from Inbox"
            card_id = cards[0]["id"]
            approval = self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/approve", {})
            assert approval["status"] == "active", "routine did not activate"
            feed = self.api("GET", "/api/v1/inbox", query={"state":"all", "limit":500})
            assert any(row["id"] == card_id and row["state"] == "resolved" for row in feed["rows"]), "review card not resolved after approval"
        return saved

    def run(self, slug, expected, key=None):
        result = self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/run", {}, key=key)
        assert result["status"] == "COMPLETED", f"routine status {result['status']}"
        assert result["output"] == str(expected), f"business output mismatch: {result['output']!r}"
        persisted = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipeline-runs/{result['run_id']}")
        assert persisted["status"].lower() == "completed", "run not durably completed"
        assert persisted["output"] == str(expected), "persisted business output mismatch"
        assert persisted["cost_usd"] == 0, "agentless run incurred model cost"
        return dict(run_id=result["run_id"], output_verified=True, persisted_verified=True, duration_ms=result["duration_ms"])

    def setup(self):
        # Auth is resolved by the CLI; this never reads or copies its token.
        ws = self.api("POST", "/api/v1/workspaces", dict(name=self.prefix, slug=self.prefix), (201,))
        self.workspace = ws["id"]
        crew = self.api("POST", "/api/v1/crews", dict(name=self.prefix, slug=self.prefix, network_mode="restricted", allowed_domains=[]), (201,))
        self.crew = crew["id"]
        self.persist()

    def cleanup(self):
        for pending_id in self.deferred:
            self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/pending/{pending_id}/cancel", {}, (200, 404))
        for run_id in self.pending:
            self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/runs/{run_id}/cancel", {}, (200, 409))
        if self.member_config:
            proc = self.cli("logout", member=True)
            assert proc.returncode == 0, "test member logout failed"
        for workspace_id, slug in self.extra_workspaces:
            proc = self.cli("workspace", "delete", workspace_id, "--confirm", slug, "--yes")
            assert proc.returncode == 0, "secondary fixture workspace cleanup failed"
        if self.workspace:
            # Exact ID plus the generated slug confirmation: only this workspace.
            proc = self.cli("workspace", "delete", self.workspace, "--confirm", self.prefix, "--yes")
            assert proc.returncode == 0, "own workspace cleanup failed"
        if self.member_tmp:
            self.member_tmp.cleanup()

    def execute(self):
        for i, case in enumerate(recipes(), 1):
            tid = f"RTN-LIVE-{i:03d}"
            slug = f"{self.prefix}-{i:03d}"
            self.slug_by_id[tid] = slug
            def execute(case=case, slug=slug):
                self.save(slug, case["definition"])
                evidence = self.run(slug, case["expected"])
                if case["title"].startswith("conditional"):
                    detail = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipeline-runs/{evidence['run_id']}")
                    go = case["definition"]["inputs"][0]["default"]
                    gate = detail["step_outputs"].get("gate")
                    assert (gate == "<skipped>") == (not go), "conditional execution evidence mismatch"
                    evidence["branch_verified"] = True
                return evidence
            self.record(tid, case["title"], execute)
        # Separate attempts, same routine: retain every measurement, never
        # count these as twenty additional recipe types.
        for i in range(20):
            self.record(f"RTN-REPEAT-{i+1:03d}", "repeat one saved routine", lambda: self.run(self.slug_by_id["RTN-LIVE-001"], "4"))
        def dedupe():
            slug = self.slug_by_id["RTN-LIVE-001"]
            key = self.prefix + "-dedupe"
            first = self.run(slug, "4", key=key)
            duplicate = self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/run", {}, key=key)
            assert duplicate["status"] == "DEDUPED", "same routine invocation executed twice"
            assert duplicate["run_id"] == first["run_id"], "dedupe returned another run"
            return dict(run_id=first["run_id"], duplicate_run_id=duplicate["run_id"], layer="routine invocation only")
        self.record("RTN-IDEMPOTENCE-001", "deduplicate repeated invocation", dedupe)
        for i, expression in enumerate(["{total: (.qty * 2)}", ".items[0foo]", ".a + .b"], 1):
            self.record(f"RTN-ADMISSION-{i:03d}", "reject invalid transform before persistence",
                        lambda expression=expression, i=i: self.save(f"{self.prefix}-invalid-{i}", dict(agentless=True, steps=[transform({"qty": 2}, expression)]), (422,)))



    def execute_workflows(self):
        for i, parallelism in enumerate([1, 2, 4, 0, 20], 1):
            values = [dict(qty=n+1, price=(n % 5)+2) for n in range(32)]
            definition = dict(agentless=True, inputs=[dict(name="products", type="array", default=values), dict(name="fee", type="integer", default=3)], steps=[
                transform(7, step_id="source"),
                dict(id="result", type="foreach", foreach=dict(items="{{ inputs.products }}", **{"as":"product"}, parallelism=parallelism, steps=[
                    dict(id="compute", type="code", code=dict(runtime="cel", code="{{ inputs.product.qty }} * {{ inputs.product.price }} + {{ steps.source.output }} + {{ inputs.fee }}")),
                    dict(id="emit", type="transform", transform=dict(input="{{ steps.compute.output }}", expression="."))]))])
            expected = json.dumps([x["qty"]*x["price"]+10 for x in values], separators=(",", ":"))
            def check(definition=definition, expected=expected, i=i):
                slug = self.prefix + "-complex-" + str(i)
                self.save(slug, definition)
                return dict(self.run(slug, expected), items_verified=32, item_order_verified=True, body_steps=2)
            self.record(f"RTN-FOREACH-COMPLEX-{i:03d}", "foreach ordered multi-step body with upstream and shared inputs", check)
        self.record("RTN-STATE-OPERATOR-001", "state write, next-run read and operator recovery", self.execute_state)
        self.record("RTN-BUDGET-MUTATION-001", "routine budget set, validation and clear without model cost", self.execute_budget)
        self.record("RTN-QUEUE-PRIORITY-001", "priority contention with 14 real deferred runs", self.execute_priority)

    def execute_state(self):
        slug = self.prefix + "-state"
        self.save(slug, dict(agentless=True, inputs=[dict(name="cursor", type="string", default="first")], steps=[
            dict(id="read", type="transform", transform=dict(input="{{ routine.state.cursor }}", expression=".")),
            dict(id="result", type="transform", transform=dict(input="{{ inputs.cursor }}", expression="."), state_write=dict(cursor="{{ steps.result.output }}"))]))
        first = self.run(slug, "first")
        base = f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/state"
        state = self.api("GET", base)
        assert any(entry["key"] == "cursor" and entry["value"] == "first" for bucket in state["buckets"] for entry in bucket["entries"]), "routine did not persist its cursor"
        second = self.run(slug, "first")
        detail = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipeline-runs/{second['run_id']}")
        assert detail["step_outputs"]["read"] == "first", "next run did not read prior state"
        self.api("PUT", base+"/cursor", dict(value="repaired"))
        third = self.run(slug, "first")
        detail = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipeline-runs/{third['run_id']}")
        assert detail["step_outputs"]["read"] == "repaired", "operator recovery not visible to next run"
        self.api("DELETE", base+"/cursor", {})
        self.api("DELETE", base+"/cursor", {}, (404,))
        self.api("PUT", base+"/other", dict(value="temporary"))
        cleared = self.api("DELETE", base, {})
        assert cleared["removed"] == 1 and self.api("GET", base)["buckets"] == [], "state clear did not clear manual bucket"
        return dict(run_ids=[first["run_id"],second["run_id"],third["run_id"]], cursor_write_verified=True, next_run_read_verified=True, operator_recovery_verified=True)

    def execute_budget(self):
        slug = self.prefix + "-budget"
        self.save(slug, dict(agentless=True, steps=[transform(5)]))
        base = f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/budget"
        initial = self.api("GET", base)
        assert not initial["has_budget"] and initial["spent_usd"] == 0
        self.api("PATCH", base, dict(monthly_budget_usd=-1), (400,))
        self.api("PATCH", base, dict(monthly_budget_usd=2))
        budget = self.api("GET", base)
        assert budget["has_budget"] and budget["monthly_budget_usd"] == 2
        evidence = self.run(slug, "5")
        budget = self.api("GET", base)
        assert budget["spent_usd"] == 0 and not budget.get("over_budget",False)
        self.api("PATCH", base, dict(monthly_budget_usd=0))
        assert not self.api("GET", base)["has_budget"]
        return dict(evidence, budget_mutations_verified=True, enforcement_with_paid_spend="NOT_RUN")

    def execute_priority(self):
        # Twelve worker slots is a code hypothesis tested here, not a completion
        # ordering guarantee. Hold each first-wave slot for twenty seconds.
        slug = self.prefix + "-priority"
        self.save(slug, dict(agentless=True, inputs=[dict(name="index", type="integer"), dict(name="until", type="string")], steps=[
            dict(id="hold", type="wait", wait=dict(kind="datetime", until="{{ inputs.until }}")),
            dict(id="result", type="transform", transform=dict(input="{{ inputs.index }}", expression="."))]))
        fire_at = datetime.now(timezone.utc) + timedelta(seconds=12)
        until = fire_at + timedelta(seconds=20)
        receipts = []
        for index in range(14):
            priority = 10 if index >= 12 else 0
            receipt = self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/run", dict(fire_at=fire_at.isoformat(), priority=priority, inputs=dict(index=index, until=until.isoformat())), (202,))
            assert receipt["status"] == "SCHEDULED" and receipt["priority"] == priority
            self.deferred.append(receipt["pending_id"])
            receipts.append(dict(index=index, priority=priority, pending_id=receipt["pending_id"]))
        assert datetime.now(timezone.utc) < fire_at, "enqueue was too slow to establish co-due priority contention"
        deadline = time.monotonic() + 65
        first_wave = None
        completed = []
        while time.monotonic() < deadline:
            rows = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipeline-runs", query={"limit":100})["rows"]
            rows = [row for row in rows if row["pipeline_slug"] == slug]
            for row in rows:
                if row["status"] not in ("completed", "failed", "cancelled") and row["id"] not in self.pending:
                    self.pending.append(row["id"])
            if len(rows) == 12 and not any(row["status"] in ("completed", "failed", "cancelled") for row in rows) and first_wave is None:
                first_wave = []
                for row in rows:
                    detail = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipeline-runs/{row['id']}")
                    first_wave.append(dict(run_id=row["id"], index=detail["inputs"]["index"], started_at=row["started_at"]))
                assert {12,13}.issubset({row["index"] for row in first_wave}), "high-priority requests did not enter the saturated first wave"
            if len(rows) == 14 and all(row["status"] in ("completed", "failed", "cancelled") for row in rows):
                for row in rows:
                    detail = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipeline-runs/{row['id']}")
                    assert detail["status"] == "completed" and detail["cost_usd"] == 0, "queued run failed or incurred cost"
                    index = detail["inputs"]["index"]
                    assert detail["output"] == str(index), "queued business output mismatch"
                    started = datetime.fromisoformat(row["started_at"].replace("Z", "+00:00"))
                    completed.append(dict(run_id=row["id"], index=index, queue_after_fire_ms=round((started-fire_at).total_seconds()*1000), duration_ms=detail["duration_ms"]))
                break
            time.sleep(0.5)
        assert len(completed) == 14 and len({x["index"] for x in completed}) == 14, "lost or duplicated queued runs"
        assert first_wave is not None, "could not observe saturated first wave; priority contention remains unverified"
        self.pending = [run_id for run_id in self.pending if run_id not in {x["run_id"] for x in completed}]
        return dict(first_wave=first_wave, runs=completed, scheduled=receipts, no_lost_or_duplicate_runs=True, ordering="priority observed at admission under saturation; completion order is not guaranteed")

    def execute_browser(self):
        self.api("POST", f"/api/v1/crews/{self.crew}/members", dict(user_id=self.test_user_id, role="MEMBER"), (201,200))
        titles = []
        run_ids = []
        for address in ("workspace", "crew:"+self.prefix):
            for priority in ("low", "medium"):
                title = self.prefix + "-" + address.split(":")[0] + "-" + priority
                titles.append(title)
                self.save(title, dict(agentless=True, steps=[dict(id="notice", type="notify", notify=dict(to=address, title=title, body=title, priority=priority))]))
                result = self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{title}/run", {})
                assert result["status"] == "COMPLETED" and result["output"].startswith("notified:"), "notification routine failed"
                run_ids.append(result["run_id"])
        feed = self.api("GET", "/api/v1/inbox", query={"state":"all", "limit":500}, member=True)
        assert all(any(row["title"] == title for row in feed["rows"]) for title in titles), "notification missing from MEMBER API"
        proc = subprocess.run(["node", str(Path(__file__).with_name("routines-live-inbox.mjs"))],
                              input=json.dumps(dict(server=self.args.server, workspace=self.workspace, email=self.prefix+"@example.test", password=self.member_password, prefix=self.prefix, titles=titles)),
                              capture_output=True, text=True, timeout=120)
        try:
            result = json.loads(proc.stdout)
        except ValueError:
            if "ERR_MODULE_NOT_FOUND" in proc.stderr or "Executable doesn't exist" in proc.stderr:
                raise DependencyBlocked("Playwright dependency or browser is not installed") from None
            raise AssertionError("browser driver failed before assertions") from None
        assert proc.returncode == 0 and result["result"] == "PASS", "browser verification failed at " + result.get("stage", "unknown")
        return dict(run_ids=run_ids, member_api_verified=True, browser=result)

    def execute_access(self):
        slug = self.prefix + "-access"
        self.save(slug, dict(agentless=True, steps=[transform(7)]))
        foreign_slug = self.prefix + "-foreign"
        foreign = self.api("POST", "/api/v1/workspaces", dict(name=foreign_slug, slug=foreign_slug), (201,))
        self.extra_workspaces.append((foreign["id"], foreign_slug))
        def provision():
            invited = self.api("POST", f"/api/v1/workspaces/{self.workspace}/members/provision",
                               dict(email=self.prefix + "@example.test", role="MEMBER", full_name="Routine contract fixture", create_only=True), (201,))
            assert invited["created_user"], "refused to reuse an existing account"
            self.test_user_id = invited["user_id"]
            token = parse_qs(urlsplit(invited["setup_url"]).query)["token"][0]
            password = "Fixture-" + uuid.uuid4().hex + "!9"
            self.member_password = password
            self.api("POST", "/api/v1/auth/reset", dict(token=token, new_password=password))
            self.member_tmp = tempfile.TemporaryDirectory(prefix=self.prefix + "-auth-")
            self.member_config = Path(self.member_tmp.name) / "config.yaml"
            self.member_config.write_text("server: " + self.args.server + "\nworkspace: " + self.workspace + "\n")
            self.member_config.chmod(0o600)
            proc = self.cli("login", "--email", self.prefix + "@example.test", "--password-stdin", data=password+"\n", member=True)
            assert proc.returncode == 0, "test account login failed"
            return dict(user_id=self.test_user_id, setup_link_consumed=True, secrets_persisted_to_report=False)
        self.record("RTN-ACCESS-SETUP", "provision and log in only a fresh synthetic account", provision)
        if self.results[-1]["result"] != "PASS":
            return
        if self.args.browser:
            self.record("RTN-UI-INBOX-001", "MEMBER sees low and medium crew and workspace messages in API and UI", self.execute_browser)
        members = self.api("GET", f"/api/v1/workspaces/{self.workspace}/members")
        membership = next(x["id"] for x in members if x.get("user_id") == self.test_user_id)
        for role in ["MEMBER", "MANAGER", "ADMIN", "VIEWER"]:
            def check(role=role):
                evidence = {}
                self.api("PATCH", f"/api/v1/workspaces/{self.workspace}/members/{membership}", dict(role=role))
                detail = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}", member=True)
                assert detail["slug"] == slug, "member read another recipe"
                if role in ("MANAGER", "ADMIN"):
                    result = self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/run", {}, member=True)
                    assert result["status"] == "COMPLETED" and result["output"] == "7", "authorized run produced wrong business result"
                    stored = self.api("GET", f"/api/v1/workspaces/{self.workspace}/pipeline-runs/{result['run_id']}", member=True)
                    assert stored["output"] == "7" and stored["status"] == "completed", "member run was not persisted"
                    evidence.update(run_id=result["run_id"], output_verified=True, persisted_verified=True)
                else:
                    self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/run", {}, (403,), member=True)
                self.api("GET", f"/api/v1/workspaces/{foreign['id']}/pipelines", expected=(403,404), query={"workspace_id":foreign["id"]}, member=True)
                if role == "ADMIN":
                    self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/disable", {}, member=True)
                    self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/enable", {}, member=True)
                else:
                    self.api("POST", f"/api/v1/workspaces/{self.workspace}/pipelines/{slug}/disable", {}, (403,), member=True)
                return dict(evidence, role=role, read_verified=True, execute_permission_verified=True, manage_permission_verified=True, workspace_isolation_verified=True)
            self.record("RTN-ACCESS-"+role, "real routine permissions and workspace isolation for "+role, check)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="crewship", help="authenticated Crewship CLI binary")
    parser.add_argument("--browser", action="store_true", help="with access bucket, verify routine messages in Playwright as MEMBER")
    parser.add_argument("--bucket", choices=["deterministic", "access", "workflows"], default="deterministic", help="access creates one synthetic account that remains after memberships are removed")
    parser.add_argument("--profile", help="explicit existing CLI profile, useful in a worktree")
    parser.add_argument("--server", required=True, help="test server URL, e.g. http://localhost:8082")
    parser.add_argument("--output", required=True, help="new directory for incremental sanitized results")
    args = parser.parse_args()
    origin = urlsplit(args.server)
    if origin.scheme not in ("http", "https") or not origin.hostname or origin.username or origin.password or origin.query or origin.fragment or origin.path not in ("", "/"):
        parser.error("--server must be an HTTP origin without credentials, query or path")
    if args.browser and args.bucket != "access":
        parser.error("--browser requires --bucket access")
    suite = Suite(args)
    try:
        suite.record("RTN-SETUP-001", "create isolated workspace and crew", suite.setup)
        if suite.results[-1]["result"] == "PASS":
            if args.bucket == "access":
                suite.execute_access()
            elif args.bucket == "workflows":
                suite.execute_workflows()
            else:
                suite.execute()
    finally:
        if suite.workspace:
            suite.record("RTN-CLEANUP-001", "delete only this suite's workspace", suite.cleanup)
    return int(any(x["result"] != "PASS" for x in suite.results))


if __name__ == "__main__":
    raise SystemExit(main())
