#!/usr/bin/env python3
"""Validate and render the pinned hosting checklist using only the standard library."""

import argparse
import collections
import functools
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
BASE = "563fb88f707b75563d03f0408df0267e3af473e9"
UPSTREAM = "b12d1386f3d458618b4c579babcd1e339c6c40b3"
BASELINE_CRITERIA = 984
BASELINE_MANIFEST = "ce2ea4e8add8e43d6b10bc24662da8a0d5bbaee1f1b7cbc694c242dbee644f9e"
COUNTS = {"requirement": 68, "acceptance": 37, "area": 19, "database": 9,
          "source": 10, "secret": 3, "builder": 7, "proxy": 4, "notification": 6}
STATES = {"missing", "partial", "implemented", "verified", "evidence_unavailable", "failed"}


@functools.lru_cache(maxsize=None)
def git(*args):
    return subprocess.check_output(["/usr/bin/git", "-C", str(ROOT), *args], text=True).strip()


@functools.lru_cache(maxsize=None)
def source_lines(sha, path):
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError(f"invalid full SHA: {sha}")
    if path.startswith("/") or ".." in Path(path).parts:
        raise ValueError(f"invalid source path: {path}")
    return git("show", f"{sha}:{path}").splitlines()


def inventory():
    """Read original parents, including exact complete text and line ranges."""
    result = []
    spec = source_lines(BASE, "docs/product-spec.md")
    for index, line in enumerate(spec):
        match = re.match(r"R(\d+)\. ", line)
        if match and int(match[1]) <= 68:
            result.append(dict(id=f"R{int(match[1]):02}", kind="requirement", text=line,
                               path="docs/product-spec.md", start=index + 1, end=index + 1))
        match = re.match(r"\|A(\d+)\|", line)
        if match and int(match[1]) <= 37:
            result.append(dict(id=f"A{int(match[1]):02}", kind="acceptance", text=line,
                               path="docs/product-spec.md", start=index + 1, end=index + 1))
    headings = {"Product areas": "area", "Database engines": "database",
                "Sources and providers": "source", "Builders and execution platforms": "builder",
                "Proxy adapters": "proxy", "Notification channels": "notification"}
    kind = None
    numbers = collections.Counter()
    prefixes = dict(area="AREA", database="DB", source="SRC", secret="SEC", builder="BUILD",
                    proxy="PROXY", notification="NOTIFY")
    for index, line in enumerate(source_lines(BASE, "docs/compatibility.md")):
        if line.startswith("## "):
            kind = headings.get(line[3:])
        if line.startswith("| External secret provider |"):
            kind = "secret"
        if not kind or not line.startswith("|"):
            continue
        first = line.split("|")[1].strip()
        if not first or first.startswith("-") or first in {
            "Product area", "Engine", "Provider or input", "External secret provider", "Strategy", "Adapter", "Channel"
        }:
            continue
        numbers[kind] += 1
        result.append(dict(id=f"{prefixes[kind]}{numbers[kind]:02}", kind=kind, text=line,
                           path="docs/compatibility.md", start=index + 1, end=index + 1))
    return result


def validate(data):
    def require(condition, message):
        if not condition:
            raise ValueError(message)

    require(data["schema_version"] == 1 and data["scoring_version"] == "hosting-atomic-v1", "unsupported schema/scoring version")
    require(data["audit_sha"] == BASE and data["coolify_sha"] == UPSTREAM, "wrong audit/reference SHA")
    require(git("rev-parse", BASE + "^{commit}") == BASE, "audit commit unavailable")
    parents = data["parents"]
    criteria = data["criteria"]
    parent_map = {p["id"]: p for p in parents}
    criterion_map = {c["id"]: c for c in criteria}
    require(len(parent_map) == len(parents), "duplicate parent ID")
    require(len(criterion_map) == len(criteria), "duplicate criterion ID")
    expected = inventory()
    require(set(parent_map) == {p["id"] for p in expected}, "missing or extra original scope item")
    require(dict(collections.Counter(p["kind"] for p in parents)) == COUNTS, "incorrect 68/37/58 inventory")
    for original in expected:
        parent = parent_map[original["id"]]
        require(all(parent[k] == value for k, value in original.items()), f"altered original source: {parent['id']}")
        require(parent["criteria"] and len(parent["criteria"]) == len(set(parent["criteria"])), f"empty/duplicate links: {parent['id']}")
        require(set(parent["criteria"]) <= criterion_map.keys(), f"dangling criterion: {parent['id']}")
        require(parent["state"] in STATES and parent["gap"], f"missing state/gap: {parent['id']}")
        require(set(parent["gates"]) <= parent_map.keys(), f"dangling gate: {parent['id']}")
        require(all(parent_map[g]["kind"] != "requirement" for g in parent["gates"]), "gate must be A/matrix")
        for duplicate in parent.get("duplicate_sources", []):
            lines = source_lines(duplicate["sha"], duplicate["path"])
            require(duplicate["sha"] == BASE and "\n".join(lines[duplicate["start"] - 1:duplicate["end"]]) == duplicate["text"], "altered duplicate table source")
        if parent["kind"] == "requirement":
            clauses = parent["clause_review"]
            require(" ".join(c["text"] for c in clauses) == parent["text"], f"missing original clause: {parent['id']}")
            require(all(c["criteria"] and set(c["criteria"]) <= criterion_map.keys() for c in clauses), "missing clause links")
    manifest = [[c["id"], c["requirement"], c["outcome"], c["source"]] for c in criteria]
    digest = hashlib.sha256(json.dumps(manifest, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
    require(digest == data["baseline_manifest_sha256"], "criterion baseline changed without versioned manifest history")
    require(digest == BASELINE_MANIFEST and len(criteria) == BASELINE_CRITERIA, "changed frozen baseline definition/denominator")
    require(data["criterion_history"] == [{"version": "hosting-atomic-v1", "added": [c["id"] for c in criteria], "removed": []}], "invalid baseline criterion history")
    require(data["baseline_criterion_count"] == len(criteria), "incorrect frozen criterion total")
    covered = collections.Counter()
    for criterion in criteria:
        cid = criterion["id"]
        owner = criterion["requirement"]
        require(owner in parent_map and parent_map[owner]["kind"] == "requirement", f"invalid owner: {cid}")
        require(cid in parent_map[owner]["criteria"], f"unlinked owned criterion: {cid}")
        require(re.fullmatch(owner + r"-C\d{2,}", cid) is not None, f"invalid criterion ID: {cid}")
        require(criterion["state"] in STATES, f"invalid state: {cid}")
        require(criterion["presence"] in {"missing", "partial", "implemented"}, f"invalid presence: {cid}")
        require(criterion["outcome"] and criterion["next_acceptance"] and criterion["limits"], f"missing criterion details: {cid}")
        require(criterion["presence"] == "missing" or criterion["evidence"], f"implementation presence without source evidence: {cid}")
        require(criterion["source"] == {k: parent_map[owner][k] for k in ("path", "start", "end", "text")}, f"invalid requirement source: {cid}")
        covered[owner] += 1
        for evidence in criterion["evidence"]:
            lines = source_lines(evidence["subject_sha"], evidence["path"])
            require(1 <= evidence["start"] <= evidence["end"] <= len(lines), f"bad evidence range: {cid}")
            require(evidence["relevance"] and evidence["limits"], f"missing relevance/limits: {cid}")
        if criterion["state"] == "verified":
            require(criterion["presence"] == "implemented" and criterion["evidence"], f"verified without implementation/source: {cid}")
            verification = criterion["verification"]
            for key in ("test", "command", "subject_sha", "profile", "limits", "artifact", "artifact_sha256", "relevance", "dependency_paths"):
                require(verification.get(key), f"verified without {key}: {cid}")
            require(verification["result"] == "passed", f"non-passing verification: {cid}")
            require(re.fullmatch(r"[0-9a-f]{40}", verification["subject_sha"]) is not None, f"bad verification SHA: {cid}")
            require(any(e["subject_sha"] == verification["subject_sha"] for e in criterion["evidence"]), f"unbound verification subject: {cid}")
            artifact = verification["artifact"]
            require(isinstance(artifact, dict) and artifact.get("exit_code") == 0, f"verification artifact unavailable/nonpassing: {cid}")
            require(hashlib.sha256(json.dumps(artifact, sort_keys=True).encode()).hexdigest() == verification["artifact_sha256"], f"verification artifact changed: {cid}")
            events = [json.loads(line) for line in artifact["stdout"].splitlines() if line]
            require(any(e.get("Action") == "pass" and e.get("Test") == verification["test"] for e in events), f"named test did not pass: {cid}")
            require(not any(e.get("Action") in {"skip", "fail"} for e in events), f"skipped/failed verification: {cid}")
            for path in verification["dependency_paths"]:
                require(git("rev-parse", f"{verification['subject_sha']}:{path}") == git("rev-parse", f"{BASE}:{path}"), f"affected historical dependency: {cid}: {path}")
            test_sources = ["\n".join(source_lines(e["subject_sha"], e["path"])) for e in criterion["evidence"]]
            require(any(verification["test"] in content for content in test_sources), f"named test absent: {cid}")
        else:
            require(criterion["verification"] is None, f"unverified criterion carries pass: {cid}")
    require(set(covered) == {f"R{i:02}" for i in range(1, 69)}, "requirement without atomic criteria")
    for obligation in data["scope_obligations"]:
        lines = source_lines(obligation["sha"], obligation["path"])
        require(obligation["sha"] == BASE, "wrong scope obligation SHA")
        require("\n".join(lines[obligation["start"] - 1:obligation["end"]]) == obligation["text"], "altered supplementary mandatory source")
        require(obligation["criteria"] and set(obligation["criteria"]) <= criterion_map.keys(), "missing supplementary scope links")
    require({o["id"] for o in data["scope_obligations"]} == {"engine-common", "builder-common", "compose-modes", "proxy-common", "notification-common", "client-contract", "migration-common", "imported-R81", "imported-R82", "imported-R85", "imported-R87", "imported-R88", "imported-R89", "imported-R90", "imported-R91", "imported-R92"}, "missing supplementary mandatory scope")
    for parent in parents:
        if parent["kind"] != "requirement" and parent["state"] == "verified":
            require(all(criterion_map[c]["state"] == "verified" for c in parent["criteria"]), f"gate verified with gaps: {parent['id']}")
            require(parent.get("gate_verification"), f"gate lacks combined acceptance receipt: {parent['id']}")
    require(data["unresolved_client_contract"]["state"] == "missing", "client endpoint contract cannot pass without inventory")
    return parent_map, criterion_map


def render(data, parents, criteria):
    rows = data["criteria"]
    verified = sum(c["state"] == "verified" for c in rows)
    present = collections.Counter(c["presence"] for c in rows)
    complete = lambda p: all(criteria[c]["state"] == "verified" for c in p["criteria"]) and all(parents[g]["state"] == "verified" for g in p["gates"])
    lines = ["# Hosting parity audit", "", "<!-- Generated by scripts/parity_audit.py. Edit docs/parity-checklist.json, then regenerate. -->", "",
             f"Audited NeroCD commit: `{BASE}`. Coolify reference: [`{UPSTREAM}`](https://github.com/coollabsio/coolify/tree/{UPSTREAM}).", "",
             f"Primary audited checklist coverage, a conservative evidence-verified lower bound: **{verified}/{len(rows)} = {100 * verified / len(rows):.2f}%**. Each atomic mandatory hosting outcome has one binary point. Only `verified` earns a point. This measures evidence-complete coverage of this versioned checklist. It does not measure engineering effort, elapsed time, implementation completion, or an intrinsic percentage of Coolify.", "",
             f"Implementation presence is separate: {present['implemented']} implemented, {present['partial']} partial, {present['missing']} missing. Verified: {verified}. These totals cannot be added.", "",
             "Original inventory: 68 requirement parents, 37 acceptance parents, and 58 matrix parents: 19 areas, 9 databases, 10 sources, 3 secret providers, 7 builders, 4 proxies, and 6 notification channels. Parent/gate rows receive no extra score.", "",
             f"Fully complete requirement parents: {sum(complete(p) for p in data['parents'] if p['kind'] == 'requirement')}/68. Acceptance gates passed: {sum(p['state'] == 'verified' for p in data['parents'] if p['kind'] == 'acceptance')}/37. Matrix gates passed: {sum(p['state'] == 'verified' for p in data['parents'] if p['kind'] not in {'acceptance', 'requirement'})}/58.", "",
             "## Method and scope", "", *data["methodology"], "", *data["semantic_review_notes"], "", "Scope exclusions and imported safety obligations:", "",
             *["- " + s for s in data["scope_notes"]], "", "Evidence certainty and known contradictions:", "",
             *["- " + s for s in data["evidence_notes"]], "",
             "A missing verification artifact or affected/unknown historical dependency removes verified credit. A source test is implementation evidence until its actual pass is retained. `partial` has zero points. No runtime tests are implied by generating this report.", "",
             "The original source row is retained verbatim in JSON with its pinned SHA and line range. Criterion source links point to that full requirement. Read acceptance and matrix gates alongside the atomic checklist before making a release claim.", "",
             "## Reproduce", "", "Run from the repository root with Python 3 and Git. No network or third-party packages are required.", "", "```sh", "python3 -B scripts/parity_audit.py --write", "python3 -B scripts/parity_audit.py --check", "python3 -B scripts/parity_audit.py --self-test", "```", "",
             "`--check` validates the original inventory at the audited SHA, frozen criterion manifest, links, source ranges, evidence subjects and embedded pass artifacts, then rejects a stale generated report. `--self-test` exercises rejected missing/duplicate scope, invalid evidence and stale-report cases. Fresh harmless unit-check JSON output is preserved in the checklist. Historical `/tmp` runtime artifacts receive no current verified credit. This audit does not run application/runtime tests.", "",
             "## Coverage by area", "", "| Area | Verified / atomic | Coverage |", "| --- | ---: | ---: |"]
    areas = collections.defaultdict(list)
    for criterion in rows:
        areas[criterion["area"]].append(criterion)
    for area, group in areas.items():
        count = sum(c["state"] == "verified" for c in group)
        lines.append(f"| {area} | {count}/{len(group)} | {100 * count / len(group):.2f}% |")
    lines.extend(["", "## Atomic checklist", "", "Every row names a mandatory outcome and its next acceptance. Implementation presence and verification status have separate columns.", ""])
    for parent in data["parents"]:
        if parent["kind"] != "requirement":
            continue
        rid = parent["id"]
        title = parent["text"].split(". ", 2)[1]
        lines.extend([f"### {rid}. {title}", "", f"[Original requirement]({source_url(parent)}) · fully_complete: `{str(complete(parent)).lower()}` · gates: {', '.join(parent['gates']) or 'none' }.", "",
                      "| Criterion and mandatory outcome | Presence | Evidence state | Evidence and limits | Next acceptance |", "| --- | --- | --- | --- | --- |"])
        for cid in parent["criteria"]:
            c = criteria[cid]
            evidence = "; ".join(f"[{e['path']}:{e['start']}]({source_url(e, e['subject_sha'])})" for e in c["evidence"]) or "No bounded source evidence recorded"
            if c["verification"]:
                v = c["verification"]
                evidence += f"; `{v['test']}` passed at `{v['subject_sha']}`; pass artifact embedded in JSON; {v['profile']}; {v['relevance']}"
            lines.append(f"| `{cid}` {cell(c['outcome'])} | {c['presence']} | {c['state']} | {cell(evidence + '; ' + c['limits'])} | {cell(c['next_acceptance'])} |")
        lines.append("")
    lines.extend(["## Acceptance and matrix gates", "", "These are separate binary release gates. The linked criteria receive the only primary score. The source link preserves the exact row meaning; a gate stays incomplete until its entire outcome passes.", "",
                  "| Gate | Kind and original meaning | State | Atomic links | Gap / next acceptance |", "| --- | --- | --- | --- | --- |"])
    for p in data["parents"]:
        if p["kind"] != "requirement":
            lines.append(f"| [{p['id']}]({source_url(p)}) | {p['kind']}: {cell(p['text'])} | {p['state']} | {', '.join(p['criteria'])} | {cell(p['gap'])} |")
    lines.extend(["", "## Conditional client compatibility gate", "", data["unresolved_client_contract"]["description"], "",
                  "Schema 1; scoring `hosting-atomic-v1`. Criterion IDs and outcome definitions are frozen for this baseline. A future scope revision must record added and removed IDs and publish both the old and new denominator. Changing evidence does not change the denominator.", ""])
    lines.extend(["## Update the audit", "", *[f"{i}. {step}" for i, step in enumerate(data["update_procedure"], 1)], ""])
    return "\n".join(lines)


def source_url(row, sha=BASE):
    path = row["path"]
    relative = path[5:] if path.startswith("docs/") else "../" + path
    return f"{relative}#L{row['start']}-L{row['end']}"


def cell(value):
    return value.replace("|", "\\|").replace("\n", " ")


def self_test(data):
    import copy
    mutations = {
        "missing original scope": lambda d: d["parents"].pop(),
        "duplicate parent": lambda d: d["parents"].append(d["parents"][0]),
        "duplicate criterion": lambda d: d["criteria"].append(d["criteria"][0]),
        "invalid evidence SHA": lambda d: d["criteria"][0]["evidence"].append(dict(subject_sha="bad", path="docs/product-spec.md", start=1, end=1, relevance="x", limits="x")),
        "missing verified evidence": lambda d: d["criteria"][0].update(state="verified", presence="implemented", evidence=[]),
        "incorrect total": lambda d: d.update(baseline_criterion_count=0),
        "changed criterion": lambda d: d["criteria"][0].update(outcome="changed"),
        "missing supplemental scope": lambda d: d["scope_obligations"].pop(),
        "missing implemented source evidence": lambda d: d["criteria"][0].update(presence="implemented", evidence=[]),
    }
    for name, mutate in mutations.items():
        candidate = copy.deepcopy(data)
        mutate(candidate)
        try:
            validate(candidate)
        except (ValueError, KeyError, subprocess.CalledProcessError):
            print(f"passed: reject {name}")
        else:
            raise ValueError(f"negative case accepted: {name}")
    parents, criteria = validate(data)
    report = render(data, parents, criteria)
    import tempfile
    with tempfile.TemporaryDirectory(prefix="nerocd-parity-negative-") as directory:
        target = Path(directory) / "stale.md"
        target.write_text(report + "stale")
        try:
            check_report(target, report)
        except ValueError:
            print("passed: reject stale report using the real --check validator")
        else:
            raise ValueError("stale report accepted")


def check_report(target, report):
    if not target.is_file() or target.read_text() != report:
        raise ValueError("stale generated report; run --write")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data", type=Path, default=ROOT / "docs/parity-checklist.json")
    actions = parser.add_mutually_exclusive_group(required=True)
    actions.add_argument("--write", action="store_true")
    actions.add_argument("--check", action="store_true")
    actions.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    try:
        data = json.loads(args.data.read_text())
        parents, criteria = validate(data)
        report = render(data, parents, criteria)
        target = ROOT / "docs/parity-audit.md"
        if args.write:
            target.write_text(report)
        elif args.check:
            check_report(target, report)
        else:
            self_test(data)
        verified = sum(c["state"] == "verified" for c in data["criteria"])
        print(f"passed: 68 requirements + 37 acceptance + 58 matrix; {verified}/{len(criteria)} = {100 * verified / len(criteria):.2f}% verified")
    except (ValueError, KeyError, TypeError, OSError, subprocess.CalledProcessError) as error:
        print(f"parity audit failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
