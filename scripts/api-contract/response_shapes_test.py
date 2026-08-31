#!/usr/bin/env python3
import io
import sys
import unittest
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from response_shapes import (  # noqa: E402
    ROUTES, _RefuseRedirects, check, checked_base, denullable, response_schema,
)


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


if __name__ == "__main__":
    unittest.main()
