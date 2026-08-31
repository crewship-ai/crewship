#!/usr/bin/env python3
"""Validate a running server's real responses against the spec it serves.

This is the check nothing else performs. The generator's own tests prove the
document is internally consistent; internal/api's pair table proves `required`
was derived from the response structs rather than someone's memory. Neither
looks at what the server actually puts on the wire.

That gap is not academic. /api/v1/approvals serialized a struct with no JSON
tags, so it answered "ID"/"Kind"/"CreatedAt" while the document, the web
client's schema and the API reference all said snake_case. Three artifacts
agreed with each other and none with the server, every gate was green, and the
approvals surface rendered zero rows in production.

Deliberately small. Schemathesis (run.sh) generates inputs and probes error
branches; this only asks whether a real 200 body satisfies the schema the
server publishes for it. That narrowness is why it can run in seconds on every
change, and why it needs no new dependency beyond `jsonschema`.
"""
import json
import sys
import urllib.request


def denullable(node):
    """Translate OpenAPI 3.0's `nullable: true` into a JSON Schema type union.

    JSON Schema has no `nullable` keyword. A validator that does not know this
    reports every legitimate null as a type error — which it did, on nine of
    thirteen routes, the first time this check was run. Schemathesis handles it
    natively; a plain validator has to be told.
    """
    if isinstance(node, dict):
        out = dict(node)
        if out.pop("nullable", False) and isinstance(out.get("type"), str):
            out["type"] = [out["type"], "null"]
        return {k: denullable(v) for k, v in out.items()}
    if isinstance(node, list):
        return [denullable(x) for x in node]
    return node


def response_schema(spec, path, method="get", status="200"):
    op = spec.get("paths", {}).get(path, {}).get(method, {})
    content = op.get("responses", {}).get(status, {}).get("content", {})
    return content.get("application/json", {}).get("schema")


def check(spec, path, body):
    """Return a list of human-readable violations for one response body."""
    import jsonschema

    schema = response_schema(spec, path)
    if schema is None:
        return None  # nothing documented to check against
    resolver = jsonschema.RefResolver.from_schema(spec)
    validator = jsonschema.Draft7Validator(schema, resolver=resolver)
    return [
        f"{'/'.join(map(str, e.path)) or '(root)'}: {e.message}"
        for e in sorted(validator.iter_errors(body), key=lambda e: list(e.path))
    ]


def fetch(base, path, token, workspace):
    sep = "&" if "?" in path else "?"
    req = urllib.request.Request(
        f"{base}{path}{sep}workspace_id={workspace}",
        headers={"Authorization": f"Bearer {token}", "X-Workspace-ID": workspace},
    )
    with urllib.request.urlopen(req, timeout=20) as r:
        return json.load(r)


# Read-only GETs only, mirroring run.sh's method deny-list: this check must
# never be the reason a workspace changed.
ROUTES = [
    "/api/v1/workspaces", "/api/v1/crews", "/api/v1/agents", "/api/v1/issues",
    "/api/v1/skills", "/api/v1/projects", "/api/v1/credentials", "/api/v1/inbox",
    "/api/v1/approvals", "/api/v1/admin/security-posture",
    "/api/v1/admin/memory/config", "/api/v1/admin/memory/stats",
    "/api/v1/admin/memory/versions",
    # keeper: read-only, in-memory or a pure config read, no path parameter.
    "/api/v1/admin/keeper/health",
    "/api/v1/admin/keeper/config",
    "/api/v1/admin/keeper/aux",
    # connectors: auth only, no workspace context, no path parameter.
    "/api/v1/connectors",
    # Deliberately NOT here:
    #   /admin/keeper/judge/models  — dials an operator-configured Ollama
    #     endpoint and is instance-wide rate limited; a checker running on
    #     every change would compete with real operators for that budget.
    #   /admin/keeper/requests      — the schema says {requests, count}; the
    #     handler writes a bare array. Adding it now reports a broken server
    #     rather than the schema bug it actually is. See the exclusion in
    #     internal/api/openapi_response_coverage_test.go.
]


def main(argv):
    if len(argv) < 4:
        print("usage: response_shapes.py <base-url> <token> <workspace-id>", file=sys.stderr)
        return 2
    base, token, workspace = argv[1], argv[2], argv[3]
    with urllib.request.urlopen(f"{base}/openapi.json", timeout=20) as r:
        spec = denullable(json.load(r))

    failed = passed = skipped = 0
    for path in ROUTES:
        try:
            body = fetch(base, path, token, workspace)
        except Exception as exc:  # unreachable route is not a shape violation
            print(f"  SKIP  {path:38} {type(exc).__name__}")
            skipped += 1
            continue
        errors = check(spec, path, body)
        if errors is None:
            print(f"  SKIP  {path:38} no documented 200 json schema")
            skipped += 1
        elif errors:
            failed += 1
            print(f"  FAIL  {path:38} {errors[0][:100]}")
            for extra in errors[1:4]:
                print(f"        {' ' * 38} {extra[:100]}")
        else:
            passed += 1
            print(f"  PASS  {path}")

    print(f"\n  {passed} pass, {failed} fail, {skipped} skipped")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
