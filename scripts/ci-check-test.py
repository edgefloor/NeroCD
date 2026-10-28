#!/usr/bin/env python3
"""Coverage and failure propagation for the parallel CI runner."""

import importlib.util
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("ci_check", Path(__file__).with_name("ci-check.py"))
CI = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CI)


class CICheckTest(unittest.TestCase):
    def test_every_test_example_and_fuzz_seed_has_one_shard(self):
        names = [f"TestCase{i}" for i in range(41)] + ["Example", "ExampleClient_Open", "FuzzDecode"]
        inventory = CI.runtime_tests("\n".join(reversed(names)) + "\nBenchmarkIgnored\nok\tnerocd/internal/runnerruntime\t0.1s\n")
        shards = [CI.shard_tests(inventory, i) for i in range(CI.SHARDS)]
        self.assertEqual(sorted(name for shard in shards for name in shard), sorted(names))
        self.assertLessEqual(max(map(len, shards)) - min(map(len, shards)), 1)
        for shard in shards:
            pattern = "^(?:" + "|".join(re.escape(name) for name in shard) + ")$"
            self.assertEqual([name for name in inventory if re.fullmatch(pattern, name)], shard)

    def test_invalid_inventories_and_shards_fail(self):
        for output in ["", "ok\tpackage\n", "TestSame\nTestSame\n"]:
            with self.assertRaises(ValueError):
                CI.runtime_tests(output)
        for names, index in [([], 0), (["TestOne"], 1), (["TestOne"], -1), (["TestOne"], CI.SHARDS)]:
            with self.assertRaises(ValueError):
                CI.shard_tests(names, index)

    def test_non_runtime_race_lane_covers_every_other_product_package(self):
        packages = ["nerocd/cmd/nerocd", CI.RUNTIME, "nerocd/internal/auth"]
        with patch.object(CI, "run", return_value=subprocess.CompletedProcess([], 0, "\n".join(packages))) as run:
            CI.check_lane("race")
        self.assertEqual(run.call_args_list[-1].args[0],
                         ["go", "test", "-race", "-timeout=10m", packages[0], packages[2]])

    def test_make_check_gate_inventory_is_preserved(self):
        expected = {"ci-release-policy-gate", "verify", "web-test", "build", "browser-smoke",
                    "web-policy", "contract", "check-generated", "production-compose-policy-gate",
                    "local-image-registry-gate", "docker-build"}
        self.assertEqual(set(CI.GO_GATES + CI.WEB_GATES), expected)
        self.assertEqual(len(CI.GO_GATES + CI.WEB_GATES), len(expected))
        with patch.object(CI, "run") as run:
            CI.check_lane("go")
            CI.check_lane("web")
        self.assertEqual(run.call_args_list[0].args[0], ["make", *CI.GO_GATES])
        self.assertEqual(run.call_args_list[1].args[0], ["make", *CI.WEB_GATES])

    def test_command_failure_stops_lane(self):
        with patch.object(CI, "run", side_effect=subprocess.CalledProcessError(1, "go test")) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                CI.check_lane("runtime-0")
        self.assertEqual(run.call_count, 1)

    def test_workflow_rejects_missing_lanes_and_success_on_failure(self):
        source = (CI.ROOT / ".github/workflows/check.yml").read_text()
        CI.validate_workflow(CI.ROOT)
        for old, new in [
            ('"runtime-5", ', ""),
            ("fail-fast: false", "fail-fast: true"),
            ("needs: [verify]", "needs: []"),
            ("if: ${{ always() }}", "if: ${{ success() }}"),
            ('run: test "$VERIFY_RESULT" = success', "run: true"),
            ("  verify:\n", "  verify:\n    continue-on-error: true\n"),
            ("  verify:\n", "  verify:\n    if: false\n"),
            ("if: matrix.lane == 'go' || matrix.lane == 'race' || matrix.lane == 'web'", "if: matrix.lane == 'web'"),
            ("      - name: Run checks\n", "      - name: Run checks\n        if: false\n"),
            ("      - name: Require every verification lane\n", "      - name: Require every verification lane\n        if: false\n"),
            ("run: make postgres-test", "run: true"),
            ("run: make release-artifacts", "run: true"),
        ]:
            with self.subTest(mutation=old), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                path = root / ".github/workflows/check.yml"
                path.parent.mkdir(parents=True)
                path.write_text(source.replace(old, new))
                with self.assertRaises(ValueError):
                    CI.validate_workflow(root)


if __name__ == "__main__":
    unittest.main()
