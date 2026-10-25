"""Qualify source/build identity and actual-test discovery, not product outcomes."""
import importlib.util
from importlib.machinery import ModuleSpec
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


fixtures = load("actual_fixture_tests", "mooring-actual-fixtures.py")
runner = load("actual_runner_tests", "mooring-actual.py")


class ActualRunnerTests(unittest.TestCase):
    def test_real_go_json_events_are_separate_from_stderr_diagnostics(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_text("module example.invalid/channels\n\ngo 1.27\n")
            source = root / "actual_test.go"
            source.write_text('package channels\nimport "testing"\nfunc TestActual(t *testing.T) {}\n')
            # A wrapper supplies the same stderr channel Go uses for cold
            # module downloads, then execs the real Go test process. Its test
            # run/pass events are produced by Go, not fabricated fixtures.
            diagnostic = "go: downloading example.invalid/fixture v1.0.0\n"
            wrapper = "import os,sys; sys.stderr.write(" + repr(diagnostic) + "); sys.stderr.flush(); os.execvp('go', ['go', 'test', '-json', '-count=1', './...'])"
            events = root / "go-test.jsonl"
            with patch.object(runner, "ROOT", root):
                code = runner.capture_go_test([sys.executable, "-c", wrapper], os.environ.copy(), events)
                self.assertEqual(code, 0)
                self.assertEqual(events.with_name("go-test.stderr.log").read_text(), diagnostic)
                lines = events.read_text().splitlines()
                self.assertNotIn(diagnostic.strip(), lines)
                self.assertEqual(runner.results.verify(lines, "example.invalid/channels", ["TestActual"])["executed"], ["TestActual"])
                with self.assertRaises(ValueError):
                    runner.results.verify([diagnostic, *lines], "example.invalid/channels", ["TestActual"])
                source.write_text(source.read_text().replace("{}", "{ undefinedFixture() }"))
                code = runner.capture_go_test([sys.executable, "-c", wrapper], os.environ.copy(), events)
                self.assertNotEqual(code, 0)
                self.assertIn(diagnostic.strip(), events.with_name("go-test.stderr.log").read_text())
                with self.assertRaises(ValueError):
                    runner.results.verify(events.read_text().splitlines(), "example.invalid/channels", ["TestActual"])

    def test_each_case_gets_verified_d3_pins_without_changing_primary_fixture(self):
        # Exercise main's fixture verification and environment construction.
        # Discovery, source identity, process execution and result verification
        # are controlled fixtures; this does not execute native integration.
        class IdentityLoader:
            def create_module(self, spec):
                return None

            def exec_module(self, module):
                module.source_identity = lambda: {"commit": "a" * 40, "tree": "b" * 40}

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def pin(name):
                path = root / name
                path.write_bytes(name.encode())
                return {"path": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}

            js = pin("mooring_wordcraft_client.js")
            wasm = pin("mooring_wordcraft_client_bg.wasm")
            fixture = {
                "format": "mooring.actual-fixtures.v1", "source": "a" * 40, "source_tree": "b" * 40,
                "binary": pin("session"),
                "sdk": {"directory": str(root), "assets": {
                    "mooring_wordcraft_client.js": js["sha256"],
                    "mooring_wordcraft_client_bg.wasm": wasm["sha256"]}},
                "checkpoints": {"d1": pin("d1"), "d3": pin("d3")},
                "payload": pin("payload"), "reads": pin("reads"),
            }
            manifest = root / "fixtures.json"
            manifest.write_text(json.dumps(fixture))
            seen = []

            def capture(command, env, events):
                expected = "d3" if command[command.index("-run") + 1] == "^" + runner.READONLY + "$" else "d1"
                self.assertEqual(env["MOORING_SESSION_CHECKPOINT"], fixture["checkpoints"][expected]["path"])
                self.assertEqual(env["MOORING_D3_CHECKPOINT"], fixture["checkpoints"]["d3"]["path"])
                self.assertEqual(env["MOORING_D3_CHECKPOINT_SHA256"], fixture["checkpoints"]["d3"]["sha256"])
                self.assertNotIn("MOORING_PRIVATE_FIXTURE", env)
                events.write_text("")
                seen.append(expected)
                return 0

            declared = {"TestAgentD1": "internal/api/agent_test.go", runner.READONLY: "internal/api/readonly_test.go"}
            with patch.object(runner, "cases", return_value=declared), \
                    patch.object(runner.importlib.util, "spec_from_file_location", return_value=ModuleSpec("actual_fixtures", IdentityLoader())), \
                    patch.object(runner, "capture_go_test", side_effect=capture), \
                    patch.object(runner.results, "verify", return_value={"executed": []}), \
                    patch.object(sys, "argv", ["runner", "--fixtures", str(manifest), "--results", str(root / "results")]), \
                    patch.dict(os.environ, {"MOORING_D3_CHECKPOINT": "/untrusted", "MOORING_D3_CHECKPOINT_SHA256": "0" * 64, "MOORING_PRIVATE_FIXTURE": "stale"}):
                runner.main()
            self.assertEqual(seen, ["d1", "d3"])

    def test_relative_cargo_target_uses_build_root_not_callers_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch.object(fixtures, "ROOT", root), patch.dict(os.environ, {"CARGO_TARGET_DIR": "local-target"}):
                self.assertEqual(fixtures.cargo_target(), root / "local-target")
            with patch.object(fixtures, "ROOT", root), patch.dict(os.environ, {"CARGO_TARGET_DIR": str(root / "absolute-target")}):
                self.assertEqual(fixtures.cargo_target(), root / "absolute-target")

    def test_source_identity_rejects_actual_dirty_or_staged_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", "-C", directory, *args], text=True).strip()
            git("init", "-q")
            git("config", "user.name", "Actual fixture test")
            git("config", "user.email", "fixture@example.invalid")
            source = root / "source.txt"
            source.write_text("committed\n")
            git("add", "source.txt")
            git("commit", "-qm", "fixture")
            with patch.object(fixtures, "ROOT", root):
                clean = fixtures.source_identity()
                self.assertEqual(clean, {"commit": git("rev-parse", "HEAD"), "tree": git("rev-parse", "HEAD^{tree}")})
                source.write_text("uncommitted\n")
                with self.assertRaisesRegex(ValueError, "clean tracked source"):
                    fixtures.source_identity()
                git("add", "source.txt")
                with self.assertRaisesRegex(ValueError, "clean tracked source"):
                    fixtures.source_identity()

    def test_discovery_covers_long_headers_and_legal_parameter_names(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            test = root / "internal/example/actual_test.go"
            test.parent.mkdir(parents=True)
            header = "// Copyright fixture\n" * 9 + '//go:build linux && mooring_rust_integration\n\npackage example\nimport "testing"\n'
            inventory = {"format": "mooring.actual-tests.v1", "files": [{"path": "internal/example/actual_test.go", "tests": ["TestActual"]}]}
            with patch.object(runner, "ROOT", root), patch.object(runner, "inventory_data", return_value=inventory):
                for parameter in ["tb *testing.T", "other *testing.T", "*testing.T", "\n tb *testing.T,\n"]:
                    with self.subTest(parameter=parameter):
                        test.write_text(header + "func TestActual(" + parameter + ") {}\n")
                        self.assertEqual(runner.cases(), {"TestActual": "internal/example/actual_test.go"})
                test.write_text(header + "func TestActual(tb *testing.T) {}\nfunc TestNew(other *testing.T) {}\n")
                with self.assertRaisesRegex(ValueError, "inventory drift"):
                    runner.cases()
                test.write_text(header + "func TestActual(tb *alias.T) {}\n")
                with self.assertRaisesRegex(ValueError, "unsupported tagged test declaration"):
                    runner.cases()

    def test_compiler_visible_r1_declarations_cannot_evade_inventory(self):
        variants = {
            "indented": ("", "//go:build linux && mooring_rust_integration", "  func TestOmitted(tb *testing.T) {}\n"),
            "header_block": ("/*\npackage fake\n*/\n", "//go:build linux && mooring_rust_integration", "func TestOmitted(tb *testing.T) {}\n"),
            "declaration_comment": ("", "//go:build linux && mooring_rust_integration", "func /* legal comment */ TestOmitted(tb *testing.T) {}\n"),
            "legacy_build": ("", "// +build linux,mooring_rust_integration", "func TestOmitted(tb *testing.T) {}\n"),
        }
        for shape, (prefix, tag, declaration) in variants.items():
            with self.subTest(shape=shape), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "go.mod").write_text("module example.invalid/discovery\n\ngo 1.27\n")
                test = root / "internal/example/actual_test.go"
                test.parent.mkdir(parents=True)
                test.write_text(prefix + tag + '\n\npackage example\nimport "testing"\n'
                                + "func TestActual(t *testing.T) {}\n" + declaration)
                listed = subprocess.check_output(["go", "test", "-tags", "mooring_rust_integration",
                                                  "-list", "^Test", "./internal/example"], cwd=root, text=True)
                self.assertIn("TestActual\n", listed)
                self.assertIn("TestOmitted\n", listed)
                inventory = {"format": "mooring.actual-tests.v1", "files": [{
                    "path": "internal/example/actual_test.go", "tests": ["TestActual"]}]}
                with patch.object(runner, "ROOT", root), patch.object(runner, "inventory_data", return_value=inventory):
                    with self.assertRaisesRegex(ValueError, "inventory drift"):
                        runner.cases()
                    inventory["files"][0]["tests"].append("TestOmitted")
                    self.assertEqual(set(runner.cases()), {"TestActual", "TestOmitted"})

    def test_inventory_rejects_missing_stale_duplicate_and_relocated_tests(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            test = root / "internal/example/actual_test.go"
            test.parent.mkdir(parents=True)
            test.write_text('//go:build linux && mooring_rust_integration\n\npackage example\nimport "testing"\nfunc TestActual(t *testing.T) {}\n')
            inventory = {"format": "mooring.actual-tests.v1", "files": [{"path": "internal/example/actual_test.go", "tests": ["TestActual"]}]}
            with patch.object(runner, "ROOT", root), patch.object(runner, "inventory_data", return_value=inventory):
                self.assertEqual(runner.cases(), {"TestActual": "internal/example/actual_test.go"})
                test.write_text(test.read_text() + "func TestMissing(t *testing.T) {}\n")
                with self.assertRaisesRegex(ValueError, "inventory drift"):
                    runner.cases()
                test.write_text(test.read_text().replace("TestMissing", "TestActual"))
                with self.assertRaisesRegex(ValueError, "duplicate source"):
                    runner.cases()
                test.write_text('//go:build linux && mooring_rust_integration\n\npackage example\nimport "testing"\nfunc TestStale(t *testing.T) {}\n')
                with self.assertRaisesRegex(ValueError, "inventory drift"):
                    runner.cases()
                test.write_text('//go:build linux && mooring_rust_integration\n\npackage example\nimport "testing"\nfunc TestActual(t *testing.T) {}\n')
                test.rename(test.with_name("moved_test.go"))
                with self.assertRaisesRegex(ValueError, "inventory drift"):
                    runner.cases()


if __name__ == "__main__":
    unittest.main()
