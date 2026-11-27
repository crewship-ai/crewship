"""Put a pre-workbook Sheet on a Page of the disposable browser gate server."""

import hashlib
import json
from pathlib import Path
import sqlite3
import sys
from datetime import datetime, timezone


def main():
    database, workspace, page = sys.argv[1:]
    path = Path(database).resolve(strict=True)
    if path.name != "crewship.db" or path.parent.name != "data" or not path.parent.parent.name.startswith("craft-gate."):
        raise ValueError("legacy seed requires the disposable Craft gate database")

    source = sys.stdin.buffer.read()
    digest = hashlib.sha256(source).hexdigest()
    manifest = json.dumps({
        "entry_point": "document.json",
        "files": {"document.json": {"hash": digest, "media_type": "application/json"}},
    })
    legacy_revision = "e68d192282499bed81a9800d742cb9b239d367e5"

    with sqlite3.connect(path.as_uri() + "?mode=rw", uri=True, timeout=10) as db:
        db.execute("PRAGMA foreign_keys=ON")
        db.execute("BEGIN IMMEDIATE")
        owner = db.execute(
            "SELECT owner_user_id FROM pages WHERE workspace_id=? AND id=? AND created_by_test=1",
            (workspace, page),
        ).fetchone()
        if not owner or not owner[0]:
            raise ValueError("legacy seed requires a newly created test Page with an owner")
        db.execute(
            """INSERT INTO craft_native_documents
               (workspace_id,page_id,application_id,engine_revision,engine_sha256,editor_bundle)
               VALUES (?,?,'gridcraft',?,?,?)""",
            (workspace, page, legacy_revision, "a" * 64, "b" * 64),
        )
        db.execute(
            "INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES (?,?,?,?)",
            (workspace, page, digest, source),
        )
        db.execute(
            "INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,actor,created_at) VALUES (?,?,1,?,?,?)",
            (workspace, page, manifest, "user:" + owner[0], datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")),
        )
        db.execute(
            "INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version) VALUES (?,?,'gridcraft',1)",
            (workspace, page),
        )


if __name__ == "__main__":
    main()
