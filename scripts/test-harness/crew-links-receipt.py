#!/usr/bin/env python3
"""Project existing crew-link observations and retain one private receipt.

Inputs use NUL-delimited stdin, never raw response argv or temporary files.
Projection modes print only allowlisted metadata; write mode prints nothing.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import uuid


OUTCOMES = {"SUCCEEDED", "NO_CHANGE", "WORK_CREATED"}
IDENTITY = re.compile(r"[A-Za-z0-9_.:-]{1,256}\Z")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")


class NotMatched(Exception):
    """A well-formed observation has not proved the required operation."""


def identity(value):
    if not isinstance(value, str) or not IDENTITY.fullmatch(value):
        raise ValueError("invalid identity")
    return value


def digest(value):
    if not isinstance(value, str):
        raise ValueError("expected text")
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def fields(count):
    values = sys.stdin.buffer.read().split(b"\0")
    if len(values) != count + 1 or values[-1]:
        raise ValueError("invalid input framing")
    return [value.decode("utf-8") for value in values[:-1]]


def positive(issue, assignment, code, tag, candidates, runs, full):
    candidate = next((row for row in candidates if row.get("assignment_id") == assignment), None)
    run = next((row for row in runs if row.get("id") == assignment
                and row.get("agent_slug") == "morgan" and row.get("status") == "COMPLETED"), None)
    summary = full.get("result_summary")
    if (candidate is None or run is None or not code or not tag
            or candidate.get("outcome") not in OUTCOMES
            or full.get("status") != "COMPLETED" or full.get("outcome") not in OUTCOMES
            or not isinstance(summary, str) or code not in summary):
        raise NotMatched()
    actor = identity(run.get("agent_id"))
    return {
        "issue_identifier": identity(issue), "assignment_id": identity(assignment),
        "journal_run_id": identity(run.get("run_id") or run.get("trace_id")),
        "agent_id": actor, "agent_slug": "morgan", "status": "COMPLETED",
        "outcome": full["outcome"], "completed_outcome": candidate["outcome"],
        "created_activity_id": identity(candidate.get("created_activity_id")),
        "completed_activity_id": identity(candidate.get("completed_activity_id")),
        "full_result_utf8_sha256": digest(summary), "full_result_utf8_bytes": len(summary.encode("utf-8")),
        "expected_record_code_sha256": digest(code), "expected_code_present": True,
        "correlation_tag_sha256": digest(tag), "tag_matched_created_task": True,
        "lookup_cli_exit": 0,
    }


def refusal(issue, source, target, source_run, entries):
    run = source_run.get("run_id") or source_run.get("trace_id")
    actor = source_run.get("agent_id")
    if source_run.get("agent_slug") != "alex":
        raise NotMatched()
    for entry in entries:
        payload = entry.get("payload") or {}
        if (entry.get("entry_type") == "assignment.refused" and entry.get("trace_id") == run
                and entry.get("actor_id") == actor and entry.get("agent_id") == actor
                and entry.get("crew_id") == source and payload.get("reason") == "crews_not_connected"
                and payload.get("http_status") == 403 and payload.get("source_crew_id") == source
                and payload.get("target_crew_id") == target and payload.get("target_slug") == "morgan"):
            task_hash = payload.get("task_sha256")
            if entry.get("actor_type") != "agent" or not isinstance(task_hash, str) or not SHA256.fullmatch(task_hash):
                raise ValueError("incomplete server receipt")
            return {
                "issue_identifier": identity(issue), "assignment_id": identity(source_run.get("id")),
                "source_run_id": identity(run), "source_actor_id": identity(actor),
                "source_crew_id": identity(source), "target_crew_id": identity(target), "target_slug": "morgan",
                "refusal": {"entry_id": identity(entry.get("id")), "entry_type": "assignment.refused",
                            "actor_type": "agent", "actor_id": identity(actor), "agent_id": identity(actor),
                            "trace_id": identity(run), "crew_id": identity(source),
                            "reason": "crews_not_connected", "http_status": 403,
                            "task_sha256": task_hash, "evidence_source": "server_journal"},
            }
    raise NotMatched()


def source_provenance():
    sha = os.environ.get("INGEST_SHA", "")
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("missing source binding")
    here = Path(__file__).resolve().parent
    root = here.parent.parent
    head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], stderr=subprocess.DEVNULL, text=True).strip()
    if head != sha:
        raise ValueError("source differs from target binding")
    hashes = {}
    for name in ["run-all.sh", "lib.sh", "test-crew-links.sh", "crew-links-evidence.sh", "crew-links-receipt.py"]:
        path = "scripts/test-harness/" + name
        committed = subprocess.check_output(["git", "-C", str(root), "show", sha + ":" + path], stderr=subprocess.DEVNULL)
        current = (here / name).read_bytes()
        if committed != current:
            raise ValueError("harness source is not the bound commit")
        hashes[path] = hashlib.sha256(current).hexdigest()
    return {"sha": sha, "files_sha256": hashes}


def receipt(positive_proof, negative_proof, record_id, record_identifier, source, target, started, passed, restored, work_landed):
    # Re-project: callers cannot smuggle arbitrary keys into the durable object.
    positive_keys = {"issue_identifier", "assignment_id", "journal_run_id", "agent_id", "agent_slug", "status", "outcome", "completed_outcome",
                     "created_activity_id", "completed_activity_id", "full_result_utf8_sha256", "full_result_utf8_bytes",
                     "expected_record_code_sha256", "expected_code_present", "correlation_tag_sha256", "tag_matched_created_task", "lookup_cli_exit"}
    negative_keys = {"issue_identifier", "assignment_id", "source_run_id", "source_actor_id", "source_crew_id", "target_crew_id", "target_slug", "refusal"}
    refusal_keys = {"entry_id", "entry_type", "actor_type", "actor_id", "agent_id", "trace_id", "crew_id", "reason", "http_status", "task_sha256", "evidence_source"}
    if (set(positive_proof) != positive_keys or set(negative_proof) != negative_keys
            or set(negative_proof.get("refusal", {})) != refusal_keys):
        raise ValueError("unexpected projection fields")
    for key in ["issue_identifier", "assignment_id", "journal_run_id", "agent_id", "created_activity_id", "completed_activity_id"]:
        identity(positive_proof[key])
    for key in ["full_result_utf8_sha256", "expected_record_code_sha256", "correlation_tag_sha256"]:
        if not isinstance(positive_proof[key], str) or not SHA256.fullmatch(positive_proof[key]):
            raise ValueError("invalid content hash")
    if (positive_proof["agent_slug"] != "morgan" or positive_proof["status"] != "COMPLETED"
            or positive_proof["outcome"] not in OUTCOMES or positive_proof["completed_outcome"] not in OUTCOMES
            or positive_proof["expected_code_present"] is not True
            or positive_proof["tag_matched_created_task"] is not True
            or type(positive_proof["lookup_cli_exit"]) is not int or positive_proof["lookup_cli_exit"] != 0
            or type(positive_proof["full_result_utf8_bytes"]) is not int or positive_proof["full_result_utf8_bytes"] < 1):
        raise ValueError("incomplete positive observation")
    for key in ["issue_identifier", "assignment_id", "source_run_id", "source_actor_id", "source_crew_id", "target_crew_id"]:
        identity(negative_proof[key])
    refusal_proof = negative_proof["refusal"]
    for key in ["entry_id", "actor_id", "agent_id", "trace_id", "crew_id"]:
        identity(refusal_proof[key])
    if (negative_proof["target_slug"] != "morgan" or refusal_proof["entry_type"] != "assignment.refused"
            or refusal_proof["actor_type"] != "agent" or refusal_proof["evidence_source"] != "server_journal"
            or refusal_proof["reason"] != "crews_not_connected" or type(refusal_proof["http_status"]) is not int
            or refusal_proof["http_status"] != 403 or refusal_proof["trace_id"] != negative_proof["source_run_id"]
            or refusal_proof["actor_id"] != negative_proof["source_actor_id"]
            or refusal_proof["agent_id"] != negative_proof["source_actor_id"]
            or refusal_proof["crew_id"] != negative_proof["source_crew_id"]
            or not isinstance(refusal_proof["task_sha256"], str) or not SHA256.fullmatch(refusal_proof["task_sha256"])):
        raise ValueError("incomplete server refusal")
    if negative_proof["source_crew_id"] != source or negative_proof["target_crew_id"] != target:
        raise ValueError("crew binding changed")
    if restored != "0" or work_landed != "0" or not passed.isdigit() or int(passed) < 1:
        raise ValueError("incomplete suite")
    run_id = identity(os.environ.get("SCAN_RUN_ID"))
    stage_run = os.environ.get("STAGE_RUN_ID")
    if stage_run and stage_run != run_id:
        raise ValueError("scan and stage differ")
    workspace = identity(os.environ.get("CREWSHIP_WORKSPACE"))
    if not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z?", started):
        raise ValueError("invalid start time")
    from datetime import datetime, timezone
    return {"schema_version": 1, "suite": "test-crew-links", "complete": True,
            "started_at": started, "completed_at": datetime.now(timezone.utc).isoformat(),
            "run": {"scan_run_id": run_id, "stage_run_id": stage_run, "workspace_sha256": digest(workspace)},
            "source": source_provenance(),
            "assertions": {"passed": int(passed), "failed": 0, "skipped": 0, "xfail": 0},
            "record": {"issue_id": identity(record_id), "issue_identifier": identity(record_identifier)},
            "positive": positive_proof, "negative": negative_proof,
            "topology": {"link_restored": True, "correlated_unlinked_work_delta": 0}}


def private_directory(path):
    root = Path(path)
    if not root.is_absolute() or ".." in root.parts:
        raise ValueError("absolute private root required")
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
    fd = os.open("/", flags)
    try:
        for part in root.parts[1:]:
            child = os.open(part, flags, dir_fd=fd)
            os.close(fd)
            fd = child
        info = os.fstat(fd)
        if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
            raise ValueError("private owner/mode required")
        return fd
    except BaseException:
        os.close(fd)
        raise


def persist(root, value):
    parent = private_directory(root)
    dest = None
    temporary = ".success-" + uuid.uuid4().hex + ".tmp"
    published = False
    temporary_created = False
    inode = None
    try:
        try:
            os.mkdir("test-crew-links", 0o700, dir_fd=parent)
            os.fsync(parent)
        except FileExistsError:
            pass
        dest = os.open("test-crew-links", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent)
        info = os.fstat(dest)
        if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
            raise ValueError("private suite owner/mode required")
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=dest)
        temporary_created = True
        os.fchmod(fd, 0o600)  # This newly created private inode only.
        with os.fdopen(fd, "wb") as output:
            inode = os.fstat(output.fileno())
            output.write((json.dumps(value, ensure_ascii=True, sort_keys=True) + "\n").encode())
            output.flush()
            os.fsync(output.fileno())
        os.link(temporary, "success-receipt.v1.json", src_dir_fd=dest, dst_dir_fd=dest, follow_symlinks=False)
        published = True
        os.unlink(temporary, dir_fd=dest)
        os.fsync(dest)
    except BaseException:
        if dest is not None and published:
            current = os.stat("success-receipt.v1.json", dir_fd=dest, follow_symlinks=False)
            if inode is not None and (current.st_dev, current.st_ino) == (inode.st_dev, inode.st_ino):
                os.unlink("success-receipt.v1.json", dir_fd=dest)
        raise
    finally:
        if dest is not None:
            if temporary_created:
                try:
                    os.unlink(temporary, dir_fd=dest)
                except FileNotFoundError:
                    pass
            os.close(dest)
        os.close(parent)


def main():
    try:
        mode = sys.argv[1]
        if mode == "positive":
            issue, assignment, code, tag, candidates, runs, full = fields(7)
            value = positive(issue, assignment, code, tag, json.loads(candidates), json.loads(runs), json.loads(full))
        elif mode == "refusal":
            issue, source, target, run, entries = fields(5)
            value = refusal(issue, source, target, json.loads(run), json.loads(entries))
        elif mode == "write":
            positive_proof, negative_proof, *context = fields(10)
            value = receipt(json.loads(positive_proof), json.loads(negative_proof), *context)
            persist(sys.argv[2], value)
            return 0
        else:
            raise ValueError("unknown mode")
        print(json.dumps(value, separators=(",", ":"), sort_keys=True))
        return 0
    except NotMatched:
        return 1
    except Exception:
        # Never include input values, exception snippets or raw model output.
        print("crew-links receipt: evidence projection or private persistence failed", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
