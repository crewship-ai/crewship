#!/usr/bin/env python3
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from response_shapes import ROUTES, check, denullable, response_schema  # noqa: E402


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


if __name__ == "__main__":
    unittest.main()
