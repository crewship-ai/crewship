#!/usr/bin/env python3
"""Run the cross-language Go contracts and require named passing evidence."""
import json
import subprocess
import sys

TESTS = {
    './cmd/crewship': {'TestProvidersDocMatchesAutoDetectionOrder'},
    './internal/api': {
        'TestCrewIcons_MirrorsWebVocabulary',
        'TestPageFolderIcons_MirrorTheCrewIconRegistry',
        'TestPageFolderColors_MirrorTheCrewPalette',
    },
    './internal/notify': {'TestFrontendCategoriesMatchBackend'},
    './internal/backup': {'TestContentCategories_MatchTheConsole'},
}


def missing_tests(events):
    passed = {(e.get('Package'), e.get('Test')) for e in events if e.get('Action') == 'pass'}
    required = {('github.com/crewship-ai/crewship/' + path.removeprefix('./'), name)
                for path, names in TESTS.items() for name in names}
    return sorted(required - passed)


def main():
    names = sorted(name for tests in TESTS.values() for name in tests)
    proc = subprocess.Popen([
        'go', 'test', *TESTS, '-json', '-count=1', '-timeout', '8m',
        '-run', '^(' + '|'.join(names) + ')$',
    ], stdout=subprocess.PIPE, text=True)
    events = []
    for line in proc.stdout:
        event = json.loads(line)
        events.append(event)
        if event.get('Output'):
            print(event['Output'], end='', flush=True)
    result = proc.wait()
    missing = missing_tests(events)
    if missing:
        print(f'::error::Required parity tests did not pass: {missing}', file=sys.stderr)
    return result or bool(missing)


if __name__ == '__main__':
    sys.exit(main())
