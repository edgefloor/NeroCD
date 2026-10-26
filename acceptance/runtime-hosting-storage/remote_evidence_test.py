import copy
import importlib.util
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

    def test_receipt_contains_only_explicit_safe_members(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "runtime").mkdir()
            (root / "result").write_text("gate_exit=0\n")
            (root / "runtime/owned-upload.tar").write_bytes(b"fixture archive")
            (root / "preservation-before.private.json").write_text("private credentials")
            (root / "browser.log").write_text("private runtime log")
            (root / "unlisted-secret").write_text("private")
            with patch("sys.argv", ["remote_evidence.py", "receipt", directory]):
                evidence.main()
            with tarfile.open(root / "safe-receipt.tar.gz") as archive:
                self.assertEqual(set(archive.getnames()), {"result", "runtime/owned-upload.tar"})


if __name__ == "__main__":
    unittest.main()
