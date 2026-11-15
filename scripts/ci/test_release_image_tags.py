"""The release image tag list and how release.yml passes it between jobs.

GitHub drops a job output whose value contains a secret's value. A secret
that is a lone `-` drops every output containing a hyphen, and every image
tag contains one (ghcr.io/crewship-ai/...), so promote received an empty
list for v0.1.0-rc.2. The list is now computed where it is used.
"""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/ci/release-image-tags.sh'
WORKFLOW = ROOT / '.github/workflows/release.yml'
BASE = 'ghcr.io/crewship-ai/crewship'


def job(source, name):
    body = source.split(f'\n  {name}:\n', 1)[1]
    return re.split(r'\n  [A-Za-z0-9_-]+:\n', body, maxsplit=1)[0]


class ReleaseImageTagsTest(unittest.TestCase):
    def run_script(self, *args):
        return subprocess.run(['bash', str(SCRIPT), *args], capture_output=True,
                              text=True, timeout=10)

    def test_stable_release_gets_majmin_and_latest(self):
        result = self.run_script('v1.2.3', BASE)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(),
                         [f'{BASE}:v1.2.3', f'{BASE}:v1.2', f'{BASE}:latest'])

    def test_prerelease_gets_only_its_full_version(self):
        result = self.run_script('v0.1.0-rc.2', BASE)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [f'{BASE}:v0.1.0-rc.2'])

    def test_refuses_empty_or_malformed_input(self):
        for args in (('', BASE), ('1.2.3', BASE), ('v1.2', BASE), ('v1.2.3 ', BASE),
                     ('v1.2.3\nlatest', BASE), ('v1.2.3-', BASE), ('v1.2.3', ''),
                     ('v1.2.3', 'ghcr.io/x/y:tag'), ('v1.2.3',), ()):
            with self.subTest(args=args):
                result = self.run_script(*args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, '')

    def test_promote_computes_the_list_instead_of_reading_a_job_output(self):
        source = WORKFLOW.read_text()
        promote = job(source, 'promote')
        self.assertNotIn('needs.docker.outputs.tags', promote)
        self.assertIn('scripts/ci/release-image-tags.sh', promote)
        self.assertIn('"$GITHUB_REF_NAME"', promote)
        self.assertIn('[ "${#args[@]}" -gt 0 ]', promote)
        self.assertIn('needs.docker.outputs.digest', promote)

    def test_only_hyphen_free_values_cross_jobs(self):
        # A sha256 digest cannot contain a hyphen; nothing else may be a job output.
        source = WORKFLOW.read_text()
        used = sorted(set(re.findall(r'needs\.[A-Za-z0-9_-]+\.outputs\.[A-Za-z0-9_-]+', source)))
        self.assertEqual(used, ['needs.docker.outputs.digest'])
        self.assertNotIn('tags: ${{ steps.tags.outputs.list }}', source)
        # The jobs that need the craft pins read the committed catalog, which
        # craft-components requires the build to reproduce.
        self.assertIn('cmp build/craft-components/dist/catalog.txt tools/craft-components/catalog.txt',
                      job(source, 'craft-components'))
        for name in ('release', 'docker'):
            self.assertIn('craft-catalog-pins.sh tools/craft-components/catalog.txt', job(source, name))

    def test_catalog_pins_accepts_the_committed_catalog_and_refuses_garbage(self):
        script = ROOT / 'scripts/ci/craft-catalog-pins.sh'
        good = subprocess.run(['bash', str(script), str(ROOT / 'tools/craft-components/catalog.txt')],
                              capture_output=True, text=True, timeout=10)
        self.assertEqual(good.returncode, 0, good.stderr)
        self.assertRegex(good.stdout, r'^pins=[a-z]+=\S+=[0-9a-f]{64}(,[a-z]+=\S+=[0-9a-f]{64})*\n$')
        bad = subprocess.run(['bash', str(script), str(SCRIPT)], capture_output=True, text=True, timeout=10)
        self.assertNotEqual(bad.returncode, 0)
        self.assertEqual(bad.stdout, '')


if __name__ == '__main__':
    unittest.main()
