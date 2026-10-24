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
BASELINE_CRITERIA = 934
BASELINE_ELIGIBLE = 933
BASELINE_MANIFEST = '86c2cdd843ce78f2a9e33417f9fb59c64285e8f63308b778e0e651f56fd053a3'
BASELINE_PARENT_MANIFEST = '61070857bb0fef33a0802f752afbf7e13f59f4381b0289785d141f7bc211efe7'
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
    # Do not strip source whitespace: excerpts must match the immutable Git object.
    return subprocess.check_output(["/usr/bin/git", "-C", str(ROOT), "show", f"{sha}:{path}"], text=True).splitlines()


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


def validate_receipt(verification, evidence, label):
    """Accept an observed passing named test bound to source and relevant inputs."""
    def require(condition, message):
        if not condition:
            raise ValueError(f"{label}: {message}")

    require(isinstance(verification, dict), "missing verification receipt")
    for key in ("test", "command", "subject_sha", "profile", "limits", "artifact",
                "artifact_sha256", "relevance", "dependency_paths"):
        require(verification.get(key), f"verified without {key}")
    require(verification.get("result") == "passed", "non-passing verification")
    sha = verification["subject_sha"]
    require(re.fullmatch(r"[0-9a-f]{40}", sha) is not None, "bad verification SHA")
    require(git("rev-parse", sha + "^{commit}") == sha, "verification commit unavailable")
    require(any(e["subject_sha"] == sha for e in evidence), "unbound verification subject")
    for item in evidence:
        lines = source_lines(item["subject_sha"], item["path"])
        require(1 <= item["start"] <= item["end"] <= len(lines), "invalid bounded evidence range")
    artifact = verification["artifact"]
    require(isinstance(artifact, dict) and artifact.get("exit_code") == 0, "artifact unavailable/nonpassing")
    require(hashlib.sha256(json.dumps(artifact, sort_keys=True).encode()).hexdigest() == verification["artifact_sha256"], "artifact changed")
    events = [json.loads(line) for line in artifact["stdout"].splitlines() if line]
    test = verification["test"]
    require(any(e.get("Action") == "run" and e.get("Test") == test for e in events), "named test did not run")
    require(any(e.get("Action") == "pass" and e.get("Test") == test for e in events), "named test did not pass")
    require(not any(e.get("Action") in {"skip", "fail"} for e in events), "skipped/failed verification")
    for path in verification["dependency_paths"]:
        require(git("rev-parse", f"{sha}:{path}") == git("rev-parse", f"{BASE}:{path}"), f"affected historical dependency: {path}")
    test_sources = ["\n".join(source_lines(e["subject_sha"], e["path"])[e["start"] - 1:e["end"]]) for e in evidence if e["subject_sha"] == sha]
    require(any(test in content for content in test_sources), "named test absent from bounded source evidence")


def parent_manifest(parents):
    return [[p["id"], p["criteria"], p["gates"], p.get("owner_requirements", [])] for p in parents]


def manifest_hash(value):
    return hashlib.sha256(json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()


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
    require(manifest_hash(parent_manifest(parents)) == BASELINE_PARENT_MANIFEST, "changed frozen parent criterion/gate mappings")
    require(data["baseline_parent_manifest_sha256"] == BASELINE_PARENT_MANIFEST, "wrong parent mapping manifest")
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
    manifest = [[c["id"], c["requirement"], c["outcome"], c["source"], c["eligibility"]] for c in criteria]
    digest = hashlib.sha256(json.dumps(manifest, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
    require(digest == data["baseline_manifest_sha256"], "criterion baseline changed without versioned manifest history")
    require(digest == BASELINE_MANIFEST and len(criteria) == BASELINE_CRITERIA, "changed frozen baseline definition/denominator")
    require(data["criterion_history"] == [{"version": "hosting-atomic-v1", "added": [c["id"] for c in criteria], "removed": []}], "invalid baseline criterion history")
    require(data["baseline_criterion_count"] == len(criteria), "incorrect frozen criterion total")
    require(data["baseline_eligible_count"] == BASELINE_ELIGIBLE == sum(c["eligibility"] == "hosting" for c in criteria), "incorrect eligible hosting total")
    trigger_criteria = [f"R10-C{i:02}" for i in range(1, 7)]
    trigger_gates = {"GitHub": "SRC01", "GitLab": "SRC02", "Gitea": "SRC03",
                     "Bitbucket": "SRC04", "Signed manual deploy webhook": "SRC06"}
    realizations = data["trigger_realizations"]
    require(len(realizations) == len(trigger_gates) and {r["provider"]: r["gate"] for r in realizations} == trigger_gates, "missing declared trigger realization")
    for realization in realizations:
        require(realization["scored"] is False and realization["criteria"] == trigger_criteria, "trigger realization duplicates points or drops contract clauses")
        require(set(trigger_criteria) <= set(parent_map[realization["gate"]]["criteria"]), "provider gate omits trigger contract")
    require(all(row["boundary"] and set(row["scored"]) <= criterion_map.keys() for row in data["dedup_family_review"]), "invalid semantic family review links")
    covered = collections.Counter()
    for criterion in criteria:
        cid = criterion["id"]
        owner = criterion["requirement"]
        require(owner in parent_map and parent_map[owner]["kind"] == "requirement", f"invalid owner: {cid}")
        require(cid in parent_map[owner]["criteria"], f"unlinked owned criterion: {cid}")
        require(re.fullmatch(owner + r"-C\d{2,}", cid) is not None, f"invalid criterion ID: {cid}")
        require(criterion["state"] in STATES, f"invalid state: {cid}")
        require(criterion["eligibility"] in {"hosting", "deferred_task"}, f"invalid eligibility: {cid}")
        require(criterion["eligibility"] == "hosting" or (cid == "R54-C03" and criterion["state"] != "verified"), f"invalid conditional criterion: {cid}")
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
            validate_receipt(criterion["verification"], criterion["evidence"], cid)
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
            require(all(criterion_map[c]["state"] == "verified" for c in parent["criteria"] if criterion_map[c]["eligibility"] == "hosting"), f"gate verified with gaps: {parent['id']}")
            require(all(parent_map[g]["state"] == "verified" for g in parent["gates"]), f"gate dependencies have gaps: {parent['id']}")
            receipt = parent.get("gate_verification")
            require(isinstance(receipt, dict), f"gate lacks combined acceptance receipt: {parent['id']}")
            require(receipt.get("criteria") == [cid for cid in parent["criteria"] if criterion_map[cid]["eligibility"] == "hosting"], f"gate receipt has wrong criterion boundary: {parent['id']}")
            validate_receipt(receipt, receipt.get("evidence", []), parent["id"])
            if parent["id"] == "A36":
                require(receipt["subject_sha"] == BASE and all(parent_map[g]["gate_verification"]["subject_sha"] == BASE for g in parent["gates"]), "A36 requires same-audit-candidate gate receipts")
        elif parent["kind"] != "requirement":
            require(parent["gate_verification"] is None, f"unverified gate carries receipt: {parent['id']}")
    require(data["unresolved_client_contract"]["state"] == "missing", "client endpoint contract cannot pass without inventory")
    return parent_map, criterion_map


def render(data, parents, criteria):
    rows = [c for c in data["criteria"] if c["eligibility"] == "hosting"]
    verified = sum(c["state"] == "verified" for c in rows)
    present = collections.Counter(c["presence"] for c in rows)
    complete = lambda p: all(criteria[c]["state"] == "verified" for c in p["criteria"] if criteria[c]["eligibility"] == "hosting") and all(parents[g]["state"] == "verified" for g in p["gates"])
    lines = ["# Hosting parity audit", "", "<!-- Generated by scripts/parity_audit.py. Edit docs/parity-checklist.json, then regenerate. -->", "",
             f"Audited NeroCD commit: `{BASE}`. Coolify reference: [`{UPSTREAM}`](https://github.com/coollabsio/coolify/tree/{UPSTREAM}).", "",
             f"Primary audited checklist coverage, a conservative evidence-verified lower bound: **{verified}/{len(rows)} = {100 * verified / len(rows):.2f}%**. Each atomic mandatory hosting outcome has one binary point. Only `verified` earns a point. This measures evidence-complete coverage of this versioned checklist. It does not measure engineering effort, elapsed time, implementation completion, or an intrinsic percentage of Coolify.", "",
             f"Implementation presence is separate: {present['implemented']} implemented, {present['partial']} partial, {present['missing']} missing. Verified: {verified}. These totals cannot be added.", "",
             f"Definition inventory: {len(data['criteria'])}; eligible hosting outcomes: {len(rows)}. Deferred Task-derived preview R54-C03 is retained outside this numerator, denominator and hosting-gate boundary.", "",
             "Original inventory: 68 requirement parents, 37 acceptance parents, and 58 matrix parents: 19 areas, 9 databases, 10 sources, 3 secret providers, 7 builders, 4 proxies, and 6 notification channels. Parent/gate rows receive no extra score.", "",
             f"Fully complete requirement parents: {sum(complete(p) for p in data['parents'] if p['kind'] == 'requirement')}/68. Acceptance gates passed: {sum(p['state'] == 'verified' for p in data['parents'] if p['kind'] == 'acceptance')}/37. Matrix gates passed: {sum(p['state'] == 'verified' for p in data['parents'] if p['kind'] not in {'acceptance', 'requirement'})}/58.", "",
             "Full parity is not accepted until all applicable requirement, acceptance and matrix release gates pass. A future 100% atomic score alone does not establish release acceptance.", "",
             "## Method and scope", "", *data["methodology"], "", *data["semantic_review_notes"], "", "Scope exclusions and imported safety obligations:", "",
             *["- " + s for s in data["scope_notes"]], "", "Evidence certainty and known contradictions:", "",
             *["- " + s for s in data["evidence_notes"]], "",
             "A missing verification artifact or affected/unknown historical dependency removes verified credit. A source test is implementation evidence until its actual pass is retained. `partial` has zero points. No runtime tests are implied by generating this report.", "",
             "Every source link points to an immutable excerpt appendix in this report, generated with `git show SHA:path`. Anchors include the full subject SHA, path hash and line range. Historical evidence has distinct targets even when its path and lines match the current source. No unpublished commit depends on a remote GitHub link. Read acceptance and matrix gates alongside the atomic checklist before making a release claim.", "",
             "## Reproduce", "", "Run from the repository root with Python 3 and Git. No network or third-party packages are required.", "", "```sh", "python3 -B scripts/parity_audit.py --write", "python3 -B scripts/parity_audit.py --check", "python3 -B scripts/parity_audit.py --self-test", "```", "",
             "`--check` validates the original inventory at the audited SHA, frozen criterion manifest, links, source ranges, evidence subjects and embedded pass artifacts, then rejects a stale generated report. `--self-test` exercises rejected missing/duplicate scope, invalid evidence and stale-report cases. Fresh harmless unit-check JSON output is preserved in the checklist. Historical `/tmp` runtime artifacts receive no current verified credit. This audit does not run application/runtime tests.", "",
             "## Coverage by area", "", "| Area | Verified / atomic | Coverage |", "| --- | ---: | ---: |"]
    areas = collections.defaultdict(list)
    for criterion in rows:
        areas[criterion["area"]].append(criterion)
    for area, group in areas.items():
        count = sum(c["state"] == "verified" for c in group)
        lines.append(f"| {area} | {count}/{len(group)} | {100 * count / len(group):.2f}% |")
    lines.extend(["", "## Semantic duplication review", "", "Shared contracts score once. Their provider or resource realizations remain required acceptance boundaries without an additional point for the same observable outcome.", "",
                  "| Family | Scored contract or distinct boundary IDs | Why the retained boundaries differ |", "| --- | --- | --- |"])
    for family in data["dedup_family_review"]:
        lines.append(f"| {cell(family['family'])} | {', '.join(family['scored'])} | {cell(family['boundary'])} |")
    lines.extend(["", "Trigger realization gates add zero primary points. Each actual provider/manual path must satisfy authentication, durable delivery identity, replay deduplication, intended revision, queued supersession and protection of the active newer release.", "",
                  "| Trigger realization | Unscored acceptance gate | Shared scored contract |", "| --- | --- | --- |"])
    for realization in data["trigger_realizations"]:
        lines.append(f"| {cell(realization['provider'])} | {realization['gate']} | {', '.join(realization['criteria'])} |")
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
            eligibility = " [deferred, outside hosting score]" if c["eligibility"] != "hosting" else ""
            lines.append(f"| `{cid}` {cell(c['outcome'])}{eligibility} | {c['presence']} | {c['state']} | {cell(evidence + '; ' + c['limits'])} | {cell(c['next_acceptance'])} |")
        lines.append("")
    lines.extend(["## Acceptance and matrix gates", "", "These are separate binary release gates. The linked criteria receive the only primary score. The source link preserves the exact row meaning; a gate stays incomplete until its entire outcome passes.", "",
                  "| Gate | Kind and original meaning | State | Atomic links | Gap / next acceptance |", "| --- | --- | --- | --- | --- |"])
    for p in data["parents"]:
        if p["kind"] != "requirement":
            lines.append(f"| [{p['id']}]({source_url(p)}) | {p['kind']}: {cell(p['text'])} | {p['state']} | {', '.join(p['criteria'])} | {cell(p['gap'])} |")
    lines.extend(["", "## Conditional client compatibility gate", "", data["unresolved_client_contract"]["description"], "",
                  "Schema 1; scoring `hosting-atomic-v1`. Criterion IDs and outcome definitions are frozen for this baseline. A future scope revision must record added and removed IDs and publish both the old and new denominator. Changing evidence does not change the denominator.", ""])
    lines.extend(["## Update the audit", "", *[f"{i}. {step}" for i, step in enumerate(data["update_procedure"], 1)], ""])
    lines.extend(source_appendix(source_references(data)))
    return "\n".join(lines)


def source_url(row, sha=BASE):
    path_hash = hashlib.sha256(row["path"].encode()).hexdigest()
    return f"#source-{sha}-{path_hash}-l{row['start']}-l{row['end']}"


def source_references(data):
    references = set()

    def add(row, sha):
        references.add((sha, row["path"], row["start"], row["end"]))

    for parent in data["parents"]:
        add(parent, BASE)
        for duplicate in parent.get("duplicate_sources", []):
            add(duplicate, duplicate["sha"])
        if parent.get("gate_verification"):
            for evidence in parent["gate_verification"]["evidence"]:
                add(evidence, evidence["subject_sha"])
    for criterion in data["criteria"]:
        for evidence in criterion["evidence"]:
            add(evidence, evidence["subject_sha"])
    for obligation in data["scope_obligations"]:
        add(obligation, obligation["sha"])
    return sorted(references)


def source_appendix(references):
    validate_source_targets(references)
    lines = ["## Immutable source excerpts", "", "Each fenced excerpt contains the exact selected source lines at its named Git object. Line numbers are given above the excerpt rather than inserted into source text.", ""]
    for sha, path, start, end in references:
        row = dict(path=path, start=start, end=end)
        content = "\n".join(source_lines(sha, path)[start - 1:end])
        # Choose a fence longer than every backtick run in untrusted source text.
        fence = "`" * max(3, 1 + max((len(m[0]) for m in re.finditer(r"`+", content)), default=0))
        lines.extend([f'<a id="{source_url(row, sha)[1:]}"></a>', "", f"### `{path}` lines {start}-{end}", "", f"Subject `{sha}`. Reproduce with `git show {sha}:{path}` and select lines {start}-{end}.", "", fence + "text", content, fence, ""])
    return lines


def validate_source_targets(references):
    targets = [source_url(dict(path=path, start=start, end=end), sha) for sha, path, start, end in references]
    if len(targets) != len(set(targets)):
        raise ValueError("immutable source targets alias different subjects")
    if any(sha not in target for (sha, _, _, _), target in zip(references, targets)):
        raise ValueError("immutable source target omits subject SHA")


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
        "relinked passing gate": lambda d: next(p for p in d["parents"] if p["id"] == "A01").update(criteria=["R11-C01"], state="verified", gate_verification={"result": "failed", "exit_code": 1}),
        "removed required gate mapping": lambda d: next(p for p in d["parents"] if p["id"] == "R03").update(gates=[]),
        "incorrect eligible denominator": lambda d: d.update(baseline_eligible_count=1),
        "missing trigger realization": lambda d: d["trigger_realizations"].pop(),
        "extra score for trigger realization": lambda d: d["trigger_realizations"][0].update(scored=True),
        "missing provider trigger clause": lambda d: d["trigger_realizations"][0].update(criteria=["R10-C01"]),
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
    # Exercise the real gate/criterion receipt validator independently of the frozen
    # parent mapping guard, using an actual retained passing receipt as the fixture.
    credited = next(c for c in data["criteria"] if c["state"] == "verified")
    validate_receipt(credited["verification"], credited["evidence"], "positive receipt fixture")
    for failure in ("failed result", "nonzero exit", "wrong named test", "missing test run"):
        receipt = copy.deepcopy(credited["verification"])
        if failure == "failed result":
            receipt["result"] = "failed"
        elif failure == "nonzero exit":
            receipt["artifact"]["exit_code"] = 1
        elif failure == "wrong named test":
            receipt["test"] = "TestUnobservedGatePass"
        else:
            receipt["artifact"]["stdout"] = "\n".join(line for line in receipt["artifact"]["stdout"].splitlines() if json.loads(line).get("Action") != "run")
        receipt["artifact_sha256"] = hashlib.sha256(json.dumps(receipt["artifact"], sort_keys=True).encode()).hexdigest()
        try:
            validate_receipt(receipt, credited["evidence"], "gate receipt negative")
        except ValueError:
            print(f"passed: reject gate receipt {failure}")
        else:
            raise ValueError(f"invalid gate receipt accepted: {failure}")
    historical = "d3849bb572ce5b7df40ef7324ef6bc71e8e08c92"
    path = "internal/runnerruntime/hosting_database_origin_upgrade_runtime_integration_test.go"
    references = [(BASE, path, 690, 754), (historical, path, 690, 754)]
    validate_source_targets(references)
    excerpts = "\n".join(source_appendix(references))
    for sha, source_path, start, end in references:
        anchor = source_url(dict(path=source_path, start=start, end=end), sha)[1:]
        if f'<a id="{anchor}"></a>' not in excerpts or "\n".join(source_lines(sha, source_path)[start - 1:end]) not in excerpts:
            raise ValueError("immutable source appendix lost exact historical subject")
    print("passed: immutable source targets distinguish full historical SHA and preserve exact excerpts")
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
        eligible = [c for c in data["criteria"] if c["eligibility"] == "hosting"]
        verified = sum(c["state"] == "verified" for c in eligible)
        print(f"passed: 68 requirements + 37 acceptance + 58 matrix; {verified}/{len(eligible)} = {100 * verified / len(eligible):.2f}% verified; {len(criteria)} definitions")
        print("Full parity requires all applicable R/A/matrix release gates; an atomic score alone is not acceptance.")
    except (ValueError, KeyError, TypeError, OSError, subprocess.CalledProcessError) as error:
        print(f"parity audit failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
