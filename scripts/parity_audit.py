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
BASE = "09a432407c41671517fa342d28c07e9c79978aea"
UPSTREAM = "b12d1386f3d458618b4c579babcd1e339c6c40b3"
BASELINE_CRITERIA = 934
BASELINE_ELIGIBLE = 933
BASELINE_MANIFEST = '9121b5399e3804a045dc9f8a98b3925b5b4565908daaeaab6535d2cbc3c48b73'
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
    validate_consolidated(data)
    return parent_map, criterion_map


ACCEPTED_CUTOFF = "f96d8c6267fcab2273b6f748d09801931660ceaf"
# Version 1 pins the reviewed safe artifact inventory, including original hashes.
NORMALIZED_ARTIFACT_MANIFEST = "957b2486ed86587c5530c66679edd576b30d85f30fb3cd579e4e3c78dd6c50ac"


def projection_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def effective_criteria(data):
    """Derive one current row per eligible ID; realizations never add points."""
    assessments = collections.defaultdict(list)
    for group in data["consolidated_snapshot"]["groups"]:
        for outcome in group["outcomes"]:
            assessments[outcome["criterion_id"]].append((group, outcome))
    result = []
    for criterion in data["criteria"]:
        if criterion["eligibility"] != "hosting":
            continue
        cid = criterion["id"]
        realizations = assessments[cid]
        validated = [(g, o) for g, o in realizations if o["classification"] == "validated"]
        historical = criterion["state"] == "verified"
        partial = any(o["classification"] == "partial" for _, o in realizations)
        result.append(dict(
            criterion_id=cid, outcome=criterion["outcome"],
            historical_presence=criterion["presence"], historical_state=criterion["state"],
            effective_presence="implemented" if validated else ("partial" if partial and criterion["presence"] == "missing" else criterion["presence"]),
            effective_state="accepted_profile_verified" if validated else ("historical_verified" if historical else ("partial" if partial else criterion["state"])),
            credited=historical or bool(validated),
            historical_evidence_ref="criteria/" + cid,
            assessments=[o["assessment_ref"] for _, o in realizations],
            accepted_proof_refs=[dict(group_id=g["id"], accepted_sha=g["accepted_sha"], physical_source_sha=g["physical_source_sha"],
                                      tests=[p["test"] for p in o["proofs"]], scope=o["scope"], limits=o["limits"])
                                 for g, o in validated],
            limits=criterion["limits"] if not validated else "Only the listed accepted profiles qualify. Original historical limits and all aggregate gate gaps remain in force.",
        ))
    return result


def validate_consolidated(data):
    """Validate portable selected observations against immutable acceptance sources."""
    def require(condition, message):
        if not condition:
            raise ValueError("consolidated: " + message)

    snapshot = data["consolidated_snapshot"]
    require(snapshot["schema_version"] == 1 and snapshot["scoring_version"] == "cumulative-accepted-profile-v1", "unsupported snapshot schema")
    require(snapshot["accepted_main_cutoff"] == ACCEPTED_CUTOFF and snapshot["historical_audit_sha"] == BASE, "wrong cutoff or historical audit")
    require(snapshot["policy"] and snapshot["evidence_policy"] and snapshot["exceptions"] and snapshot["future_update_procedure"], "missing scope or update policy")
    originals = {g["id"]: g for g in data["supplemental_assessments"]}
    groups = snapshot["groups"]
    require(len(originals) == len(data["supplemental_assessments"]) == 9, "duplicate or missing supplemental group")
    require(len(groups) == len({g["id"] for g in groups}) == len(originals), "duplicate or missing normalized group")
    require({g["id"] for g in groups} == originals.keys(), "unknown normalized group")
    criteria = {c["id"]: c for c in data["criteria"]}
    for group in groups:
        original = originals[group["id"]]
        require(original["assessment_kind"] == "later_bounded_acceptance" and original["historical_audit_unchanged"] is True, "invalid supplemental kind")
        require(original["historical_eligible_count"] == BASELINE_ELIGIBLE and original["historical_verified_count"] == 30, "changed historical supplemental count")
        sha, physical = group["accepted_sha"], group["physical_source_sha"]
        for subject in (sha, physical):
            require(re.fullmatch(r"[0-9a-f]{40}", subject) is not None, "invalid source identity")
            require(git("rev-parse", subject + "^{commit}") == subject, "unavailable source identity")
        require(git("merge-base", sha, ACCEPTED_CUTOFF) == sha, "accepted SHA is not an ancestor of cutoff")
        require(group["physical_tree"] == git("rev-parse", physical + "^{tree}"), "wrong physical source tree")
        changes = git("diff", "--name-only", physical, sha).splitlines()
        allow = group["documentation_equivalence_allowlist"]
        require(changes == allow and all(p == "README.md" or (p.startswith("docs/") and p.endswith(".md")) or p == "docs/parity-checklist.json" for p in allow), "changed product dependency under documentation equivalence")
        artifacts = {a["id"]: a for a in group["artifacts"]}
        require(len(artifacts) == len(group["artifacts"]), "duplicate artifact")
        paths = set(original.get("evidence_paths", []))
        for outcome in original["outcomes"]:
            paths.update(outcome["current_bounded_assessment"]["evidence_paths"])
        identities, successful_gate = set(), False
        for artifact in artifacts.values():
            require(artifact["role"] in {"physical_gate_identity", "named_test_observations"}, "unknown artifact role")
            require(artifact["original_path"] in paths and re.fullmatch(r"[0-9a-f]{64}", artifact["original_sha256"]) is not None, "missing original provenance")
            projection = artifact["projection"]
            require(projection_hash(projection) == artifact["projection_sha256"], "essential artifact hash mismatch")
            for key in ("candidate", "source", "head"):
                if key in projection:
                    identities.add(projection[key])
            if "tree" in projection:
                require(projection["tree"] == group["physical_tree"], "receipt/source tree mismatch")
            for key in ("exit_code", "gate_exit", "exit", "failures", "skips"):
                if key in projection:
                    require(projection[key] == 0, "failed or skipped physical receipt")
            for key in ("failures_or_skips",):
                require(not projection.get(key), "failed or skipped physical receipt")
            require(projection.get("redaction_failure", False) is False, "redaction failed")
            if "no_fail_or_skip" in projection:
                require(projection["no_fail_or_skip"] is True, "failed or skipped physical receipt")
            terminal = projection.get("terminal")
            if terminal is not None:
                require(terminal.get("exit") == 0 and terminal.get("joined") is True and terminal.get("deadline_reached") is False, "physical gate not successfully joined")
            if artifact["id"] == group["gate_artifact_id"]:
                require(artifact["role"] == "physical_gate_identity", "missing essential gate role")
                successful_gate = terminal is not None or any(projection.get(k) == 0 for k in ("exit_code", "gate_exit", "exit") if k in projection)
        require(physical in identities and successful_gate, "missing source-bound passing physical gate")
        original_outcomes = {o["criterion_id"]: o for o in original["outcomes"]}
        require(len(original_outcomes) == len(original["outcomes"]), "duplicate supplemental criterion")
        require(len(group["outcomes"]) == len(original_outcomes) and {o["criterion_id"] for o in group["outcomes"]} == original_outcomes.keys(), "missing normalized outcome")
        for outcome in group["outcomes"]:
            cid = outcome["criterion_id"]
            require(cid in criteria and criteria[cid]["eligibility"] == "hosting", "unknown or deferred criterion")
            record = original_outcomes[cid]
            assessment = record["current_bounded_assessment"]
            require(record["outcome"] == outcome["outcome"] == criteria[cid]["outcome"], "altered supplemental outcome text")
            require(assessment["classification"] in {"validated", "partial"}, "unknown original classification")
            require(assessment.get("accepted_sha", original.get("accepted_sha")) == sha, "wrong accepted subject")
            require(assessment.get("physical_source_sha", original.get("physical_sha", sha)) == physical, "wrong physical subject")
            require(outcome["classification"] in {"validated", "partial", "unsupported"}, "unknown normalized classification")
            require(outcome["scope"] and outcome["adequacy"] and outcome["limits"] == assessment["limits"] and outcome["assessment_ref"] == group["id"] + "/" + cid, "missing profile, limits or reference")
            if "scope" in assessment:
                require(outcome["scope"] == assessment["scope"], "changed assessment scope")
            if assessment["classification"] == "partial":
                require(outcome["classification"] == "partial", "partial-only criterion given credit")
            require(bool(outcome["proofs"]) or outcome["classification"] in {"unsupported", "partial"}, "validated without observed named test")
            tests = [p["test"] for p in outcome["proofs"]]
            observed = {match[1] for artifact in artifacts.values() for line in artifact["projection"].get("test_observations", [])
                        if (match := re.search(r"--- PASS: (\S+)", line))}
            require(set(tests) == set(assessment["named_tests"]) & observed, "selected essential named tests omitted")
            require(len(tests) == len(set(tests)), "duplicate selected test")
            require(set(tests).isdisjoint(outcome["unobserved_named_tests"]) and set(tests) | set(outcome["unobserved_named_tests"]) == set(assessment["named_tests"]), "selected/unobserved test inventory changed")
            for proof in outcome["proofs"]:
                require(proof["artifact_ids"] and set(proof["artifact_ids"]) <= artifacts.keys(), "missing essential named-test artifact")
                for aid in proof["artifact_ids"]:
                    observations = artifacts[aid]["projection"].get("test_observations", [])
                    pattern = re.escape(proof["test"]) + r"(?:\s|$)"
                    subtree = re.escape(proof["test"]) + r"(?:/|\s|$)"
                    require(any(re.search(r"--- PASS: " + pattern, line) for line in observations), "unobserved named test")
                    require(not any(re.search(r"--- (?:FAIL|SKIP): " + subtree, line) for line in observations), "failed or skipped named test")
                source = proof["source"]
                require(source["subject_sha"] == physical, "named-test source is not physical source")
                lines = source_lines(physical, source["path"])
                require(1 <= source["start"] <= source["end"] <= len(lines), "bad named-test source range")
                require("func " + proof["test"].split("/")[0] + "(" in "\n".join(lines[source["start"] - 1:source["end"]]), "named test absent from source")
    # Preserve every pre-consolidation field, including historical rows and all nine
    # original assessments. Git supplies the immutable accepted document offline.
    frozen = json.loads("\n".join(source_lines(ACCEPTED_CUTOFF, "docs/parity-checklist.json")))
    require({k: v for k, v in data.items() if k != "consolidated_snapshot"} == frozen, "original audit or supplemental history changed")
    artifact_manifest = [[g["id"], a["id"], a["role"], a["original_path"], a["original_sha256"], a["projection_sha256"]]
                         for g in groups for a in g["artifacts"]]
    require(projection_hash(artifact_manifest) == NORMALIZED_ARTIFACT_MANIFEST, "reviewed artifact inventory changed")
    expected = effective_criteria(data)
    require(snapshot["effective_criteria"] == expected, "stale or altered effective criterion view")
    require(len(expected) == BASELINE_ELIGIBLE and len({r["criterion_id"] for r in expected}) == BASELINE_ELIGIBLE, "wrong effective denominator")
    return expected


def render(data, parents, criteria):
    effective = {r["criterion_id"]: r for r in effective_criteria(data)}
    accepted = sum(r["credited"] for r in effective.values())
    effective_presence = collections.Counter(r["effective_presence"] for r in effective.values())
    rows = [c for c in data["criteria"] if c["eligibility"] == "hosting"]
    verified = sum(c["state"] == "verified" for c in rows)
    present = collections.Counter(c["presence"] for c in rows)
    complete = lambda p: all(criteria[c]["state"] == "verified" for c in p["criteria"] if criteria[c]["eligibility"] == "hosting") and all(parents[g]["state"] == "verified" for g in p["gates"])
    lines = ["# Hosting parity audit", "", "<!-- Generated by scripts/parity_audit.py. Edit docs/parity-checklist.json, then regenerate. -->", "",
             f"Cumulative accepted bounded-profile criterion coverage: **{accepted}/{len(rows)} = {100 * accepted / len(rows):.2f}%**. Accepted-main cutoff: `{ACCEPTED_CUTOFF}`. The numerator is the union of distinct criterion IDs with sufficient accepted evidence: {verified} historical criteria plus {accepted - verified} later criteria. Partial evidence earns zero. Repeated PostgreSQL and owned-upload realizations count once. Superseded `audit_history` receipts stay historical and add no resurrected credit.", "",
             "This percentage measures cumulative accepted bounded-profile criterion coverage. It does not measure engineering effort or full product completion, and it does not claim simultaneous qualification of every receipt at the current SHA or cutoff. Each credited row retains its own physical source, accepted revision, profile and limits. Pending catalog staging is excluded.", "",
             f"Effective implementation presence within those profiles: {effective_presence['implemented']} implemented, {effective_presence['partial']} partial, {effective_presence['missing']} missing. These presence counts are separate from the accepted criterion score.", "",
             "## Historical audit retained unchanged", "",
             f"Audited NeroCD commit: `{BASE}`. Coolify reference: [`{UPSTREAM}`](https://github.com/coollabsio/coolify/tree/{UPSTREAM}).", "",
             f"Historical audited checklist coverage, retained at its original SHA: **{verified}/{len(rows)} = {100 * verified / len(rows):.2f}%**. Each atomic mandatory hosting outcome has one binary point. Only `verified` earns a point. This measures evidence-complete coverage of this versioned checklist. It does not measure engineering effort, elapsed time, implementation completion, or an intrinsic percentage of Coolify.", "",
             f"Historical implementation presence is separate: {present['implemented']} implemented, {present['partial']} partial, {present['missing']} missing. Verified: {verified}. These totals cannot be added.", "",
             f"Definition inventory: {len(data['criteria'])}; eligible hosting outcomes: {len(rows)}. Deferred Task-derived preview R54-C03 is retained outside this numerator, denominator and hosting-gate boundary.", "",
             "Original inventory: 68 requirement parents, 37 acceptance parents, and 58 matrix parents: 19 areas, 9 databases, 10 sources, 3 secret providers, 7 builders, 4 proxies, and 6 notification channels. Parent/gate rows receive no extra score.", "",
             f"Fully complete requirement parents: {sum(complete(p) for p in data['parents'] if p['kind'] == 'requirement')}/68. Acceptance gates passed: {sum(p['state'] == 'verified' for p in data['parents'] if p['kind'] == 'acceptance')}/37. Matrix gates passed: {sum(p['state'] == 'verified' for p in data['parents'] if p['kind'] not in {'acceptance', 'requirement'})}/58.", "",
             "Full parity is not accepted until all applicable requirement, acceptance and matrix release gates pass. A future 100% atomic score alone does not establish release acceptance.", "",
             "## Historical method and scope", "", "The following original methodology and evidence notes describe the pinned historical audit. Their references to the primary figure and current source belong to that historical snapshot. The consolidated policy and effective rows above supersede the historical-only headline for cumulative reporting.", "", *data["methodology"], "", *data["semantic_review_notes"], "", "Scope exclusions and imported safety obligations:", "",
             *["- " + s for s in data["scope_notes"]], "", "Evidence certainty and known contradictions:", "",
             *["- " + s for s in data["evidence_notes"]], "",
             "A missing verification artifact or affected/unknown historical dependency removes verified credit. A source test is implementation evidence until its actual pass is retained. `partial` has zero points. No runtime tests are implied by generating this report.", "",
             "Every source link points to an immutable excerpt appendix in this report, generated with `git show SHA:path`. Anchors include the full subject SHA, path hash and line range. Historical evidence has distinct targets even when its path and lines match the current source. No unpublished commit depends on a remote GitHub link. Read acceptance and matrix gates alongside the atomic checklist before making a release claim.", "",
             "## Reproduce", "", "Run from the repository root with Python 3 and Git. No network or third-party packages are required.", "", "```sh", "python3 -B scripts/parity_audit.py --write", "python3 -B scripts/parity_audit.py --check", "python3 -B scripts/parity_audit.py --self-test", "```", "",
             "`--check` validates the original inventory, immutable history, supplemental definitions, accepted ancestry, physical source identity, exact documentation equivalence, safe retained observations, content hashes and effective rows. It then rejects a stale generated report. Original receipt paths are provenance, not offline dependencies. `--self-test` exercises historical guards, supplemental evidence failures, partial-only credit, duplicate realizations and stale-report cases. Observed named-test output is preserved in the checklist, including current physical output converted with test2json. Historical receipts retain credit only after complete declared dependency equality. Generating this audit does not run application/runtime tests.", "",
             "## Coverage by area", "", "| Area | Cumulative accepted / atomic | Coverage |", "| --- | ---: | ---: |"]
    areas = collections.defaultdict(list)
    for criterion in rows:
        areas[criterion["area"]].append(criterion)
    for area, group in areas.items():
        count = sum(effective[c["id"]]["credited"] for c in group)
        lines.append(f"| {area} | {count}/{len(group)} | {100 * count / len(group):.2f}% |")
    lines.extend(["", "## Semantic duplication review", "", "Shared contracts score once. Their provider or resource realizations remain required acceptance boundaries without an additional point for the same observable outcome.", "",
                  "| Family | Scored contract or distinct boundary IDs | Why the retained boundaries differ |", "| --- | --- | --- |"])
    for family in data["dedup_family_review"]:
        lines.append(f"| {cell(family['family'])} | {', '.join(family['scored'])} | {cell(family['boundary'])} |")
    lines.extend(["", "Trigger realization gates add zero primary points. Each actual provider/manual path must satisfy authentication, durable delivery identity, replay deduplication, intended revision, queued supersession and protection of the active newer release.", "",
                  "| Trigger realization | Unscored acceptance gate | Shared scored contract |", "| --- | --- | --- |"])
    for realization in data["trigger_realizations"]:
        lines.append(f"| {cell(realization['provider'])} | {realization['gate']} | {', '.join(realization['criteria'])} |")
    lines.extend(consolidated_appendix(data))
    lines.extend(["", "## Atomic checklist", "", "One effective row per eligible criterion follows. The historical presence and state remain visible. Accepted-profile verification applies only to the linked assessment scopes, with selected named-test artifacts in the consolidated receipts above. An unassessed row retains its original source evidence, limits and next acceptance. The deferred definition remains outside all effective totals.", ""])
    for parent in data["parents"]:
        if parent["kind"] != "requirement":
            continue
        rid = parent["id"]
        title = parent["text"].split(". ", 2)[1]
        lines.extend([f"### {rid}. {title}", "", f"[Original requirement]({source_url(parent)}) · fully_complete: `{str(complete(parent)).lower()}` · gates: {', '.join(parent['gates']) or 'none' }.", "",
                      "| Criterion and mandatory outcome | Historical presence / state | Effective presence / state | Evidence and limits | Next acceptance |", "| --- | --- | --- | --- | --- |"])
        for cid in parent["criteria"]:
            c = criteria[cid]
            evidence = "; ".join(f"[{e['path']}:{e['start']}]({source_url(e, e['subject_sha'])})" for e in c["evidence"]) or "No bounded source evidence recorded"
            if c["verification"]:
                v = c["verification"]
                evidence += f"; `{v['test']}` passed at `{v['subject_sha']}`; pass artifact embedded in JSON; {v['profile']}; {v['relevance']}"
            eligibility = " [deferred, outside hosting score]" if c["eligibility"] != "hosting" else ""
            current = effective.get(cid)
            current_state = f"{current['effective_presence']} / {current['effective_state']}" if current else "deferred / outside score"
            if current and current["assessments"]:
                links = "; ".join(f"[{ref}](#assessment-{ref.replace('/', '-')})" for ref in current["assessments"])
                evidence = "Consolidated assessments: " + links + "; historical evidence: " + evidence
            lines.append(f"| `{cid}` {cell(c['outcome'])}{eligibility} | {c['presence']} / {c['state']} | {current_state} | {cell(evidence + '; ' + c['limits'])} | {cell(c['next_acceptance'])} |")
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


def consolidated_appendix(data):
    snapshot = data["consolidated_snapshot"]
    lines = ["", "## Consolidated accepted profiles", "", snapshot["policy"], "", snapshot["evidence_policy"], "",
             "The original nine supplemental assessments and historical audit are retained in JSON. The versioned `consolidated_snapshot` contains portable projections and the derived `effective_criteria` view. Original full-file SHA-256 values identify provenance. Projection SHA-256 values detect changes to the retained content. The calculator also pins the reviewed artifact inventory and its original hashes. These hashes detect corruption; they are not signatures or independent proof of authenticity. The accepted record and its immutable Git source supply that trust boundary.", "",
             "The safe projections record actual named passes and any observed selected-test descendant failures or skips. A retained `--- PASS: ExactTestName` observation proves that named execution even when the original summary did not preserve its `=== RUN` line. A package-only `ok` result, source definition, skipped test or absent name supplies no named-test credit. Source-only test names remain explicitly unobserved.", "",
             "Known evidence limits:", "", *["- " + note for note in snapshot["exceptions"]], ""]
    for group in snapshot["groups"]:
        lines.extend([f"### {group['id']}", "", f"Accepted `{group['accepted_sha']}`; physical source `{group['physical_source_sha']}`; physical tree `{group['physical_tree']}`.", "",
                      "Exact documentation-only differences between physical and accepted source: " + (", ".join(f"`{p}`" for p in group["documentation_equivalence_allowlist"]) or "none") + ". Product dependencies are identical across this equivalence boundary. This is not an equivalence claim through the later cutoff.", "",
                      "| Criterion | Selected evidence status and tests | Scope and limits |", "| --- | --- | --- |"])
        for outcome in group["outcomes"]:
            cid = outcome["criterion_id"]
            tests = "; ".join(f"`{p['test']}` [source]({source_url(p['source'], group['physical_source_sha'])}), artifacts {', '.join(p['artifact_ids'])}" for p in outcome["proofs"]) or "No sufficient observed named test"
            omitted = "; unobserved: " + ", ".join(outcome["unobserved_named_tests"]) if outcome["unobserved_named_tests"] else ""
            lines.append(f"| <a id=\"assessment-{group['id']}-{cid}\"></a>`{cid}` | {outcome['classification']}: {cell(tests + omitted)} | {cell(outcome['scope'] + '. ' + outcome['limits'] + ' Proof adequacy: ' + outcome['adequacy'])} |")
        lines.extend(["", "Retained safe receipt projections. Artifact IDs are local to this assessment group. `physical_gate_identity` identifies the source and terminal gate; `named_test_observations` supplies selected actual passes. Legacy `runtime_receipt_sha256` fields are preserved only in their original assessment context. They are not reused as interchangeable summary or archive hashes.", ""])
        for artifact in group["artifacts"]:
            lines.extend([f"#### {artifact['id']} · {artifact['role']}", "", f"Original `{artifact['original_path']}`. Full-file SHA-256 `{artifact['original_sha256']}`. Projection SHA-256 `{artifact['projection_sha256']}`.", "", "```json", json.dumps(artifact["projection"], indent=2, sort_keys=True), "```", ""])
    lines.extend(["## Advance the consolidated snapshot", "", *[f"{i}. {step}" for i, step in enumerate(snapshot["future_update_procedure"], 1)], ""])
    return lines


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
    for group in data["consolidated_snapshot"]["groups"]:
        for outcome in group["outcomes"]:
            for proof in outcome["proofs"]:
                add(proof["source"], group["physical_source_sha"])
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
    def normalized_outcome(d):
        return d["consolidated_snapshot"]["groups"][0]["outcomes"][0]

    def selected_artifact(d):
        group = d["consolidated_snapshot"]["groups"][0]
        aid = normalized_outcome(d)["proofs"][0]["artifact_ids"][0]
        return next(a for a in group["artifacts"] if a["id"] == aid)

    def mutate_observation(d, action):
        artifact = selected_artifact(d)
        test = normalized_outcome(d)["proofs"][0]["test"]
        if action == "UNOBSERVED":
            artifact["projection"]["test_observations"] = []
        else:
            artifact["projection"]["test_observations"].append("--- " + action + ": " + test + " (0.01s)")
        artifact["projection_sha256"] = projection_hash(artifact["projection"])

    def changed_dependency(d):
        group = d["consolidated_snapshot"]["groups"][0]
        group["physical_source_sha"] = BASE
        group["physical_tree"] = git("rev-parse", BASE + "^{tree}")
        group["documentation_equivalence_allowlist"] = git("diff", "--name-only", BASE, group["accepted_sha"]).splitlines()

    supplemental_mutations = {
        "unknown supplemental ID": lambda d: normalized_outcome(d).update(criterion_id="R99-C99"),
        "deferred supplemental ID": lambda d: normalized_outcome(d).update(criterion_id="R54-C03"),
        "altered supplemental text": lambda d: normalized_outcome(d).update(outcome="changed"),
        "wrong accepted ancestry": lambda d: d["consolidated_snapshot"]["groups"][0].update(accepted_sha="c1f7b153a963609336851f94a16b2eac96f86e91"),
        "missing essential artifact": lambda d: d["consolidated_snapshot"]["groups"][0]["artifacts"].clear(),
        "essential artifact hash mismatch": lambda d: selected_artifact(d)["projection"].update(tampered=True),
        "failed named test": lambda d: mutate_observation(d, "FAIL"),
        "skipped named test": lambda d: mutate_observation(d, "SKIP"),
        "unobserved named test": lambda d: mutate_observation(d, "UNOBSERVED"),
        "changed product dependency under equivalence": changed_dependency,
        "duplicate supplemental group": lambda d: d["consolidated_snapshot"]["groups"].append(copy.deepcopy(d["consolidated_snapshot"]["groups"][0])),
        "partial-only credit": lambda d: next(o for g in d["consolidated_snapshot"]["groups"] for o in g["outcomes"] if o["criterion_id"] == "R29-C12").update(classification="validated"),
        "stale effective view": lambda d: d["consolidated_snapshot"]["effective_criteria"][0].update(credited=True),
        "changed original supplemental history": lambda d: d["supplemental_assessments"][0].update(note="rewritten"),
    }
    for name, mutate in supplemental_mutations.items():
        candidate = copy.deepcopy(data)
        mutate(candidate)
        try:
            validate_consolidated(candidate)
        except (ValueError, KeyError, subprocess.CalledProcessError):
            print(f"passed: reject {name}")
        else:
            raise ValueError(f"negative case accepted: {name}")
    repeated = copy.deepcopy(data)
    repeated["consolidated_snapshot"]["groups"].append(copy.deepcopy(repeated["consolidated_snapshot"]["groups"][0]))
    if sum(r["credited"] for r in effective_criteria(repeated)) != sum(r["credited"] for r in effective_criteria(data)):
        raise ValueError("repeated valid realization inflated numerator")
    print("passed: repeated valid realization leaves distinct criterion numerator unchanged")
    partial = next(r for r in effective_criteria(data) if r["criterion_id"] == "R29-C12")
    timeout = next(r for r in effective_criteria(data) if r["criterion_id"] == "R46-C06")
    if partial["credited"] or not timeout["credited"]:
        raise ValueError("partial-only or later-validated criterion scoring is wrong")
    print("passed: R29-C12 partial earns zero and later R46-C06 validation earns one")
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
        accepted = sum(r["credited"] for r in effective_criteria(data))
        print(f"passed: cumulative accepted bounded-profile coverage {accepted}/{len(eligible)} = {100 * accepted / len(eligible):.2f}%; cutoff {ACCEPTED_CUTOFF}")
        print(f"historical audit {BASE}: {verified}/{len(eligible)} = {100 * verified / len(eligible):.2f}%; {len(criteria)} definitions")
        print("Full parity requires all applicable R/A/matrix release gates; an atomic score alone is not acceptance.")
    except (ValueError, KeyError, TypeError, OSError, subprocess.CalledProcessError) as error:
        print(f"parity audit failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
