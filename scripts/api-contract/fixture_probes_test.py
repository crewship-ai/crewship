#!/usr/bin/env python3
"""Unit tests for fixture_probes.py — the probe logic against stubbed answers.

The probe's own contract: exit 0 ONLY when every probe positively passed, and
name every failure otherwise. The cases pin the two ways a checker like this
silently rots: a fixture-shaped gap reported as a pass (no crew-integration
binding, no project draft), and a designed negative that stopped being one
(revision 0 answering 200).
"""
import contextlib
import io
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import fixture_probes  # noqa: E402


class ApiStub:
    """Answers GETs from a path -> (status, body) table."""

    def __init__(self, answers):
        self.answers = answers
        self.calls = []

    def get(self, path):
        self.calls.append(path)
        return self.answers[path]


def run(answers):
    stub = ApiStub(answers)
    buffer = io.StringIO()
    failures = []
    with mock.patch.object(fixture_probes, "Api", return_value=stub), \
            contextlib.redirect_stdout(buffer):
        fixture_probes.probe_pages(stub, failures)
        fixture_probes.probe_crew_integration(stub, failures)
    return failures, buffer.getvalue(), stub.calls


def happy_answers():
    return {
        "/api/v1/pages": (200, [{"slug": "demo-marketing"}, {"slug": "bare-page"}]),
        "/api/v1/pages/demo-marketing/project": (200, {"revision": 1, "digest": "d"}),
        "/api/v1/pages/demo-marketing/project/fsck": (200, {"healthy": True}),
        "/api/v1/pages/demo-marketing/project/preview": (200, {"build": None}),
        "/api/v1/pages/demo-marketing/project/publications": (200, {"publications": []}),
        "/api/v1/pages/demo-marketing/project/review": (200, {"baseline": {}}),
        "/api/v1/pages/demo-marketing/project/source": (200, {"revision": 1}),
        "/api/v1/pages/demo-marketing/project/history": (200, {"revisions": [{"revision": 1}]}),
        "/api/v1/pages/demo-marketing/project/history/1": (200, {"revision": 1}),
        "/api/v1/pages/demo-marketing/project/history/1/source": (200, {"revision": 1}),
        "/api/v1/pages/demo-marketing/project/history/0": (400, {"error": "revision must be positive"}),
        "/api/v1/pages/demo-marketing/project/history/99999999": (404, {"error": "project revision not found"}),
        "/api/v1/pages/bare-page/project": (200, {"error": "none"}),  # no revision -> skipped
        "/api/v1/crews": (200, [{"id": "crew-1"}, {"id": "crew-2"}]),
        "/api/v1/crews/crew-1/integrations": (200, []),
        "/api/v1/crews/crew-2/integrations": (200, [{"id": "integ-2"}]),
        "/api/v1/crews/crew-2/integrations/integ-2/tools": (200, []),
        "/api/v1/crews/no-such-crew/integrations/integ-2/tools": (404, {"error": "Crew integration not found"}),
        "/api/v1/crews/crew-2/integrations/no-such-integration/tools": (404, {"error": "Crew integration not found"}),
    }


class HappyPath(unittest.TestCase):
    def test_all_probes_pass(self):
        failures, out, calls = run(happy_answers())
        self.assertEqual(failures, [])
        # The positive revision probe must use a revision read from the
        # fixture's history, not a constant.
        self.assertIn("/api/v1/pages/demo-marketing/project/history/1/source", calls)
        self.assertIn("/api/v1/crews/crew-2/integrations/integ-2/tools", calls)

    def test_page_without_usable_draft_is_skipped_in_favour_of_one_with(self):
        # bare-page answers 200 with no revision field; the probe must move
        # on to demo-marketing rather than fail or probe bare-page's history.
        failures, out, calls = run(happy_answers())
        self.assertEqual(failures, [])
        self.assertNotIn("/api/v1/pages/bare-page/project/history", calls)


class FixtureGaps(unittest.TestCase):
    def test_no_crew_integration_binding_fails(self):
        answers = happy_answers()
        answers["/api/v1/crews/crew-2/integrations"] = (200, [])
        failures, _, _ = run(answers)
        self.assertTrue(any("crew has a bound integration" in f for f in failures))

    def test_tools_route_not_200_for_bound_pair_fails(self):
        answers = happy_answers()
        answers["/api/v1/crews/crew-2/integrations/integ-2/tools"] = (
            404, {"error": "Crew integration not found"})
        failures, _, _ = run(answers)
        self.assertTrue(any("fixture-bound pair" in f for f in failures))

    def test_no_project_draft_fails(self):
        answers = happy_answers()
        for slug in ("demo-marketing", "bare-page"):
            answers[f"/api/v1/pages/{slug}/project"] = (
                503, {"error": "Page project storage is not configured"})
        failures, _, _ = run(answers)
        self.assertTrue(any("project draft" in f for f in failures))

    def test_empty_page_list_fails(self):
        answers = happy_answers()
        answers["/api/v1/pages"] = (200, [])
        failures, _, _ = run(answers)
        self.assertTrue(any("non-empty seeded page list" in f for f in failures))


class PairingNegatives(unittest.TestCase):
    def test_unknown_crew_answering_200_fails(self):
        answers = happy_answers()
        answers["/api/v1/crews/no-such-crew/integrations/integ-2/tools"] = (200, [])
        failures, _, _ = run(answers)
        self.assertTrue(any("unknown crew" in f for f in failures))

    def test_unknown_integration_answering_200_fails(self):
        answers = happy_answers()
        answers["/api/v1/crews/crew-2/integrations/no-such-integration/tools"] = (200, [])
        failures, _, _ = run(answers)
        self.assertTrue(any("unknown integration" in f for f in failures))

    def test_tools_object_instead_of_list_fails(self):
        answers = happy_answers()
        answers["/api/v1/crews/crew-2/integrations/integ-2/tools"] = (200, {"tools": []})
        failures, _, _ = run(answers)
        self.assertTrue(any("JSON list" in f for f in failures))

    def test_pair_is_returned_for_the_gate_overlay(self):
        stub = ApiStub(happy_answers())
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(fixture_probes.probe_crew_integration(stub, []), ("crew-2", "integ-2"))


class StatusIsPreserved(unittest.TestCase):
    """Api.get used to return a hardcoded 200 for every successful response."""

    def _api_answering(self, status, body=b"{}"):
        class Resp:
            def __init__(self):
                self.status = status
            def read(self):
                return body
            def __enter__(self):
                return self
            def __exit__(self, *a):
                return False
        api = fixture_probes.Api("http://127.0.0.1:1", "t", "w")
        return api, mock.patch.object(fixture_probes._opener, "open", return_value=Resp())

    def test_202_is_not_reported_as_200(self):
        api, patch = self._api_answering(202)
        with patch:
            status, _ = api.get("/x")
        self.assertEqual(status, 202)

    def test_202_fails_a_probe_that_expects_200(self):
        answers = happy_answers()
        answers["/api/v1/pages/demo-marketing/project/fsck"] = (202, {"accepted": True})
        failures, _, _ = run(answers)
        self.assertTrue(any("project/fsck: expected 200, got HTTP 202" in f for f in failures))

    def test_204_empty_body_keeps_its_status(self):
        api, patch = self._api_answering(204, b"")
        with patch:
            self.assertEqual(api.get("/x"), (204, None))


class DesignedNegatives(unittest.TestCase):
    def test_revision_zero_answering_200_fails(self):
        answers = happy_answers()
        answers["/api/v1/pages/demo-marketing/project/history/0"] = (200, {"revision": 0})
        failures, _, _ = run(answers)
        self.assertTrue(any("history/0: expected 400" in f for f in failures))

    def test_absent_revision_answering_200_fails(self):
        answers = happy_answers()
        answers["/api/v1/pages/demo-marketing/project/history/99999999"] = (200, {"revision": 99999999})
        failures, _, _ = run(answers)
        self.assertTrue(any("history/99999999: expected 404" in f for f in failures))

    def test_history_without_revisions_fails(self):
        answers = happy_answers()
        answers["/api/v1/pages/demo-marketing/project/history"] = (200, {"revisions": []})
        failures, _, _ = run(answers)
        self.assertTrue(any("project/history: expected 200 with revisions" in f for f in failures))


class TokenSafety(unittest.TestCase):
    def test_plain_http_refused_off_loopback(self):
        with self.assertRaises(SystemExit):
            fixture_probes.checked_base("http://example.com:8080")

    def test_loopback_http_allowed(self):
        self.assertEqual(fixture_probes.checked_base("http://127.0.0.1:8080/"),
                         "http://127.0.0.1:8080")


if __name__ == "__main__":
    unittest.main()
