#!/usr/bin/env python3
"""Positive fixture probes for the #1815 bucket-3 operation families.

Schemathesis (run.sh) generates path parameters from the route catalog, so it
pairs ids the fixture never bound: crew cmuu… from one listing with an
integration from another, revision 0 for a route whose real revisions start at
1. Those findings are about the generator's id assembly, not the product —
but the flip side is that the gate alone cannot prove the PRODUCT side of
those operations works against real fixture data either.

This script proves the fixture side, with ids resolved from the seeded
workspace at run time:

  - a Page whose project storage holds a draft (revision >= 1; that the seed
    also PUBLISHED it is proven by the seed step's own exit status, not by
    this probe, which accepts an empty publications list): every read branch of
    /pages/{slug}/project* answers 200 with real data, history revisions are
    read from the fixture and one real revision is positively fetched (plus
    the two designed negatives: revision 0 is a 400, an absent revision is a
    404);
  - a crew-scoped MCP integration that actually exists: the bound
    (crew_id, integration_id) pair answers 200 on the tools route, and the
    same route answers 404 for an unknown crew and for an unknown
    integration on the real crew (the pairing check is live, not absent).

What a pass proves is deliberately narrow: HTTP status and, where stated, the
shape of the key field (revision >= 1, a non-empty revision list, a JSON list
from the tools route). It does NOT validate response bodies against the
published schema — the gate and response_shapes.py do that — and an empty
tools list is a 200 that says the route reached its handler, not that any
tool item is well-formed. The draft, publication and review probes likewise
prove "reached real project storage and answered", not field-level content.

It is deliberately a PROBE, not a generator: it does not judge schemas (the
gate does), it judges that the fixture makes the operations exercisable with
correlated, real ids. A run with no crew-integration binding or no project
draft is a FAILURE, not a skip — the CI fixture's whole point is that those
exist.

Token handling matches response_shapes.py: the bearer goes on every request,
so plain http is refused except for loopback, and redirects are refused
rather than followed.
"""
import json
import sys
import urllib.error
import urllib.parse
import urllib.request

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
    parsed = urllib.parse.urlparse(base)
    if parsed.scheme not in ("http", "https"):
        raise SystemExit(f"base url must be http or https, got {parsed.scheme!r}")
    if parsed.scheme == "http" and parsed.hostname not in LOOPBACK_HOSTS:
        raise SystemExit(
            f"refusing to send a bearer token in cleartext to {parsed.hostname}; "
            "use https, or a loopback address for local development")
    return base.rstrip("/")


class Api:
    def __init__(self, base, token, workspace):
        self.base = checked_base(base)
        self.token = token
        self.workspace = workspace

    def get(self, path):
        """Return (status, parsed-body-or-None). Never raises for 4xx/5xx."""
        return self.send("GET", path)

    def send(self, method, path, payload=None):
        """Same contract as get(), for the fixture's few provisioning writes."""
        headers = {
            "Authorization": f"Bearer {self.token}",
            "X-Workspace-ID": self.workspace,
        }
        data = None
        if payload is not None:
            data = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with _opener.open(req, timeout=20) as r:
                status, body = r.status, r.read()
        except urllib.error.HTTPError as e:
            status, body = e.code, e.read()
        # The real status, never a constant: a 202 (accepted, not yet done)
        # or a 204 must not read as the 200 the probes assert on.
        return status, self._maybe_json(body, path, status)

    @staticmethod
    def _maybe_json(raw, what, status):
        if not raw:
            return None
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            raise SystemExit(f"{what} answered HTTP {status} with a non-JSON body")


def probe_pages(api, failures):
    print("pages fixture probes:")
    pages_status, pages = api.get("/api/v1/pages")
    if pages_status != 200 or not isinstance(pages, list) or not pages:
        failures.append(f"GET /api/v1/pages: expected a non-empty seeded page list, got HTTP {pages_status}")
        return
    slug = None
    for candidate in pages:
        s = candidate.get("slug") if isinstance(candidate, dict) else None
        if not s:
            continue
        status, draft = api.get(f"/api/v1/pages/{s}/project")
        if status == 200 and isinstance(draft, dict) and draft.get("revision", 0) >= 1:
            slug = s
            break
        print(f"  = {s}: no project draft (HTTP {status}) — trying the next page")
    if slug is None:
        failures.append("no seeded Page carries a project draft; the pages fixture is not installed")
        return
    print(f"  ok   /pages/{slug}/project -> 200 revision {draft.get('revision')}")

    for op in ("project/fsck", "project/preview", "project/publications", "project/review", "project/source"):
        status, body = api.get(f"/api/v1/pages/{slug}/{op}")
        if status == 200:
            print(f"  ok   /pages/{slug}/{op} -> 200")
        else:
            failures.append(f"GET /pages/{slug}/{op}: expected 200, got HTTP {status}: {body}")

    status, history = api.get(f"/api/v1/pages/{slug}/project/history")
    revisions = None
    if status == 200 and isinstance(history, dict):
        revisions = history.get("revisions")
    if status != 200 or not revisions:
        failures.append(f"GET /pages/{slug}/project/history: expected 200 with revisions, got HTTP {status}: {history}")
        return
    revision = revisions[0].get("revision")
    if not isinstance(revision, int) or revision < 1:
        failures.append(f"first history revision is not a positive integer: {revisions[0]!r}")
        return
    for suffix in ("", "/source"):
        status, body = api.get(f"/api/v1/pages/{slug}/project/history/{revision}{suffix}")
        if status == 200:
            print(f"  ok   /pages/{slug}/project/history/{revision}{suffix} -> 200 (real revision)")
        else:
            failures.append(f"GET /pages/{slug}/project/history/{revision}{suffix}: expected 200, got HTTP {status}: {body}")

    # The designed negatives. Revision 0 is rejected by validation ("revision
    # must be positive" — the same missing-minimum finding the gate reports),
    # and an absent revision is the uniform not-found answer. A 200 for either
    # would mean the routes stopped validating at all.
    status, body = api.get(f"/api/v1/pages/{slug}/project/history/0")
    if status == 400:
        print("  ok   /pages/…/project/history/0 -> 400 (validation negative)")
    else:
        failures.append(f"GET /pages/{slug}/project/history/0: expected 400, got HTTP {status}: {body}")
    status, body = api.get(f"/api/v1/pages/{slug}/project/history/99999999")
    if status == 404:
        print("  ok   /pages/…/project/history/99999999 -> 404 (absent revision negative)")
    else:
        failures.append(f"GET /pages/{slug}/project/history/99999999: expected 404, got HTTP {status}: {body}")


def probe_crew_integration(api, failures):
    """Probe the bound pair; return (crew_id, integration_id) or None."""
    print("crew/integration fixture probes:")
    status, crews = api.get("/api/v1/crews")
    rows = crews if isinstance(crews, list) else (crews or {}).get("crews") if isinstance(crews, dict) else None
    if status != 200 or not rows:
        failures.append(f"GET /api/v1/crews: expected a non-empty seeded crew list, got HTTP {status}")
        return None
    bound = None
    for crew in rows:
        crew_id = crew.get("id") if isinstance(crew, dict) else None
        if not crew_id:
            continue
        istatus, integrations = api.get(f"/api/v1/crews/{crew_id}/integrations")
        irows = integrations if isinstance(integrations, list) else None
        if istatus == 200 and irows:
            bound = (crew_id, irows[0].get("id"))
            break
    if bound is None:
        failures.append(
            "no crew has a bound integration (GET /api/v1/crews/{id}/integrations "
            "returned no rows for any seeded crew); the crew-integration fixture "
            "is not installed")
        return None
    crew_id, integration_id = bound
    if not integration_id:
        failures.append("crew integration row without an id; cannot probe the tools route")
        return None
    status, body = api.get(f"/api/v1/crews/{crew_id}/integrations/{integration_id}/tools")
    if status == 200 and isinstance(body, list):
        print(f"  ok   /crews/{crew_id}/integrations/{integration_id}/tools -> 200 list of {len(body)} (bound pair)")
    else:
        failures.append(
            f"GET /crews/{crew_id}/integrations/{integration_id}/tools: expected 200 with a JSON list for the "
            f"fixture-bound pair, got HTTP {status}: {body}")
    # Negatives: ids that are not bound must not answer 200. These are the
    # shape the gate's uncorrelated pair produced (case 184), asserted here as
    # designed 404s rather than left as unexplained gate findings.
    for label, path in (
        ("unknown crew", f"/api/v1/crews/no-such-crew/integrations/{integration_id}/tools"),
        ("unknown integration", f"/api/v1/crews/{crew_id}/integrations/no-such-integration/tools"),
    ):
        nstatus, nbody = api.get(path)
        if nstatus == 404:
            print(f"  ok   {label} -> 404 (pairing negative)")
        else:
            failures.append(f"GET {path}: expected 404 for {label}, got HTTP {nstatus}: {nbody}")
    return crew_id, integration_id


def main():
    argv = sys.argv[1:]
    pair_file = None
    if len(argv) == 5 and argv[3] == "--write-pair":
        pair_file = argv[4]
        argv = argv[:3]
    if len(argv) != 3:
        raise SystemExit("usage: fixture_probes.py <base-url> <token> <workspace-id> [--write-pair FILE]")
    base, token, workspace = argv
    if not token or not workspace:
        raise SystemExit("token and workspace-id must be non-empty")
    api = Api(base, token, workspace)
    failures = []
    probe_pages(api, failures)
    pair = probe_crew_integration(api, failures)
    print()
    if failures:
        for f in failures:
            print(f"FIXTURE PROBE FAIL: {f}")
        raise SystemExit(1)
    if pair_file:
        # The correlated ids for run.sh's per-operation parameter overlay.
        # Written only after every probe passed, so the gate is never steered
        # onto a pair the probes did not prove.
        with open(pair_file, "w") as fh:
            json.dump({"crew_id": pair[0], "integration_id": pair[1]}, fh)
    print("fixture probes: all passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
