import copy
import json
import unittest

from build_routine import build
from review_guard import check_event, complete, marker, packet, preflight, publication


class ReviewGuardTests(unittest.TestCase):
    def setUp(self):
        self.config = {"repo": "crewship-ai/webhook-review-sandbox", "repository_id": 123,
                       "pr_number": 1, "base_ref": "main", "reviewer_login": "review-bot"}
        repo = {"id": 123, "full_name": self.config["repo"]}
        self.pr = {"number": 1, "state": "open", "base": {"repo": repo, "ref": "main", "sha": "a" * 40},
                   "head": {"repo": repo, "sha": "b" * 40}}
        self.event = {"number": 1, "action": "opened", "repository": repo, "pull_request": self.pr}
        self.snap = check_event(self.config, self.event)
        self.comparison = {"base_commit": {"sha": "a" * 40}, "commits": [{"sha": "b" * 40}],
                           "total_commits": 1, "files": [{"filename": "calc.py", "status": "modified",
                           "patch": "@@ -1 +1 @@\n-return total\n+return 0", "additions": 1, "deletions": 1}]}

    def review(self, login="review-bot"):
        return {"user": {"login": login}, "commit_id": "b" * 40, "body": marker(self.config, self.snap)}

    def test_event_scope_rejects_other_repo_pr_fork_and_branch(self):
        for change in (lambda e: e["repository"].update(id=999),
                       lambda e: e.update(number=2),
                       lambda e: e["pull_request"]["head"].update(repo={"id": 999}),
                       lambda e: e["pull_request"]["base"].update(ref="other"),
                       lambda e: e.update(action="closed")):
            event = copy.deepcopy(self.event)
            change(event)
            with self.assertRaises(ValueError):
                check_event(self.config, event)

    def test_stale_event_never_reaches_review(self):
        current = copy.deepcopy(self.pr)
        current["head"]["sha"] = "c" * 40
        with self.assertRaisesRegex(ValueError, "stale"):
            preflight(self.config, self.event, current, [])

    def test_duplicate_and_spoofed_review_marker(self):
        self.assertFalse(preflight(self.config, self.event, self.pr, [self.review()])["proceed"])
        self.assertTrue(preflight(self.config, self.event, self.pr, [self.review("someone-else")])["proceed"])

    def test_partial_history_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "pagination"):
            preflight(self.config, self.event, self.pr, [{}] * 100)

    def test_diff_pinned_to_snapshot(self):
        self.assertEqual(packet(self.config, self.snap, self.comparison)["commit"], "b" * 40)
        self.comparison["commits"][-1]["sha"] = "c" * 40
        with self.assertRaises(ValueError):
            packet(self.config, self.snap, self.comparison)

    def test_incomplete_or_oversized_diff_refused(self):
        for change in (lambda c: c.update(total_commits=2),
                       lambda c: c["files"][0].update(patch=None),
                       lambda c: c["files"][0].update(additions=4),
                       lambda c: c.update(files=c["files"] * 21),
                       lambda c: c["files"][0].update(patch="+" + "x" * 31000, deletions=0)):
            comparison = copy.deepcopy(self.comparison)
            change(comparison)
            with self.assertRaises(ValueError):
                packet(self.config, self.snap, comparison)

    def test_publish_is_comment_bound_to_reviewed_sha(self):
        result = publication(self.config, self.snap, self.pr, [], "Bug: @owner <script>")
        self.assertEqual(result["request"]["event"], "COMMENT")
        self.assertEqual(result["request"]["commit_id"], "b" * 40)
        self.assertNotIn("@owner", result["request"]["body"])
        self.assertNotIn("<script>", result["request"]["body"])

    def test_publish_rechecks_head_base_state_and_existing_review(self):
        for change in (lambda p: p["head"].update(sha="c" * 40),
                       lambda p: p["base"].update(sha="d" * 40),
                       lambda p: p.update(state="closed")):
            current = copy.deepcopy(self.pr)
            change(current)
            with self.assertRaises(ValueError):
                publication(self.config, self.snap, current, [], "finding")
        self.assertFalse(publication(self.config, self.snap, self.pr, [self.review()], "finding")["proceed"])

    def test_agent_does_not_choose_destination_or_receive_credential_reference(self):
        routine = build(self.config, "reviewer")
        agent_step = next(step for step in routine["steps"] if step["type"] == "agent_run")
        self.assertNotIn("secrets.", agent_step["prompt"])
        publish = next(step for step in routine["steps"] if step["id"] == "publish")
        self.assertEqual(publish["http"]["url"], "https://api.github.com/repos/crewship-ai/webhook-review-sandbox/pulls/1/reviews")
        self.assertEqual(publish["if"], "{{ steps.publication.output.proceed }}")

    def test_success_outcome_requires_actual_published_review(self):
        snap = {**self.snap, "proceed": True}
        response = {**self.review(), "id": 456, "state": "COMMENTED"}
        self.assertIn("outcome: SUCCEEDED", complete(self.config, snap, '{"proceed":true}', json.dumps(response)))
        response["commit_id"] = "c" * 40
        with self.assertRaises(ValueError):
            complete(self.config, snap, '{"proceed":true}', json.dumps(response))
        self.assertIn("outcome: NO_CHANGE", complete(self.config, {**snap, "proceed": False}, "<skipped>", "<skipped>"))


if __name__ == "__main__":
    unittest.main()
