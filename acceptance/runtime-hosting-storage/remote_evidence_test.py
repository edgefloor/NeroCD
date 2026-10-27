import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import tarfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("remote_evidence", Path(__file__).with_name("remote_evidence.py"))
evidence = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evidence)


class ProfileTest(unittest.TestCase):
    def fixture(self):
        return {"HostConfig": {"PortBindings": {"18190/tcp": [{"HostIp": "127.0.0.1", "HostPort": ""}]}},
                "NetworkSettings": {"Ports": {"2375/tcp": None, "18190/tcp": [{"HostIp": "127.0.0.1", "HostPort": "49155"}]}}}

    def test_exact_loopback_assignment(self):
        self.assertEqual(evidence.publication(self.fixture()), "https://127.0.0.1:49155")

    def test_compose_publication_requires_exact_owner_and_fixed_app_port(self):
        value = self.fixture()
        value["Name"] = "/nerocd-compose-vikunja-runtime-20261027-dind"
        value["Config"] = {"Labels": {"io.nerocd.storage-runtime.owner": "nerocd-compose-vikunja-runtime-20261027"}}
        for section, key in [("HostConfig", "PortBindings"), ("NetworkSettings", "Ports")]:
            value[section][key]["443/tcp"] = [{"HostIp": "127.0.0.1", "HostPort": "25443"}]
        self.assertEqual(evidence.publication(value), "https://127.0.0.1:49155")
        for mutation in ["owner", "port", "address"]:
            wrong = copy.deepcopy(value)
            if mutation == "owner": wrong["Config"]["Labels"]["io.nerocd.storage-runtime.owner"] = "foreign"
            elif mutation == "port": wrong["NetworkSettings"]["Ports"]["443/tcp"][0]["HostPort"] = "25444"
            else: wrong["HostConfig"]["PortBindings"]["443/tcp"][0]["HostIp"] = "0.0.0.0"
            with self.subTest(mutation=mutation), self.assertRaises(ValueError): evidence.publication(wrong)

    def test_rejects_non_loopback_and_extra_publications(self):
        for section in ("HostConfig", "NetworkSettings"):
            key = "PortBindings" if section == "HostConfig" else "Ports"
            for ip in ("0.0.0.0", "::", "::1", "127.0.0.2"):
                value = self.fixture()
                value[section][key]["18190/tcp"][0]["HostIp"] = ip
                with self.subTest(section=section, ip=ip), self.assertRaises(ValueError):
                    evidence.publication(value)
            value = self.fixture()
            value[section][key]["9000/tcp"] = [{"HostIp": "127.0.0.1", "HostPort": "49000"}]
            with self.subTest(section=section, extra=True), self.assertRaises(ValueError):
                evidence.publication(value)

    def test_rejects_malformed_or_fixed_request(self):
        for port in ("0", "1023", "65536", "-1", "49155x", 49155):
            value = self.fixture()
            value["NetworkSettings"]["Ports"]["18190/tcp"][0]["HostPort"] = port
            with self.subTest(port=port), self.assertRaises(ValueError):
                evidence.publication(value)
        value = self.fixture()
        value["HostConfig"]["PortBindings"]["18190/tcp"][0]["HostPort"] = "49155"
        with self.assertRaises(ValueError):
            evidence.publication(value)

    def test_preservation_ignores_only_rotating_health_log(self):
        value = {"Id": "source", "State": {"Running": True, "Health": {"Status": "healthy", "Log": ["old"]}},
                 "Config": {"Env": ["protected=private"]}, "Mounts": []}
        new = copy.deepcopy(value)
        new["State"]["Health"]["Log"] = ["new"]
        self.assertEqual(evidence.selected(value), evidence.selected(new))
        for field in ("Running", "Health"):
            changed = copy.deepcopy(new)
            changed["State"][field] = False
            self.assertNotEqual(evidence.selected(value), evidence.selected(changed))

    def preservation_fixture(self):
        container = {"Id": "a" * 64, "Name": "/baseline", "Image": "image",
                     "Config": {"Env": ["protected=private"], "Labels": {}}, "HostConfig": {},
                     "Mounts": [], "State": {"Running": True}, "NetworkSettings": {"Network": "baseline"}}
        return {"containers": {container["Id"]: container}, "volumes": {"baseline-volume": {"Name": "baseline-volume", "Labels": {}}},
                "ui_files": {"binary": {"sha256": "original", "inode": 1}}, "ui_root": [1, 2, 3, 4, 5],
                "ui_listener": "original listener", "ui_health": {"/": {"status": 200, "sha256": "original body"}}}

    def preservation(self, before, after):
        return evidence.preservation(before, after, "task-owner", "task-owner-dind", "task-owner-docker-data")

    def test_reports_non_task_additions_without_changing_or_attributing_them(self):
        before = self.preservation_fixture()
        after = copy.deepcopy(before)
        added = copy.deepcopy(after["containers"]["a" * 64])
        added.update(Id="b" * 64, Name="/concurrent-addition")
        added["Config"]["Labels"] = {"unrelated-owner": "private"}
        after["containers"][added["Id"]] = added
        original = copy.deepcopy(after)
        result = self.preservation(before, after)
        self.assertEqual(result["status"], "passed")
        self.assertEqual(result["non_task_added_container_ids"], ["b" * 64])
        self.assertEqual(result["task_owned_or_task_volume_addition_ids"], [])
        self.assertNotIn("private", json.dumps(result))
        self.assertNotIn("concurrent-addition", json.dumps(result))
        self.assertEqual(after, original)

    def test_rejects_deleted_or_modified_baseline_container_profiles(self):
        before = self.preservation_fixture()
        for key in [None, "Name", "Image", "Config", "HostConfig", "Mounts", "State", "NetworkSettings"]:
            after = copy.deepcopy(before)
            if key is None:
                del after["containers"]["a" * 64]
            else:
                after["containers"]["a" * 64][key] = "modified"
            with self.subTest(key=key):
                self.assertEqual(self.preservation(before, after)["status"], "failed")

    def test_rejects_any_volume_inventory_or_profile_change(self):
        before = self.preservation_fixture()
        for kind in ["deleted", "added", "modified"]:
            after = copy.deepcopy(before)
            if kind == "deleted":
                after["volumes"].clear()
            elif kind == "added":
                after["volumes"]["unowned-new-volume"] = {"Name": "unowned-new-volume"}
            else:
                after["volumes"]["baseline-volume"]["Labels"] = {"changed": "true"}
            with self.subTest(kind=kind):
                self.assertEqual(self.preservation(before, after)["status"], "failed")

    def test_rejects_task_label_name_and_task_volume_users_among_additions(self):
        before = self.preservation_fixture()
        for kind in ["label", "name", "actual_volume", "requested_volume"]:
            after = copy.deepcopy(before)
            added = copy.deepcopy(after["containers"]["a" * 64])
            added.update(Id="b" * 64, Name="/addition")
            if kind == "label":
                added["Config"]["Labels"] = {"io.nerocd.storage-runtime.owner": "task-owner"}
            elif kind == "name":
                added["Name"] = "/task-owner-dind"
            elif kind == "actual_volume":
                added["Mounts"] = [{"Type": "volume", "Name": "task-owner-docker-data"}]
            else:
                added["HostConfig"]["Mounts"] = [{"Type": "volume", "Source": "task-owner-docker-data"}]
            after["containers"][added["Id"]] = added
            with self.subTest(kind=kind):
                result = self.preservation(before, after)
                self.assertEqual(result["status"], "failed")
                self.assertEqual(result["task_owned_or_task_volume_addition_ids"], ["b" * 64])
                self.assertEqual(result["non_task_added_container_ids"], [])

    def test_rejects_additions_without_sufficient_ownership_profile(self):
        before = self.preservation_fixture()
        for key, invalid in [("Name", None), ("Config", None), ("HostConfig", None), ("Mounts", None)]:
            after = copy.deepcopy(before)
            added = copy.deepcopy(after["containers"]["a" * 64])
            added.update(Id="b" * 64, Name="/addition")
            added[key] = invalid
            after["containers"][added["Id"]] = added
            with self.subTest(key=key):
                result = self.preservation(before, after)
                self.assertEqual(result["status"], "failed")
                self.assertEqual(result["unclassified_added_container_ids"], ["b" * 64])
                self.assertEqual(result["non_task_added_container_ids"], [])

    def test_after_action_writes_classification_and_retains_failed_baseline_evidence(self):
        before = self.preservation_fixture()
        for changed in [False, True]:
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                original = json.dumps(before)
                (root / "preservation-before.private.json").write_text(original)
                after = copy.deepcopy(before)
                if changed: after["containers"].clear()
                with patch.object(evidence, "snapshot", return_value=after), patch("sys.argv", ["remote_evidence.py", "after", directory, "task-owner", "task-owner-dind", "task-owner-docker-data"]):
                    if changed:
                        with self.assertRaises(ValueError): evidence.main()
                    else: evidence.main()
                self.assertEqual((root / "preservation-before.private.json").read_text(), original)
                self.assertTrue((root / "preservation-after.private.json").is_file())
                result = json.loads((root / "preservation-safe.json").read_text())
                self.assertEqual(result["status"], "failed" if changed else "passed")

    def test_rejects_ui_identity_listener_health_or_content_change(self):
        before = self.preservation_fixture()
        for key in ["ui_files", "ui_root", "ui_listener", "ui_health"]:
            after = copy.deepcopy(before)
            after[key] = {} if key != "ui_listener" else "changed listener"
            with self.subTest(key=key):
                self.assertEqual(self.preservation(before, after)["status"], "failed")
        for change in [{"status": 500, "sha256": "original body"}, {"status": 200, "sha256": "changed body"}]:
            after = copy.deepcopy(before)
            after["ui_health"]["/"] = change
            self.assertEqual(self.preservation(before, after)["status"], "failed")

    def test_receipt_contains_only_explicit_safe_members(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "runtime").mkdir()
            (root / "result").write_text("gate_exit=0\n")
            (root / "runtime/owned-upload.tar").write_bytes(b"fixture archive")
            (root / "runtime/deletion-markers.json").write_text("{}")
            (root / "preservation-before.private.json").write_text("private credentials")
            (root / "browser.log").write_text("private runtime log")
            (root / "unlisted-secret").write_text("private")
            with patch("sys.argv", ["remote_evidence.py", "receipt", directory]):
                evidence.main()
            with tarfile.open(root / "safe-receipt.tar.gz") as archive:
                self.assertEqual(set(archive.getnames()), {"result", "runtime/owned-upload.tar", "runtime/deletion-markers.json"})


if __name__ == "__main__":
    unittest.main()
