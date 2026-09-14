import unittest
from verified_commit import verdict


class VerifiedCommitTests(unittest.TestCase):
    def run_record(self, **kw):
        return dict(id=1, head_sha='a' * 40, event='push', head_branch='main',
                    head_repository={'full_name': 'owner/repo'}, status='completed', conclusion='success', **kw)

    def test_only_matching_trusted_push(self):
        record = self.run_record()
        self.assertEqual(verdict([record], 'a' * 40, 'owner/repo'), 'success')
        for key, value in [('head_sha', 'b' * 40), ('event', 'pull_request'), ('head_branch', 'feature'), ('head_repository', {'full_name': 'fork/repo'})]:
            self.assertEqual(verdict([record | {key: value}], 'a' * 40, 'owner/repo'), 'pending')

    def test_new_failure_or_pending_cannot_reuse_old_green(self):
        good = self.run_record()
        for status in ['failure', 'cancelled', 'skipped', None]:
            self.assertEqual(verdict([good, good | {'id': 2, 'conclusion': status}], 'a' * 40, 'owner/repo'), 'failure')
        self.assertEqual(verdict([good, good | {'id': 2, 'status': 'in_progress'}], 'a' * 40, 'owner/repo'), 'pending')
        self.assertEqual(verdict([], 'a' * 40, 'owner/repo'), 'pending')


if __name__ == '__main__':
    unittest.main()
