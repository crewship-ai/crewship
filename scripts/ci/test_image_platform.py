"""Keep both architectures pinned to their index without legacy-store collisions."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('image_platform', Path(__file__).with_name('image-platform.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
IMAGE = 'ghcr.io/example/image@sha256:' + 'a' * 64


def entry(architecture, digest='b', **platform):
    return dict(digest='sha256:' + digest * 64,
                platform=dict(os='linux', architecture=architecture, **platform))


class ImagePlatformTests(unittest.TestCase):
    def test_architectures_use_distinct_children_and_ignore_attestations(self):
        index = dict(manifests=[entry('amd64'), entry('arm64', 'c'), entry('unknown', 'd')])
        for arch, digest in [('amd64', 'b'), ('arm64', 'c')]:
            self.assertEqual(module.resolve(IMAGE, f'linux/{arch}', index),
                             'ghcr.io/example/image@sha256:' + digest * 64)

    def test_single_platform_digest_is_preserved(self):
        for media in ['application/vnd.oci.image.manifest.v1+json',
                      'application/vnd.docker.distribution.manifest.v2+json']:
            self.assertEqual(module.resolve(IMAGE, 'linux/amd64', dict(mediaType=media)), IMAGE)

    def test_missing_and_ambiguous_platforms_fail(self):
        for entries in [[], [entry('arm64')], [entry('amd64'), entry('amd64', 'c')]]:
            with self.subTest(entries=entries), self.assertRaises(ValueError):
                module.resolve(IMAGE, 'linux/amd64', dict(manifests=entries))

    def test_variant_must_match_when_requested(self):
        index = dict(manifests=[entry('arm', variant='v6'), entry('arm', 'c', variant='v7')])
        self.assertTrue(module.resolve(IMAGE, 'linux/arm/v7', index).endswith('c' * 64))
        with self.assertRaises(ValueError):
            module.resolve(IMAGE, 'linux/arm', index)

    def test_bad_identity_or_manifest_is_rejected(self):
        index = dict(manifests=[entry('amd64')])
        for image, platform, manifest in [
            ('ghcr.io/example/image:nightly', 'linux/amd64', index),
            (IMAGE, 'amd64', index), (IMAGE, 'linux/amd64', {}),
            (IMAGE, 'linux/amd64', dict(manifests=[dict(entry('amd64'), digest='broken')])),
        ]:
            with self.subTest(image=image, platform=platform, manifest=manifest), self.assertRaises(ValueError):
                module.resolve(image, platform, manifest)
