"""Negative controls execute the actual pre-start and saved-image parsers."""
import copy
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).parent


class BoundaryTest(unittest.TestCase):
    def test_prestart_denies_shared_or_privileged_configuration(self):
        valid = {
            "HostConfig": {"CgroupnsMode": "private", "PidMode": "", "Privileged": False,
                           "Binds": None, "Devices": [], "NetworkMode": "owned-network-id",
                           "PortBindings": {}},
            "Mounts": [{"Type": "volume", "Name": "owned-data", "Destination": "/var/lib/docker"}],
        }
        cases = [("valid", valid, 0)]
        for key, value in [("CgroupnsMode", "host"), ("PidMode", "host"), ("Privileged", True),
                           ("Binds", ["/root:/root"]), ("Devices", [{"PathOnHost": "/dev/sda"}]),
                           ("NetworkMode", "host"), ("PortBindings", {"22/tcp": []})]:
            changed = copy.deepcopy(valid)
            changed["HostConfig"][key] = value
            cases.append((key, changed, 1))
        changed = copy.deepcopy(valid)
        changed["Mounts"].append({"Type": "bind", "Source": "/sys/fs/cgroup"})
        cases.append(("extra mount", changed, 1))
        with tempfile.TemporaryDirectory(prefix="nerocd-bootstrap-boundary.", dir="/tmp") as work:
            path = Path(work) / "inspect.json"
            for name, configuration, outcome in cases:
                with self.subTest(name=name):
                    path.write_text(json.dumps([configuration]))
                    result = subprocess.run([sys.executable, "-B", str(ROOT / "check-isolation.py"),
                                             str(path), "owned-data"], capture_output=True, timeout=5)
                    self.assertEqual(result.returncode, outcome, result.stderr.decode())

    def test_sentinel_config_uses_native_content_hash_and_rejects_wrong_arch(self):
        config = json.dumps({"os": "linux", "architecture": "arm64"}).encode()
        expected = "sha256:" + hashlib.sha256(config).hexdigest()
        with tempfile.TemporaryDirectory(prefix="nerocd-bootstrap-archive.", dir="/tmp") as work:
            path = Path(work) / "sentinel.tar"
            with tarfile.open(path, "w") as archive:
                for name, data in [("manifest.json", b'[{"Config":"config.json"}]'), ("config.json", config)]:
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    archive.addfile(info, io.BytesIO(data))
            for arch, outcome in [("arm64", 0), ("amd64", 1)]:
                result = subprocess.run([sys.executable, "-B", str(ROOT / "sentinel-config.py"),
                                         str(path), arch], capture_output=True, timeout=5)
                self.assertEqual(result.returncode, outcome)
                if outcome == 0:
                    self.assertEqual(result.stdout.decode().strip(), expected)


if __name__ == "__main__":
    unittest.main()
