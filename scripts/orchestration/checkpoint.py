#!/usr/bin/env python3
"""Manually advance an external, single-writer orchestration checkpoint."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from orchestration.protocol import ProtocolError, advance_checkpoint


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo-root", type=Path, default=Path.cwd())
    parser.add_argument("--state", required=True, type=Path)
    parser.add_argument("--stage", required=True, choices=("candidate_validated", "reviewed", "accepted"))
    parser.add_argument("--packet", required=True, type=Path)
    parser.add_argument("--result", required=True, type=Path)
    parser.add_argument("--review", action="append", default=[], type=Path)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    try:
        checkpoint = advance_checkpoint(
            args.state, args.stage, args.packet, args.result, args.review, args.repo_root,
        )
    except ProtocolError as exc:
        payload = {"ok": False, "error": str(exc)}
        print(json.dumps(payload) if args.json else f"checkpoint unchanged: {exc}")
        return 1
    payload = {"ok": True, "checkpoint": checkpoint}
    print(json.dumps(payload) if args.json else f"checkpoint advanced: {args.stage}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
