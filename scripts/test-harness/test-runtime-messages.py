#!/usr/bin/env python3
"""CLI acceptance driver for an OWNED, preprovisioned offline Messages fixture.

Does not start/seed servers, alter production credentials, or call providers.
--emit-scenario prepares a synthetic script for the test-built sidecar; its
exact advertised MCP tool names must be verified against the pinned Claude CLI.
Successful execution proves mention dispatch and delegation, not deferred queue
draining, independent cross-crew isolation, or live-provider compatibility.
"""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
from urllib.parse import urlsplit


class Failure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Failure(message)


class CLI:
    def __init__(self, binary, config, manifest=None, timeout=30):
        self.binary = str(Path(binary).resolve(strict=True))
        self.config = str(Path(config).resolve(strict=True))
        self.timeout = timeout
        self.deadline = None
        # Accept only the legacy, explicit loopback config generated for an
        # owned throwaway instance. Named/live profiles are outside this driver.
        config_text = Path(self.config).read_text()
        require(not re.search(r"^(?:current|servers):\s*\S", config_text, re.MULTILINE),
                "fixture requires an explicit single-server config")
        server = re.search(r"^server:\s*([^\n]+)$", config_text, re.MULTILINE)
        require(server is not None, "fixture config lacks explicit server")
        target = urlsplit(server[1].strip().strip("\"'"))
        require(target.scheme == "http" and target.hostname in ("127.0.0.1", "localhost", "::1")
                and target.port is not None and target.port != 8084 and not target.username
                and not target.query and not target.fragment and target.path in ("", "/"),
                "fixture requires an owned loopback server; stage is forbidden")
        require(manifest is not None, "explicit throwaway ownership manifest is required")
        manifest_path = Path(manifest).resolve(strict=True)
        owner_dir = manifest_path.parent
        require(manifest_path.name == "manifest.json"
                and Path(self.config) == owner_dir / "cli-config.yaml"
                and manifest_path.stat().st_uid == os.getuid()
                and owner_dir.stat().st_mode & 0o022 == 0,
                "config and manifest must belong to the same owned throwaway directory")
        try:
            owned = json.loads(manifest_path.read_text())
            port, pid = int(owned.get("port", 0)), int(owned.get("pid", 0))
        except (ValueError, TypeError, AttributeError) as exc:
            raise Failure("malformed throwaway ownership manifest") from exc
        name = owned.get("name", "")
        require(isinstance(name, str) and re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", name)
                and owned.get("state") == "running"
                and isinstance(owned.get("instance_id"), str) and owned["instance_id"]
                and isinstance(owned.get("prefix"), str)
                and owned["prefix"].startswith(f"crewship-tw-{name}-")
                and target.hostname == "127.0.0.1"
                and port == target.port and port != 8084 and pid > 1,
                "manifest does not identify a running owned throwaway target")
        require(Path(str(owned.get("binary", ""))).resolve(strict=True) == Path(self.binary)
                and Path(str(owned.get("data_dir", ""))).resolve(strict=True) == owner_dir / "data"
                and Path(str(owned.get("database", ""))).resolve() == owner_dir / "data" / "crewship.db",
                "manifest binary/data ownership does not match this fixture")
        try:
            os.kill(pid, 0)  # Read-only existence check; no signal is delivered.
        except OSError as exc:
            raise Failure("owned throwaway process is unavailable") from exc

    def text(self, *args):
        env = os.environ.copy()
        for name in ("CREWSHIP_SERVER", "CREWSHIP_TOKEN", "CREWSHIP_WORKSPACE", "CREWSHIP_PROFILE"):
            env.pop(name, None)
        env.update(CREWSHIP_CONFIG=self.config, NO_COLOR="1")
        timeout = self.timeout
        if self.deadline is not None:
            timeout = min(timeout, self.deadline - time.monotonic())
            require(timeout > 0, "overall runtime acceptance deadline exceeded")
        try:
            result = subprocess.run([self.binary, *args], env=env, text=True,
                                    capture_output=True, timeout=timeout, check=False)
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise Failure(f"CLI {args[0]} failed or timed out") from exc
        # Never dump arbitrary CLI stdout/stderr: errors may include credentials.
        require(result.returncode == 0, f"CLI {' '.join(args[:2])} returned {result.returncode}")
        require(len(result.stdout) <= 4 << 20, "CLI output exceeds acceptance bound")
        return result.stdout

    def json(self, *args):
        try:
            return json.loads(self.text(*args, "-f", "json"))
        except ValueError as exc:
            raise Failure(f"CLI {args[0]} returned malformed JSON") from exc


def identities(cli, args):
    issue = cli.json("issue", "get", args.issue)
    crew = cli.json("crew", "get", args.crew)
    lead = cli.json("agent", "get", args.lead)
    peer = cli.json("agent", "get", args.peer)
    require(all(isinstance(row, dict) and row.get("id") for row in (issue, crew, lead, peer)),
            "fixture identity response lacks id")
    require(issue.get("crew_id") == crew["id"], "issue belongs to another crew")
    require(lead["id"] != peer["id"], "lead and peer must be distinct")
    for row, slug in ((lead, args.lead), (peer, args.peer)):
        require(row.get("slug") == slug and isinstance(row.get("crew"), dict)
                and row["crew"].get("slug") == args.crew, "agent does not belong to fixture crew")
        require(row.get("cli_adapter") == "CLAUDE_CODE", "fixture must use actual Claude adapter")
    require(str(lead.get("agent_role", "")).upper() == "LEAD", "fixture lead lacks lead role")
    return issue, crew, lead, peer


def diagnostics(slug, agent_id, nonce, key):
    # Print only a nonce after auditing actual process values. No token values
    # enter argv/output. Only the expected upstream key HASH reaches the agent.
    return ("set -eu\n"
            "test \"$(id -u)\" = 1001\n"
            f"test \"${{CREWSHIP_AGENT_ID:-}}\" = {shlex.quote(agent_id)}\n"
            f"case \"${{HOME:-}}\" in /crew/runs/{slug}/*) ;; *) exit 41;; esac\n"
            "test \"${ANTHROPIC_BASE_URL:-}\" = http://127.0.0.1:9119\n"
            "test -n \"${ANTHROPIC_API_KEY:-}\"\n"
            "actual_hash=$(printf '%s' \"$ANTHROPIC_API_KEY\" | sha256sum)\n"
            f"test \"${{actual_hash%% *}}\" != {hashlib.sha256(key.encode()).hexdigest()}\n"
            'case "$ANTHROPIC_API_KEY" in sk-ant-dummy-crewship-sidecar*) ;; *) exit 42;; esac\n'
            "test -n \"${CREWSHIP_AGENT_TOKEN:-}\"\n"
            f"printf '%s\\n' {shlex.quote('ACTOR_' + slug + '_' + nonce)}")


def scenario(args, lead, peer):
    key = args.synthetic_key
    marker = lambda role: f"FIXTURE:{args.nonce}:{role}"
    actor = lambda slug: f"ACTOR_{slug}_{args.nonce}"
    steps = []

    def step(role, **fields):
        steps.append(dict(Role=role, MatchMessage=marker(role), **fields))

    def bash(role, ident, command, **fields):
        step(role, ToolName=args.bash_tool, ToolUseID=f"tool_{role}_{ident}",
             ToolArgs={"command": command}, **fields)

    bash("lead", "actor", diagnostics(args.lead, lead["id"], args.nonce, key))
    step("lead", ToolName=args.memory_write_tool, ToolUseID="tool_lead_write",
         ToolArgs={"tier": "CREW", "mode": "append", "content": args.nonce,
                   "operation_id": f"runtime-{args.nonce}"}, RequireResult=actor(args.lead))
    body = json.dumps({"target": args.peer, "task": marker("peer")}, separators=(",", ":"))
    # fd3 authentication is the production exec.go recipe. Agent token is
    # expanded into a descriptor, never an argument or a persisted file.
    delegate = ("set -eu\ncurl --fail --silent --show-error -X POST http://localhost:9119/assign "
                "-H 'Content-Type: application/json' --data @- -K /dev/fd/3 3<<AUTH <<'BODY'\n"
                'header = "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"\n'
                f"AUTH\n{body}\nBODY\nprintf '%s\\n' DELEGATED_{args.nonce}\n")
    bash("lead", "delegate", delegate)
    step("lead", Text=f"LEAD_DONE_{args.nonce}", RequireResult=f"DELEGATED_{args.nonce}")
    bash("peer", "actor", diagnostics(args.peer, peer["id"], args.nonce, key))
    step("peer", ToolName=args.memory_read_tool, ToolUseID="tool_peer_read",
         ToolArgs={"tier": "CREW"}, RequireResult=actor(args.peer))
    step("peer", Text=f"PEER_DONE_{args.nonce}", RequireResult=args.nonce)
    return dict(ExpectedKey=key, Model=args.model, Script=steps)


def select_runs(rows, issue_id, lead_id, peer_id, nonce):
    require(isinstance(rows, list), "issue runs must be a JSON array")
    selected = {}
    for role, agent_id, source in (("lead", lead_id, "mention"), ("peer", peer_id, "delegation")):
        marker = f"FIXTURE:{nonce}:{role}"
        matches = [r for r in rows if isinstance(r, dict) and marker in str(r.get("task", ""))]
        require(len(matches) <= 1, f"duplicate nonce-correlated {role} assignment")
        if not matches:
            continue
        row = matches[0]
        require(row.get("agent_id") == agent_id and row.get("mission_id") == issue_id
                and row.get("source") == source and row.get("id"),
                f"{role} assignment identity/source/issue mismatch")
        require(str(row.get("status", "")).upper() not in ("FAILED", "CANCELLED", "CANCELED"),
                f"{role} assignment failed")
        if str(row.get("status", "")).upper() == "COMPLETED":
            require(row.get("run_id"), f"{role} completed assignment lacks actual run identity")
        selected[role] = row
    if len(selected) == 2:
        require(selected["lead"]["id"] != selected["peer"]["id"], "duplicate assignment identity")
    return selected


def lifecycle(entries, row, crew_id, actor_id=None):
    require(isinstance(entries, list), "activity/journal must be a JSON array")
    required = {"assignment.created", "assignment.running", "assignment.completed"}
    found = {}
    for entry in entries:
        if not isinstance(entry, dict):
            continue
        payload = entry.get("payload") or {}
        if payload.get("assignment_id") != row["id"]:
            continue
        kind = entry.get("entry_type")
        require(kind != "assignment.failed", "correlated assignment has failed lifecycle")
        if kind not in required:
            continue
        require(entry.get("crew_id") == crew_id and entry.get("agent_id") == row["agent_id"],
                "lifecycle crew/target identity mismatch")
        if kind == "assignment.created":
            require(payload.get("task") == row["task"] and payload.get("target_id") == row["agent_id"],
                    "assignment.created is not correlated to actual task/target")
            if actor_id:
                require(entry.get("actor_id") == actor_id, "delegation created by another actor")
        found[kind] = entry
    require(required <= found.keys(), "missing created/running/completed lifecycle")


def verify(cli, args, issue, crew, lead, peer):
    deadline = time.monotonic() + args.timeout
    cli.deadline = deadline
    started = datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
    baseline = cli.json("issue", "runs", args.issue, "--limit", "100")
    require(isinstance(baseline, list) and all(isinstance(r, dict) for r in baseline),
            "baseline issue runs must be an array of records")
    require(not any(f"FIXTURE:{args.nonce}:" in str(r.get("task", "")) for r in baseline),
            "nonce already exists in fixture runs")
    old_versions = cli.json("memory", "versions", "list", args.memory_path, "--limit", "100")
    require(isinstance(old_versions, dict) and isinstance(old_versions.get("entries"), list),
            "baseline memory version response malformed")
    old_hashes = {v.get("sha256") for v in old_versions["entries"] if isinstance(v, dict)}
    cli.text("issue", "comment", args.issue, "--mention", args.lead,
             "--body", f"FIXTURE:{args.nonce}:lead")
    while True:
        rows = cli.json("issue", "runs", args.issue, "--limit", "100")
        selected = select_runs(rows, issue["id"], lead["id"], peer["id"], args.nonce)
        if len(selected) == 2 and all(str(r.get("status", "")).upper() == "COMPLETED" for r in selected.values()):
            break
        require(time.monotonic() < deadline, "timed out waiting for lead and delegated peer completion")
        time.sleep(min(args.poll_interval, max(0, deadline - time.monotonic())))
    for role, row in selected.items():
        result = cli.json("issue", "result", args.issue, row["id"])
        require(isinstance(result, dict) and str(result.get("status", "")).upper() == "COMPLETED"
                and f"{role.upper()}_DONE_{args.nonce}" in str(result.get("result_summary", "")),
                f"{role} CLI terminal result missing")
    activity = cli.json("activity", "--crew", args.crew, "--since", started, "--lines", "500")
    journal = cli.json("journal", "--mission", issue["id"], "--since", started, "--lines", "500")
    for row in selected.values():
        actor = lead["id"] if row["agent_id"] == peer["id"] else None
        lifecycle(activity, row, crew["id"], actor)
        lifecycle(journal, row, crew["id"], actor)
    versions = cli.json("memory", "versions", "list", args.memory_path, "--limit", "100")
    require(isinstance(versions, dict) and versions.get("path") == args.memory_path
            and isinstance(versions.get("entries"), list) and versions["entries"],
            "persisted memory version missing")
    for version in versions["entries"]:
        sha = version.get("sha256", "")
        require(re.fullmatch(r"[a-f0-9]{64}", sha) is not None, "memory version lacks content hash")
        if sha in old_hashes:
            continue
        content = cli.text("memory", "versions", "show", args.memory_path, sha)
        require(hashlib.sha256(content.encode("utf-8")).hexdigest() == sha,
                "memory version content does not match its stored hash")
        if args.nonce in content:
            return {"status": "cli state verified", "runtime_acceptance_passed": False,
                    "requires": "owned runner must verify successful sidecar exit and fixture completion",
                    "lead_assignment": selected["lead"]["id"],
                    "peer_assignment": selected["peer"]["id"], "memory_sha256": sha,
                    "coverage": "mention dispatch, real tool diagnostics, memory write/read, delegation",
                    "deferred_queue_draining": "not tested"}
    raise Failure("CLI memory version contents do not contain nonce")


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("binary", "config", "manifest", "issue", "lead", "peer", "crew", "nonce", "memory-path"):
        p.add_argument("--" + name, required=True)
    p.add_argument("--timeout", type=float, default=180)
    p.add_argument("--poll-interval", type=float, default=1)
    p.add_argument("--emit-scenario", type=Path)
    p.add_argument("--model")
    p.add_argument("--memory-write-tool")
    p.add_argument("--memory-read-tool")
    p.add_argument("--bash-tool", default="Bash")
    p.add_argument("--synthetic-key", default="synthetic-runtime-fixture-key")
    return p


def main(argv=None):
    args = parser().parse_args(argv)
    try:
        for value in (args.lead, args.peer, args.crew, args.nonce):
            require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", value) is not None,
                    "fixture slugs and nonce require bounded safe identifiers")
        require(0 < args.timeout <= 600 and 0 < args.poll_interval <= 10, "invalid acceptance time bound")
        require(args.synthetic_key.startswith("synthetic-"), "only a synthetic fixture key is permitted")
        cli = CLI(args.binary, args.config, args.manifest, timeout=min(30, args.timeout))
        issue, crew, lead, peer = identities(cli, args)
        if args.emit_scenario:
            require(args.model and args.memory_write_tool and args.memory_read_tool,
                    "scenario requires pinned model and verified explicit MCP tool names")
            # Refuse replacing operator files. These are synthetic fixtures only.
            with args.emit_scenario.open("x") as handle:
                json.dump(scenario(args, lead, peer), handle, indent=2)
                handle.write("\n")
            print(json.dumps({"status": "scenario prepared", "runtime_executed": False,
                              "requires": "verify pinned Claude tool names/protocol; build owned offline sidecar"}))
        else:
            print(json.dumps(verify(cli, args, issue, crew, lead, peer)))
        return 0
    except (Failure, OSError, ValueError) as exc:
        # Errors above contain neither arbitrary CLI output nor credentials.
        print(f"runtime Messages fixture failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
