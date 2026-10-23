"""Resolve the native config ID from the pinned Docker save archive."""
import hashlib
import json
import sys
import tarfile

with tarfile.open(sys.argv[1]) as archive:
    manifest = json.load(archive.extractfile("manifest.json"))
    if len(manifest) != 1:
        raise ValueError("exactly one native sentinel image required")
    config = archive.extractfile(manifest[0]["Config"]).read()
    metadata = json.loads(config)
    if metadata["os"] != "linux" or metadata["architecture"] != sys.argv[2]:
        raise ValueError("sentinel archive architecture mismatch")
    print("sha256:" + hashlib.sha256(config).hexdigest())
