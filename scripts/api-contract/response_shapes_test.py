#!/usr/bin/env python3
import contextlib
import io
import sys
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import response_shapes  # noqa: E402
from response_shapes import (  # noqa: E402
    ROUTES, NotA200, NotJSON, _RefuseRedirects, check, checked_base, denullable,
    fetch, fetch_spec, response_schema,
)


class _FakeResponse:
    """Enough of an http.client.HTTPResponse for `fetch` to read."""

    def __init__(self, status, payload=b'{"rows": []}',
                 content_type="application/json"):
        self.status = status
        self.headers = {"Content-Type": content_type} if content_type else {}
        self._payload = io.BytesIO(payload)

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def read(self, *args):
        return self._payload.read(*args)


def run_main(routes, spec, fetched):
    """Drive main() over a stubbed server and return (exit code, printed output).

    `fetched` maps a route to the body it answers with, or to an exception to
    raise for it.
    """
    def fake_fetch(base, path, token, workspace):
        answer = fetched[path]
        if isinstance(answer, Exception):
            raise answer
        return answer

    buffer = io.StringIO()
    with mock.patch.object(response_shapes, "ROUTES", routes), \
            mock.patch.object(response_shapes, "fetch_spec", return_value=denullable(spec)), \
            mock.patch.object(response_shapes, "fetch", fake_fetch), \
            contextlib.redirect_stdout(buffer):
        code = response_shapes.main(
            ["response_shapes.py", "http://localhost:8082", "token", "ws-1"])
    return code, buffer.getvalue()


class ResponseShapesTest(unittest.TestCase):
    # The failure this whole check exists to catch: a body whose fields have
    # all been renamed. It is only detectable because the schema names its
    # required properties — see docs/prd/response-shape-contract.md.
    SPEC = {
        "paths": {
            "/api/v1/approvals": {
                "get": {"responses": {"200": {"content": {"application/json": {"schema": {
                    "type": "object",
                    "properties": {"rows": {"type": "array", "items": {"$ref": "#/components/schemas/Approval"}}},
                    "required": ["rows"],
                }}}}}}
            }
        },
        "components": {"schemas": {"Approval": {
            "type": "object",
            "properties": {"id": {"type": "string"}, "kind": {"type": "string"},
                           "decided_at": {"type": "string", "nullable": True}},
            "required": ["id", "kind", "decided_at"],
        }}},
    }

    def test_accepts_the_shape_the_server_is_supposed_to_send(self):
        spec = denullable(self.SPEC)
        body = {"rows": [{"id": "ap_1", "kind": "tool_call", "decided_at": None}]}
        self.assertEqual(check(spec, "/api/v1/approvals", body), [])

    def test_rejects_a_body_with_every_field_renamed(self):
        spec = denullable(self.SPEC)
        body = {"rows": [{"ID": "ap_1", "Kind": "tool_call", "DecidedAt": None}]}
        errors = check(spec, "/api/v1/approvals", body)
        self.assertTrue(errors, "a renamed body must not validate — that is the whole point")

    def test_nullable_is_translated_because_json_schema_has_no_such_keyword(self):
        # Without this the first real run reported nine false failures, all of
        # them a legitimate null on a nullable field.
        raw = {"type": "string", "nullable": True}
        self.assertEqual(denullable(raw)["type"], ["string", "null"])
        self.assertNotIn("nullable", denullable(raw))
        # A non-nullable field keeps its scalar type.
        self.assertEqual(denullable({"type": "string"})["type"], "string")

    def test_an_undocumented_route_is_skipped_not_failed(self):
        self.assertIsNone(response_schema(denullable(self.SPEC), "/api/v1/nope"))
        self.assertIsNone(check(denullable(self.SPEC), "/api/v1/nope", {}))

    def test_routes_are_read_only(self):
        # Mirrors run.sh's method deny-list: a contract check must never be the
        # reason a workspace changed.
        for route in ROUTES:
            self.assertTrue(route.startswith("/api/v1/"), route)

    def test_refuses_to_put_the_bearer_token_on_a_cleartext_wire(self):
        # The token goes on every request this script makes; plain http to
        # anything but the local host publishes it.
        for base in ("http://api.example.com", "http://10.0.0.5:8082"):
            with self.assertRaises(SystemExit, msg=base):
                checked_base(base)

    def test_keeps_the_documented_localhost_development_path(self):
        # http://localhost:8082 is what the README tells people to run and what
        # every dev clone answers on. The token never leaves the host.
        self.assertEqual(checked_base("http://localhost:8082"), "http://localhost:8082")
        self.assertEqual(checked_base("http://127.0.0.1:8082"), "http://127.0.0.1:8082")
        self.assertEqual(
            checked_base("https://crewship-dev2.unifylab.cz/"),
            "https://crewship-dev2.unifylab.cz")

    def test_rejects_a_scheme_that_is_neither_http_nor_https(self):
        for base in ("file:///etc/passwd", "ftp://example.com"):
            with self.assertRaises(SystemExit, msg=base):
                checked_base(base)

    def test_refuses_to_follow_a_redirect_rather_than_forward_the_token(self):
        # urllib copies request headers onto a redirect verbatim, excluding
        # only content-length and content-type — so Authorization would follow
        # a 302 to any host. On a GET /api/v1/... a redirect is itself worth
        # reporting, so this raises rather than silently dropping the header.
        handler = _RefuseRedirects()
        request = urllib.request.Request(
            "https://crewship-dev2.unifylab.cz/api/v1/inbox",
            headers={"Authorization": "Bearer sekrit"})
        with self.assertRaises(urllib.error.HTTPError) as caught:
            handler.redirect_request(
                request, io.BytesIO(b""), 302, "Found",
                {}, "https://elsewhere.example.com/api/v1/inbox")
        self.assertIn("elsewhere.example.com", str(caught.exception))
        self.assertNotIn("sekrit", str(caught.exception))


class StatusCodeTest(unittest.TestCase):
    """Only a real 200 may be measured against the documented 200 schema.

    urllib raises on 4xx/5xx, so those were already failures, and redirects are
    refused — which left the 2xx band. A 201 or a 206 sailed through `fetch`
    and its body was then validated against the schema documented for **200**,
    so the checker would have compared a response against a contract that was
    never written for it and called that a pass. Comparing against the wrong
    contract and reporting success is the defect this whole branch is about.
    """

    def test_a_201_is_not_a_200(self):
        with mock.patch.object(response_shapes._opener, "open",
                               return_value=_FakeResponse(201)):
            with self.assertRaises(NotA200) as caught:
                fetch("http://localhost:8082", "/api/v1/approvals", "tok", "ws-1")

        self.assertIn("201", str(caught.exception))

    def test_a_204_is_not_a_200_either(self):
        # The realistic one on a read-only GET: a handler that answers "no
        # content" has no body to compare, and json.load would have failed with
        # a decode error that reads like a broken server.
        with mock.patch.object(response_shapes._opener, "open",
                               return_value=_FakeResponse(204, b"")):
            with self.assertRaises(NotA200):
                fetch("http://localhost:8082", "/api/v1/approvals", "tok", "ws-1")

    def test_the_spec_itself_must_also_be_a_200(self):
        # The symmetric hole. Requiring 200 of the 17 data routes while
        # accepting any 2xx for /openapi.json holds the routes to a rule the
        # DOCUMENT THEY ARE ALL JUDGED AGAINST does not have to meet — the same
        # defect one level up, where it is worth more.
        with mock.patch.object(response_shapes._opener, "open",
                               return_value=_FakeResponse(201, b'{"paths": {}}')):
            with self.assertRaises(NotA200):
                fetch_spec("http://localhost:8082")

    def test_a_json_body_under_the_wrong_media_type_is_not_json(self):
        # `response_schema` reads the schema out of the "application/json" entry
        # of the documented 200. A text/plain body that happens to parse is
        # still not the response that schema describes, so grading it against
        # that schema is comparing against a contract the server did not claim.
        with mock.patch.object(response_shapes._opener, "open",
                               return_value=_FakeResponse(200, content_type="text/plain")):
            with self.assertRaises(NotJSON):
                fetch("http://localhost:8082", "/api/v1/approvals", "tok", "ws-1")

    def test_a_charset_parameter_is_still_application_json(self):
        # RFC 9110 media types carry parameters. Rejecting
        # "application/json; charset=utf-8" would fail every server that spells
        # it out, which is a checker that cries wolf — the failure mode the PRD
        # warns about for the whole gate family.
        with mock.patch.object(
                response_shapes._opener, "open",
                return_value=_FakeResponse(200, content_type="application/json; charset=utf-8")):
            self.assertEqual(
                fetch("http://localhost:8082", "/api/v1/approvals", "tok", "ws-1"),
                {"rows": []})

    def test_a_200_is_read_normally(self):
        with mock.patch.object(response_shapes._opener, "open",
                               return_value=_FakeResponse(200)):
            self.assertEqual(
                fetch("http://localhost:8082", "/api/v1/approvals", "tok", "ws-1"),
                {"rows": []})


class ExitStatusTest(unittest.TestCase):
    """What the exit code is allowed to mean.

    `ROUTES` is a hand-curated list of read-only GETs that a reachable server
    with a workspace-owner token must answer, and the routes that cannot be
    checked are already commented out of it with reasons. So on this list there
    is no such thing as an excusable skip: an unreachable route or an
    undocumented 200 is a defect in the server, the document or the credentials,
    and in every case the run verified less than it claims to.
    """

    SPEC = ResponseShapesTest.SPEC
    GOOD = {"rows": [{"id": "ap_1", "kind": "tool_call", "decided_at": None}]}

    def test_a_run_that_verified_nothing_is_not_a_pass(self):
        # The regression. Every route unreachable — a wrong token, a server on
        # the wrong port, a workspace id that is not the operator's — used to
        # print "0 pass, 0 fail, 17 skipped" and exit 0. A checker that
        # exercised nothing reported success, which is the exact shape of green
        # this checker was written to make impossible.
        code, out = run_main(
            ["/api/v1/approvals"], self.SPEC,
            {"/api/v1/approvals": urllib.error.URLError("connection refused")})

        self.assertEqual(code, 1, out)
        self.assertIn("0 of 1", out)

    def test_an_undocumented_response_is_not_a_pass_either(self):
        # The other silent exit: the route answers, but the served document has
        # no 200 schema for it, so nothing was compared. `check` returning None
        # is the right answer for an arbitrary path and the wrong one to shrug
        # at for a route this file declares.
        code, out = run_main(
            ["/api/v1/not-documented"], self.SPEC,
            {"/api/v1/not-documented": {"anything": True}})

        self.assertEqual(code, 1, out)
        self.assertIn("documents no 200", out)

    def test_a_real_violation_still_fails(self):
        code, out = run_main(
            ["/api/v1/approvals"], self.SPEC,
            {"/api/v1/approvals": {"rows": [{"ID": "ap_1", "Kind": "tool_call"}]}})

        self.assertEqual(code, 1, out)
        self.assertIn("FAIL", out)

    def test_an_empty_route_list_cannot_report_success(self):
        # `passed == len(ROUTES)` is satisfied by 0 == 0, so emptying the list —
        # a bad merge, a filter that matched nothing, someone commenting out the
        # last entry while debugging — printed "0 of 0 routes verified" and
        # exited 0. Same false green as the one this file was hardened against,
        # reached from the other side, so the invariant has to say that a run
        # must have had something to check.
        code, out = run_main([], self.SPEC, {})

        self.assertEqual(code, 1, out)
        self.assertIn("no routes", out)

    def test_every_route_passing_is_the_only_way_to_exit_zero(self):
        code, out = run_main(
            ["/api/v1/approvals"], self.SPEC, {"/api/v1/approvals": self.GOOD})

        self.assertEqual(code, 0, out)
        self.assertIn("1 of 1", out)

    def test_one_bad_route_among_good_ones_fails_the_run(self):
        code, out = run_main(
            ["/api/v1/approvals", "/api/v1/other"], self.SPEC,
            {"/api/v1/approvals": self.GOOD,
             "/api/v1/other": urllib.error.URLError("refused")})

        self.assertEqual(code, 1, out)


if __name__ == "__main__":
    unittest.main()
