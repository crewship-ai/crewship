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
import urllib.error
import urllib.parse
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


# The token goes on every request in this file, so the two ways urllib gives it
# away both have to be closed.
#
# urllib copies request headers onto a redirect verbatim, excluding only
# content-length and content-type — Authorization rides along, to any host. And
# `base` is operator-supplied, so plain http would put the bearer on the wire in
# cleartext. Neither is exotic for a checker aimed at whatever URL someone
# passes it.
#
# Refusing every redirect rather than allowing same-origin ones is not just the
# simpler rule: on a GET /api/v1/... a redirect is itself worth reporting, and
# the exception below names the route it happened on.
LOOPBACK_HOSTS = {"localhost", "127.0.0.1", "::1"}


class _RefuseRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.HTTPError(
            req.full_url, code,
            f"refused to follow a redirect to {newurl} — the bearer token "
            "would be forwarded with it",
            headers, fp)


_opener = urllib.request.build_opener(_RefuseRedirects)


def checked_base(base):
    """Reject a base URL that would put the bearer token on the wire in clear.

    http://localhost:8082 stays allowed and is the documented development
    path — the token never leaves the host.
    """
    parsed = urllib.parse.urlparse(base)
    if parsed.scheme not in ("http", "https"):
        raise SystemExit(f"base url must be http or https, got {parsed.scheme!r}")
    if parsed.scheme == "http" and parsed.hostname not in LOOPBACK_HOSTS:
        raise SystemExit(
            f"refusing to send a bearer token in cleartext to {parsed.hostname}; "
            "use https, or a loopback address for local development")
    return base.rstrip("/")


def fetch_spec(base):
    """The document the target instance actually serves, not the file in the repo.

    Held to the same 200-and-JSON rule as the routes. Holding the 17 routes to a
    rule the document they are ALL judged against does not have to meet is the
    same defect one level up, where it is worth more: a wrong openapi.json
    mis-grades every route at once.
    """
    with _opener.open(f"{base}/openapi.json", timeout=20) as r:
        return denullable(read_json_200(r, "openapi.json"))


class Unjudgeable(Exception):
    """A response there is no documented schema to judge, so it is not a pass."""


class NotA200(Unjudgeable):
    """A 2xx that is not 200, which this checker has no schema to judge."""


class NotJSON(Unjudgeable):
    """A body whose media type is not the one the 200 schema was written for."""


def read_json_200(response, what):
    """Read a body only when it is the response the documented 200 describes.

    `response_schema` pulls the schema out of the `application/json` entry of
    the documented **200** and nothing else, so both halves of that — the status
    AND the media type — have to hold before the body may be compared against
    it. Otherwise the checker grades a response against a contract the server
    never claimed for it and calls that a pass, which is the defect this whole
    branch exists to remove.

    urllib raises on 4xx/5xx and redirects are refused, so what this actually
    catches is the rest of the 2xx band and a wrong Content-Type: a 201 or 206
    read as if it were the 200, a 204 that would otherwise surface as a JSON
    decode error reading like a broken server, and a text/plain body that
    happens to parse.

    Media-type PARAMETERS are fine — `application/json; charset=utf-8` is
    application/json. Rejecting it would fail every server that spells the
    charset out, and a gate that cries wolf gets switched off, which
    docs/prd/response-shape-contract.md gives as the reason #1815 is already in
    that position.
    """
    if response.status != 200:
        raise NotA200(f"{what}: HTTP {response.status}, and only 200 has a documented schema")
    media = response.headers.get("Content-Type", "").split(";", 1)[0].strip().lower()
    if media != "application/json":
        raise NotJSON(f"{what}: Content-Type {media or '(absent)'}, not application/json")
    return json.load(response)


def fetch(base, path, token, workspace):
    sep = "&" if "?" in path else "?"
    # urlencode, not interpolation. `workspace` is argv: a value carrying `&`
    # splits into a second parameter and one carrying `#` truncates the rest,
    # so the URL could name a different workspace than the X-Workspace-ID
    # header — and the checker would validate one workspace while reporting on
    # another. Measuring the wrong thing and calling it a pass, again.
    query = urllib.parse.urlencode({"workspace_id": workspace})
    req = urllib.request.Request(
        f"{base}{path}{sep}{query}",
        headers={"Authorization": f"Bearer {token}", "X-Workspace-ID": workspace},
    )
    with _opener.open(req, timeout=20) as r:
        return read_json_200(r, path)


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
    base, token, workspace = checked_base(argv[1]), argv[2], argv[3]
    try:
        spec = fetch_spec(base)
    except Exception as exc:
        print(f"  could not read {base}/openapi.json: {type(exc).__name__}: {exc}",
              file=sys.stderr)
        return 1

    # NOTHING HERE IS SKIPPABLE, and that is the whole design.
    #
    # ROUTES is a hand-curated list of read-only GETs that a reachable server
    # with a workspace-owner token must answer; the ones that cannot be checked
    # are commented out of it with reasons. So an unreachable route or an
    # undocumented 200 is a defect in the server, the document or the
    # credentials — never an excusable condition — and either way the run
    # verified less than the summary line claims.
    #
    # It used to count both as SKIP and return `1 if failed else 0`. A wrong
    # token, a server on the wrong port or somebody else's workspace id printed
    # "0 pass, 0 fail, 17 skipped" and exited 0: a checker that exercised
    # nothing, reporting success. That is the exact shape of green this file was
    # written to make impossible, and it had it.
    failed = passed = 0
    for path in ROUTES:
        try:
            body = fetch(base, path, token, workspace)
        except Exception as exc:
            failed += 1
            print(f"  FAIL  {path:38} no 200 JSON body: {type(exc).__name__}: {exc}"[:170])
            continue
        errors = check(spec, path, body)
        if errors is None:
            failed += 1
            print(f"  FAIL  {path:38} the served spec documents no 200 "
                  "application/json schema for it")
        elif errors:
            failed += 1
            print(f"  FAIL  {path:38} {errors[0][:100]}")
            for extra in errors[1:4]:
                print(f"        {' ' * 38} {extra[:100]}")
        else:
            passed += 1
            print(f"  PASS  {path}")

    # Ahead of the summary line, which would otherwise contradict it with a
    # cheerful "0 of 0 routes verified, 0 fail".
    #
    # `passed == len(ROUTES)` is satisfied by 0 == 0, so an emptied list — a bad
    # merge, a filter that matched nothing, the last entry commented out during
    # debugging — reached the same false green from the other end. A run has to
    # have had something to check.
    if not ROUTES:
        print("\n  no routes declared — this run verified nothing")
        return 1

    print(f"\n  {passed} of {len(ROUTES)} routes verified, {failed} fail")

    # The exit status asks "did every declared route pass?", not "did nothing
    # fail?". Those two differ in exactly one case — the run that checked
    # nothing — and phrasing it this way keeps a future edit from
    # reintroducing a silent skip path without also failing the run.
    return 0 if passed == len(ROUTES) else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
