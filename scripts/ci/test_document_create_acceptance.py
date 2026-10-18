"""Runner/verifier contracts; fake Go children are not Docs endpoint evidence."""
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('doc_acceptance', Path(__file__).with_name('document-create-acceptance.py'))
lane = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lane)


class DocumentCreationAcceptance(unittest.TestCase):
    def event(self, action='pass', **extra):
        return dict(Package=lane.PACKAGE, Test=lane.TEST, Action=action, **extra)

    def test_required_single_pass_and_all_negative_outcomes(self):
        cases = [([], 0), ([self.event('skip')], 0), ([self.event('fail')], 0),
                 ([self.event(), self.event('skip')], 0), ([self.event(), self.event('fail')], 0),
                 ([self.event(), self.event()], 0), ([self.event()], 7),
                 ([dict(self.event(), Package='wrong/package')], 0),
                 ([dict(self.event(), Test=lane.TEST + 'Renamed')], 0)]
        for events, status in cases:
            with self.subTest(events=events, status=status):
                evidence = lane.Evidence()
                for event in events:
                    evidence.accept(json.dumps(event).encode())
                self.assertFalse(evidence.passed(status))
        evidence = lane.Evidence()
        evidence.accept(json.dumps(self.event()).encode())
        self.assertTrue(evidence.passed(0))

    def test_malformed_json_is_not_evidence(self):
        for raw in [b'not JSON', b'[]', b'{}', b'null', b'\xff']:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                lane.Evidence().accept(raw)

    def child(self, directory, events, status=0):
        executable = directory / 'go'
        executable.write_text('#!/usr/bin/env python3\n'
                              'import json,os,pathlib,sys\n'
                              'root=pathlib.Path(os.environ["DOC_RUNNER_FIXTURE"])\n'
                              '(root/"call.json").write_text(json.dumps({"args":sys.argv[1:],"cwd":os.getcwd(),"opt_in":os.environ.get("CREWSHIP_DOCUMENT_CREATE")}))\n'
                              + 'sys.stdout.buffer.write(' + repr(events) + ')\n'
                              + 'sys.exit(' + str(status) + ')\n')
        executable.chmod(0o700)

    def test_actual_child_launch_and_streaming(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            raw = json.dumps(self.event()).encode() + b'\n'
            self.child(directory, raw)
            output = directory / 'evidence' / 'document-create.jsonl'
            stream = io.BytesIO()
            with patch.dict(os.environ, PATH=str(directory) + os.pathsep + os.environ['PATH'], DOC_RUNNER_FIXTURE=str(directory), CREWSHIP_DOCUMENT_CREATE='0'):
                self.assertEqual(lane.run(output, stream), 0)
            self.assertEqual(output.read_bytes(), raw)
            self.assertEqual(stream.getvalue(), raw)
            call = json.loads((directory / 'call.json').read_text())
            self.assertEqual(call['args'], ['test', '-json', '-tags', 'docs_foundation', './internal/api', '-run', '^' + lane.TEST + '$', '-count=1', '-timeout=5m'])
            self.assertEqual(call['cwd'], str(lane.ROOT))
            self.assertEqual(call['opt_in'], '1')

    def test_child_exit_and_oversized_line_fail_closed(self):
        for raw, status, expected in [(json.dumps(self.event()).encode() + b'\n', 7, 7), (b'x' * ((1 << 20) + 1), 0, 1), (b'not JSON\n', 0, 1), (b'', 0, 1)]:
            with self.subTest(status=status, size=len(raw)), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                self.child(directory, raw, status)
                stream = io.BytesIO()
                with patch.dict(os.environ, PATH=str(directory) + os.pathsep + os.environ['PATH'], DOC_RUNNER_FIXTURE=str(directory)):
                    self.assertEqual(lane.run(directory / 'events.jsonl', stream), expected)
                self.assertLessEqual(len(stream.getvalue()), 1 << 20)

    def test_existing_output_and_symlink_parent_are_rejected_before_launch(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            existing = directory / 'events.jsonl'
            existing.write_bytes(b'original')
            parent = directory / 'link'
            parent.symlink_to(directory, target_is_directory=True)
            for output in [existing, parent / 'new.jsonl']:
                with self.subTest(output=output), patch.object(lane.subprocess, 'Popen') as launch:
                    with self.assertRaises((OSError, ValueError)):
                        lane.run(output, io.BytesIO())
                    launch.assert_not_called()
            self.assertEqual(existing.read_bytes(), b'original')


if __name__ == '__main__':
    unittest.main()
