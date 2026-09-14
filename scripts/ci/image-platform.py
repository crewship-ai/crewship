#!/usr/bin/env python3
"""Select an immutable platform manifest from an immutable image index."""
import json
import re
import sys


def resolve(image, platform, manifest):
    repository, separator, digest = image.rpartition('@')
    if not repository or not separator or not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
        raise ValueError('image must identify an immutable sha256 digest')
    if not re.fullmatch(r'[a-z0-9]+/[a-z0-9]+(?:/[a-z0-9]+)?', platform):
        raise ValueError('platform must be os/architecture[/variant]')
    if 'manifests' not in manifest:
        if manifest.get('mediaType') not in (
            'application/vnd.oci.image.manifest.v1+json',
            'application/vnd.docker.distribution.manifest.v2+json',
        ):
            raise ValueError('expected an image manifest or platform index')
        return image
    os_name, architecture, *variant = platform.split('/')
    matches = [entry for entry in manifest['manifests']
               if entry.get('platform', {}).get('os') == os_name
               and entry.get('platform', {}).get('architecture') == architecture
               and (not variant or entry['platform'].get('variant') == variant[0])]
    if len(matches) != 1:
        raise ValueError(f'expected exactly one manifest for {platform}, found {len(matches)}')
    child = matches[0].get('digest', '')
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', child):
        raise ValueError('platform manifest has an invalid digest')
    return f'{repository}@{child}'


if __name__ == '__main__':
    try:
        print(resolve(sys.argv[1], sys.argv[2], json.load(sys.stdin)))
    except (ValueError, TypeError, KeyError, IndexError, AttributeError) as error:
        sys.exit(f'Cannot resolve image platform: {error}')
