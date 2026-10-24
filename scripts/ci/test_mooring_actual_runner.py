"""Qualify source/build identity and actual-test discovery, not product outcomes."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
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
