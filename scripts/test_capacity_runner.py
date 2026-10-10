"""Acceptance evidence must describe one exact binary/workload, never a guess."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import start_soak
import run_capacity


class CapacityRunnerTest(unittest.TestCase):
    def record(self):
        return {"harness": "mixed", "container": "fixture-container-id", "name": "goomni-soak-fixture", "image_id": "sha256:fixture-image",
                "container_started_at": "synthetic-start", "duration": "1h", "warmup": "10m", "synthetic_accounts": 300,
                "event_rate": 60, "send_rate": 10, "seed": 1, "architecture": "arm64", "source_sha256": "fixture-source", "binary_sha256": "fixture-binary", "queue_limit": 1000, "connection_queue_limit": 100}

    def result(self):
        return {"passed": True, "measurement_scope": "synthetic provider, real REST/storage/webhook; process includes bounded harness", "accounts": 300,
                "event_rate": 60, "send_rate": 10, "seed": 1, "duration_seconds": 3600, "warmup_seconds": 600, "measured_disk_growth_bytes": 1024**3, "queue_limit": 1000, "connection_queue_limit": 100}

    def test_disk_budget_and_unusable_growth_evidence(self):
        self.assertEqual(start_soak.required_disk_bytes(self.result(), 86400), 38 * start_soak.GIB)
        for growth in (-1, float("nan"), float("inf"), True, "100"):
            with self.subTest(growth=growth), self.assertRaises(ValueError):
                start_soak.required_disk_bytes(dict(self.result(), measured_disk_growth_bytes=growth), 86400)
        with self.assertRaises(ValueError):
            start_soak.required_disk_bytes(dict(self.result(), duration_seconds=3599), 86400)

    def test_evidence_requires_same_frozen_binary_image_and_workload(self):
        expected = self.record()
        evidence = dict(expected, status="passed", result=self.result())
        start_soak.validate_evidence(evidence, expected, 86400, 38 * start_soak.GIB)
        for key in ("binary_sha256", "source_sha256", "image_id", "architecture", "seed", "event_rate", "send_rate", "synthetic_accounts", "queue_limit", "connection_queue_limit"):
            bad = dict(evidence, **{key: "changed"})
            with self.subTest(key=key), self.assertRaises(ValueError):
                start_soak.validate_evidence(bad, expected, 86400, 100 * start_soak.GIB)
        with self.assertRaises(ValueError):
            start_soak.validate_evidence(evidence, expected, 86400, 37 * start_soak.GIB)
        with self.assertRaises(ValueError):
            start_soak.validate_evidence(dict(evidence, result="invalid"), expected, 86400, 100 * start_soak.GIB)

    def test_frozen_binary_rejects_each_mismatch(self):
        prior = self.record()
        args = [prior["source_sha256"], prior["architecture"], prior["image_id"], prior["binary_sha256"]]
        start_soak.validate_frozen(prior, *args)
        for index in range(len(args)):
            changed = args.copy()
            changed[index] = "changed"
            with self.subTest(index=index), self.assertRaises(ValueError):
                start_soak.validate_frozen(prior, *changed)

    def test_pass_requires_boolean_and_exact_result_scope(self):
        state = {"ExitCode": 0, "OOMKilled": False}
        self.assertTrue(start_soak.valid_result(self.record(), state, self.result()))
        for field, value in (("passed", "true"), ("passed", 1), ("accounts", 50), ("duration_seconds", 10), ("seed", 2), ("warmup_seconds", 0), ("measurement_scope", "unknown"), ("queue_limit", 999), ("connection_queue_limit", 0), ("connection_queue_limit", None)):
            with self.subTest(field=field):
                self.assertFalse(start_soak.valid_result(self.record(), state, dict(self.result(), **{field: value})))
        self.assertFalse(start_soak.valid_result(self.record(), dict(state, OOMKilled=True), self.result()))
        self.assertFalse(start_soak.valid_result(self.record(), dict(state, ExitCode=1), self.result()))

    def test_mixed_provider_evidence_cannot_be_replaced_by_bale_only(self):
        providers = ["bale", "eitaa", "rubika"]
        record = dict(self.record(), providers=providers)
        result = dict(self.result(), providers=providers)
        state = {"ExitCode": 0}
        self.assertTrue(start_soak.valid_result(record, state, result))
        self.assertFalse(start_soak.valid_result(record, state, dict(result, providers=["bale"])))
        evidence = dict(record, status="passed", result=result)
        with self.assertRaises(ValueError):
            start_soak.validate_evidence(dict(evidence, providers=["bale"]), record, 86400, 100 * start_soak.GIB)

    def test_source_hash_includes_embedded_assets_and_runner(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            assets = root / "src/ui/web/dist"
            assets.mkdir(parents=True)
            (root / "src/domains").mkdir()
            (root / "scripts").mkdir()
            files = [assets / "app.js", root / "src/domains/operation_catalog.json", root / "scripts/start_soak.py"]
            for path in files:
                before = start_soak.source_hash(root)
                path.write_text("synthetic source input")
                self.assertNotEqual(before, start_soak.source_hash(root), str(path))

    def test_collect_uses_immutable_container_and_fails_malformed_result(self):
        for malformed in (False, True):
            with self.subTest(malformed=malformed), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                binary = root / "capacity.test"
                binary.write_bytes(b"synthetic executable")
                record = dict(self.record(), status="running", binary_sha256=start_soak.file_hash(binary))
                path = root / "run.json"
                start_soak.save_record(path, record)
                inspected = {"Id": record["container"], "Image": record["image_id"], "RestartCount": 0,
                             "State": {"Running": False, "ExitCode": 0, "OOMKilled": False, "StartedAt": record["container_started_at"], "FinishedAt": "synthetic-finish"}}
                def run(command, **kwargs):
                    self.assertIn(record["container"], " ".join(command))
                    self.assertNotIn(record["name"], command)
                    if command[1] == "cp":
                        (root / "result.json").write_text("broken JSON" if malformed else json.dumps(self.result()))
                    return subprocess.CompletedProcess(command, 0, stdout="fixture logs", stderr="")
                with patch.object(start_soak, "docker", return_value=json.dumps([inspected])) as docker, patch.object(start_soak.subprocess, "run", side_effect=run):
                    result = start_soak.collect(path)
                docker.assert_called_once_with("inspect", record["container"])
                self.assertEqual("failed" if malformed else "passed", result["status"])
                self.assertEqual(result["status"], json.loads(path.read_text())["status"])

    def test_collect_rejects_replaced_or_restarted_container(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "run.json"
            record = self.record()
            inspected = {"Id": record["container"], "Image": record["image_id"], "RestartCount": 0,
                         "State": {"Running": True, "StartedAt": record["container_started_at"]}}
            for key, value in (("Id", "different"), ("Image", "different"), ("RestartCount", 1)):
                changed = copy.deepcopy(inspected)
                changed[key] = value
                start_soak.save_record(path, record)
                with patch.object(start_soak, "docker", return_value=json.dumps([changed])):
                    self.assertEqual("failed", start_soak.collect(path)["status"])

    def test_disk_block_is_not_a_capacity_failure_and_never_overwrites_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = SimpleNamespace(name="goomni-soak-blocked", native=False, max_disk_gib=1)
            with self.assertRaises(SystemExit) as exited:
                start_soak.blocked_run(root, args, "synthetic insufficient volume space")
            self.assertEqual(3, exited.exception.code)
            path = root / "artifacts/soak/goomni-soak-blocked/run.json"
            before = path.read_bytes()
            with patch.object(start_soak, "docker") as docker:
                self.assertEqual("blocked_insufficient_space", start_soak.collect(path)["status"])
                docker.assert_not_called()
            with self.assertRaises(SystemExit):
                start_soak.blocked_run(root, args, "new reason")
            self.assertEqual(before, path.read_bytes())

    def test_invalid_resume_keeps_blocked_manifest_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence = root / "native.json"
            evidence.write_text(json.dumps({"status": "passed", "source_sha256": "different-source", "image_id": "fixture-image"}))
            manifest = root / "acceptance.json"
            record = {"status": "blocked_insufficient_space", "short_stage_disk_gib": 1, "soak_requested": True,
                      "source_sha256": "fixture-source", "image_id": "fixture-image",
                      "phases": [{"phase": "native300", "run": str(evidence), "status": "passed"},
                                 {"phase": "smoke", "status": "blocked_insufficient_space"}]}
            manifest.write_text(json.dumps(record))
            before = manifest.read_bytes()
            with patch.object(run_capacity.sys, "argv", ["run_capacity.py", "--resume", str(manifest)]), patch("sys.stderr"), self.assertRaises(SystemExit):
                run_capacity.main()
            self.assertEqual(before, manifest.read_bytes())

    def test_collection_preserves_interrupted_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "run.json"
            for status in ("interrupted_source_revision", "interrupted_resource_profile"):
                start_soak.save_record(path, dict(self.record(), status=status))
                before = path.read_bytes()
                with patch.object(start_soak, "docker") as docker:
                    self.assertEqual(status, start_soak.collect(path)["status"])
                    docker.assert_not_called()
                self.assertEqual(before, path.read_bytes())


if __name__ == "__main__":
    unittest.main()
