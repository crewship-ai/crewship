#!/usr/bin/env python3
"""Bounded metadata for owned issues only; never persist response text."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import selectors
import signal
import stat
import subprocess
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
MAX_BYTES = 256 * 1024
ISSUE = re.compile(r"[A-Z][A-Z0-9]{0,15}-[1-9][0-9]{0,12}\Z")
IDENTITY = re.compile(r"[A-Za-z0-9_.:-]{1,256}\Z")
STATES = {"PENDING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELLED", "CANCELED", "STOPPED", "PAUSED", "WAITING"}
OUTCOMES = {"SUCCEEDED", "FAILED", "NO_CHANGE", "WORK_CREATED", "PARTIAL", "CANCELLED", "STOPPED"}
SOURCE_FILES = ["run-all.sh", "lib.sh", "test-crew-links.sh", "crew-links-evidence.sh", "crew-links-receipt.py", "crew-links-interrupted.py"]


def identity(value):
    return value if isinstance(value, str) and IDENTITY.fullmatch(value) else None


def command(argv, deadline):
    """Each owned process group gets <=2s and <=256KiB stdout, no stderr."""
    remaining = min(2.0, deadline - time.monotonic())
    if remaining <= 0:
        return None, "deadline"
    try:
        child = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    except OSError:
        return None, "cli_unavailable"
    data = bytearray()
    selector = selectors.DefaultSelector()
    selector.register(child.stdout, selectors.EVENT_READ)
    until = time.monotonic() + remaining
    reason = "ok"
    try:
        while selector.get_map():
            left = until - time.monotonic()
            if left <= 0:
                reason = "timeout"
                break
            for key, _ in selector.select(left):
                part = os.read(key.fileobj.fileno(), min(65536, MAX_BYTES + 1 - len(data)))
                if not part:
                    selector.unregister(key.fileobj)
                else:
                    data.extend(part)
                    if len(data) > MAX_BYTES:
                        reason = "oversized"
                        break
            if reason != "ok":
                break
        if reason == "ok":
            try:
                code = child.wait(timeout=max(0.001, until - time.monotonic()))
                if code:
                    reason = "cli_nonzero"
            except subprocess.TimeoutExpired:
                reason = "timeout"
    finally:
        # Do not poll/reap here: an exited, unreaped leader pins the group ID
        # while a descendant can still hold stdout. Reap only AFTER group kill.
        if child.returncode is None:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        child.wait(timeout=1)
        selector.close()
        child.stdout.close()
    return bytes(data), reason


def source_provenance(deadline):
    root = HERE.parent.parent
    sha = os.environ.get("INGEST_SHA", "")
    if not re.fullmatch(r"[a-f0-9]{40}", sha):
        raise ValueError("source")
    head, reason = command(["git", "-C", str(root), "rev-parse", "HEAD"], deadline)
    if reason != "ok" or head.decode().strip() != sha:
        raise ValueError("source")
    hashes = {}
    for name in SOURCE_FILES:
        path = "scripts/test-harness/" + name
        raw, reason = command(["git", "-C", str(root), "show", sha + ":" + path], deadline)
        if reason != "ok" or raw != (HERE / name).read_bytes():
            raise ValueError("source")
        hashes[path] = hashlib.sha256(raw).hexdigest()
    return {"sha": sha, "files_sha256": hashes}


def project_run(row):
    if not isinstance(row, dict) or not identity(row.get("id")):
        raise ValueError("row")
    if row.get("run_id") and row.get("trace_id") and row["run_id"] != row["trace_id"]:
        raise ValueError("conflicting journal identity")
    result = {"assignment_id": row["id"], "journal_run_id": identity(row.get("run_id") or row.get("trace_id")),
              "agent_id": identity(row.get("agent_id")),
              "agent_slug": row.get("agent_slug") if row.get("agent_slug") in {"alex", "morgan"} else "other",
              "status": row.get("status") if row.get("status") in STATES else "UNKNOWN",
              "outcome": row.get("outcome") if row.get("outcome") in OUTCOMES else "UNKNOWN",
              "error_class": "not_available_from_metadata"}
    return result


def collect(issues, cli, deadline):
    result = []
    for ident in issues:
        item = {"issue_identifier": ident, "complete": False, "lookups": {}, "runs": []}
        result.append(item)
        for operation in ("get", "runs"):
            raw, reason = command(cli + ["issue", operation, ident, "--format", "json"], deadline)
            item["lookups"][operation] = reason
            if reason != "ok":
                continue
            try:
                body = json.loads(raw)
                if operation == "get":
                    if not isinstance(body, dict) or body.get("identifier") != ident or not identity(body.get("id")):
                        raise ValueError("identity")
                    item["issue_id"] = body["id"]
                else:
                    if not isinstance(body, list) or len(body) > 16:
                        raise ValueError("rows")
                    item["runs"] = [project_run(row) for row in body]
            except (ValueError, TypeError, RecursionError):
                item["lookups"][operation] = "invalid_or_excess_metadata"
        item["complete"] = all(item["lookups"].get(op) == "ok" for op in ("get", "runs"))
    return result


def persist(root, value):
    # Reuse the reviewed no-follow owner/mode directory traversal, no raw input.
    spec = importlib.util.spec_from_file_location("receipt_paths", HERE / "crew-links-receipt.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    parent = module.private_directory(root)
    dest = None
    temporary = ".interrupted-" + uuid.uuid4().hex + ".tmp"
    published = False
    inode = None
    try:
        try:
            os.mkdir("test-crew-links", 0o700, dir_fd=parent)
        except FileExistsError:
            pass
        dest = os.open("test-crew-links", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent)
        info = os.fstat(dest)
        if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
            raise ValueError("private")
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=dest)
        with os.fdopen(fd, "wb") as output:
            inode = os.fstat(output.fileno())
            os.fchmod(output.fileno(), 0o600)
            output.write((json.dumps(value, sort_keys=True) + "\n").encode())
            output.flush()
            os.fsync(output.fileno())
        os.link(temporary, "interruption-receipt.v1.json", src_dir_fd=dest, dst_dir_fd=dest, follow_symlinks=False)
        published = True
        os.fsync(dest)
    except BaseException:
        if published:
            current = os.stat("interruption-receipt.v1.json", dir_fd=dest, follow_symlinks=False)
            if inode is not None and (current.st_dev, current.st_ino) == (inode.st_dev, inode.st_ino):
                os.unlink("interruption-receipt.v1.json", dir_fd=dest)
        raise
    finally:
        if dest is not None:
            try:
                os.unlink(temporary, dir_fd=dest)
            except FileNotFoundError:
                pass
            os.close(dest)
        os.close(parent)


def main():
    started = time.monotonic()
    deadline = started + 12
    original = int(sys.argv[1])
    if original not in {130, 143}:
        raise ValueError("status")
    raw = sys.stdin.buffer.read(4097)
    if len(raw) > 4096:
        raise ValueError("owned input")
    issues = raw.decode().splitlines()
    if len(issues) > 3 or len(set(issues)) != len(issues) or not all(ISSUE.fullmatch(x) for x in issues):
        raise ValueError("owned issues")
    source = source_provenance(deadline)  # Validates this helper before path-helper import.
    cli = [os.environ["CREWSHIP"], "--server", os.environ["SERVER"]]
    for env, flag in (("CREWSHIP_PROFILE", "--profile"), ("CREWSHIP_WORKSPACE", "--workspace")):
        if os.environ.get(env):
            cli += [flag, os.environ[env]]
    rows = collect(issues, cli, deadline)
    value = {"schema_version": 1, "suite": "test-crew-links", "interrupted": True,
             "original_exit_status": original, "capture_stage": "before_topology_restore",
             "complete": bool(issues) and all(x["complete"] for x in rows),
             "source": source, "owned_issue_count": len(issues), "issues": rows,
             "elapsed_seconds": round(time.monotonic() - started, 3),
             "raw_text_retained": False, "mutation_requests": 0,
             "global_termination_bound": "existing_infra1900s_TERM_plus30s_KILL_not_changed"}
    persist(os.environ["HARNESS_DETAIL_DIR"], value)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception:
        print("crew-links interrupted metadata: capture incomplete", file=sys.stderr)
        raise SystemExit(2) from None
