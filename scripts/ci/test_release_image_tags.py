"""The release image tag list and how release.yml passes it between jobs.

GitHub drops a job output whose value contains a secret's value. A secret
that is a lone `-` drops every output containing a hyphen, and every image
tag contains one (ghcr.io/crewship-ai/...), so promote received an empty
list for v0.1.0-rc.2. The list is now computed where it is used.
"""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/ci/release-image-tags.sh'
WORKFLOW = ROOT / '.github/workflows/release.yml'
BASE = 'ghcr.io/crewship-ai/crewship'


def workflow():
    return yaml.safe_load(WORKFLOW.read_text())


def named_step(job, name):
    matches = [step for step in job['steps'] if step.get('name') == name]
    assert len(matches) == 1, (name, len(matches))
    return matches[0]


def code_lines(run):
    """The run script's lines without blank or comment lines."""
    return [line.strip() for line in run.splitlines() if line.strip() and not line.strip().startswith('#')]


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

    def test_tag_length_limit_is_128(self):
        longest = 'v1.2.3-' + 'a' * 121
        self.assertEqual(len(longest), 128)
        result = self.run_script(longest, BASE)
        self.assertEqual((result.returncode, result.stdout), (0, f'{BASE}:{longest}\n'))
        result = self.run_script(longest + 'a', BASE)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, '')

    def test_refuses_empty_or_malformed_input(self):
        for args in (('', BASE), ('1.2.3', BASE), ('v1.2', BASE), ('v1.2.3 ', BASE),
                     ('v1.2.3\nlatest', BASE), ('v1.2.3-', BASE), ('v1.2.3', ''),
                     ('v1.2.3', 'ghcr.io/x/y:tag'), ('v1.2.3',), ()):
            with self.subTest(args=args):
                result = self.run_script(*args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, '')

    def test_promote_computes_the_list_instead_of_reading_a_job_output(self):
        promote = workflow()['jobs']['promote']
        self.assertEqual(promote['permissions'], {'contents': 'read', 'packages': 'write'})
        step = named_step(promote, 'Promote the tested digest after binary publication')
        self.assertEqual(step['env'], {'IMAGE': 'ghcr.io/${{ github.repository_owner }}/crewship',
                                       'DIGEST': '${{ needs.docker.outputs.digest }}'})
        lines = code_lines(step['run'])
        self.assertEqual(lines[0], 'set -euo pipefail')
        self.assertIn('[[ "$DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "::error::malformed image digest: '
                      "'$DIGEST'\"; exit 1; }", lines)
        call = [line for line in lines if 'release-image-tags.sh' in line]
        self.assertEqual(call, ['TAGS="$(bash scripts/ci/release-image-tags.sh "$GITHUB_REF_NAME" "$IMAGE")"'])
        self.assertNotIn('||', call[0])
        self.assertNotIn('TAGS:', step['run'])
        self.assertIn('[ "${#args[@]}" -gt 0 ]', lines)
        self.assertEqual([line for line in lines if 'imagetools' in line],
                         ['docker buildx imagetools create "${args[@]}" "$IMAGE@$DIGEST"'])
        self.assertEqual(lines[-1], 'docker buildx imagetools create "${args[@]}" "$IMAGE@$DIGEST"')

    def promote(self, ref, digest='sha256:' + 'a' * 64):
        step = named_step(workflow()['jobs']['promote'], 'Promote the tested digest after binary publication')
        with tempfile.TemporaryDirectory() as temporary:
            bin_dir = Path(temporary)
            calls = bin_dir / 'calls'
            (bin_dir / 'docker').write_text('#!/usr/bin/env bash\nprintf "%s\\n" "$@" >> "$CALLS"\n')
            (bin_dir / 'docker').chmod(0o755)
            env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], CALLS=str(calls),
                       GITHUB_REF_NAME=ref, IMAGE=BASE, DIGEST=digest)
            result = subprocess.run(['bash', '-c', step['run']], cwd=ROOT, env=env,
                                    capture_output=True, text=True, timeout=10)
            return result.returncode, calls.read_text().splitlines() if calls.exists() else None

    def test_promote_step_tags_exactly_the_tested_digest(self):
        digest = 'sha256:' + 'b' * 64
        self.assertEqual(self.promote('v0.1.0-rc.2', digest),
                         (0, ['buildx', 'imagetools', 'create', '--tag', f'{BASE}:v0.1.0-rc.2', f'{BASE}@{digest}']))
        self.assertEqual(self.promote('v1.2.3', digest),
                         (0, ['buildx', 'imagetools', 'create', '--tag', f'{BASE}:v1.2.3', '--tag', f'{BASE}:v1.2',
                              '--tag', f'{BASE}:latest', f'{BASE}@{digest}']))
        for ref, bad in (('v1.2', digest), ('', digest), ('v1.2.3', ''), ('v1.2.3', 'sha256:' + 'A' * 64),
                         ('v1.2.3', 'sha256:' + 'a' * 63), ('v1.2.3', 'latest')):
            with self.subTest(ref=ref, digest=bad):
                code, calls = self.promote(ref, bad)
                self.assertNotEqual(code, 0)
                self.assertIsNone(calls)

    def test_only_hyphen_free_values_cross_jobs(self):
        # A sha256 digest cannot contain a hyphen; nothing else may be a job output.
        source = WORKFLOW.read_text()
        used = sorted(set(re.findall(r'needs\.[A-Za-z0-9_-]+\.outputs\.[A-Za-z0-9_-]+', source)))
        self.assertEqual(used, ['needs.docker.outputs.digest'])
        self.assertNotIn('tags: ${{ steps.tags.outputs.list }}', source)

    def test_craft_pins_come_from_the_committed_catalog_the_build_reproduces(self):
        jobs = workflow()['jobs']
        check = named_step(jobs['craft-components'], 'Require the committed catalog')
        self.assertEqual(code_lines(check['run']), [
            'set -euo pipefail',
            'bash scripts/ci/craft-catalog-pins.sh build/craft-components/dist/catalog.txt',
            'cmp build/craft-components/dist/catalog.txt tools/craft-components/catalog.txt',
        ])
        read = 'bash scripts/ci/craft-catalog-pins.sh tools/craft-components/catalog.txt >> "$GITHUB_OUTPUT"'
        pins = '${{ steps.catalog.outputs.pins }}'
        for name in ('release', 'docker'):
            steps = jobs[name]['steps']
            ids = [step.get('id') for step in steps]
            self.assertEqual(ids.count('catalog'), 1, name)
            self.assertEqual(steps[ids.index('catalog')]['run'], read)
        release = jobs['release']
        self.assertEqual(named_step(release, 'Verify the Word and Sheet packages against their pins')['env'],
                         {'PINS': pins})
        self.assertEqual(named_step(release, 'Publish the Word and Sheet packages with the archives')['env'],
                         {'PINS': pins})
        self.assertEqual(named_step(release, 'Build and sign final release assets without publishing')
                         ['env']['CRAFT_COMPONENT_PINS'], pins)
        docker = jobs['docker']['steps']
        names = [step.get('name') for step in docker]
        guard = names.index('Require the pinned catalog before building')
        build = names.index('Build immutable release image candidate')
        self.assertLess(docker.index(next(s for s in docker if s.get('id') == 'catalog')), guard)
        self.assertLess(guard, build)
        self.assertEqual(docker[guard]['env'], {'PINS': pins})
        self.assertNotIn('if', docker[guard])
        self.assertEqual(code_lines(docker[guard]['run'])[:2], [
            'set -euo pipefail',
            '[[ "$PINS" =~ ^[a-z]+=[A-Za-z0-9.+_-]+=[0-9a-f]{64}(,[a-z]+=[A-Za-z0-9.+_-]+=[0-9a-f]{64})*$ ]] || {'])
        good = Path(ROOT / 'tools/craft-components/catalog.txt').read_text().strip()
        for value, ok in ((good, True), ('', False), ('wordcraft=1.0', False), (good + ',', False)):
            with self.subTest(pins=value):
                result = subprocess.run(['bash', '-c', docker[guard]['run']], env=dict(os.environ, PINS=value),
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode == 0, ok, result.stderr)
        args = docker[build]['with']['build-args'].splitlines()
        self.assertEqual([a for a in args if a.startswith('CRAFT_COMPONENT_PINS=')],
                         ['CRAFT_COMPONENT_PINS=' + pins])

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
