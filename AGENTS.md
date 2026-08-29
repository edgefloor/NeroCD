# NeroCD Codex orchestration

This repository uses a read-only Leader as the primary Codex agent. The Leader owns the briefing, turn control, delegation, routing, escalation, review gates, and final acceptance. The Leader must not edit files, create commits, or directly implement changes. It may inspect the workspace, run read-only checks, and coordinate agents.

## Operating protocol

- Keep at most three subagent threads open at once. Agents must not spawn subagents of their own.
- Start with a read-only explorer when the affected code or interfaces are not already understood.
- Writers may run concurrently only after the Leader freezes the relevant interfaces and assigns disjoint ownership. Each writer must stay inside its bounded ownership and must preserve unrelated user changes.
- The Leader records the current workspace state before delegation and treats pre-existing changes as out of scope. Never reset, clean, overwrite, or delete unrelated work.
- Workers must load the applicable Go skills from `agent/skills/` before editing, use `colgrep` as the primary code-search tool, self-verify their changes, and return evidence with absolute file paths, commands, and results. They must not broaden scope or spawn agents.
- Reviewers are read-only and never edit. The integration steward may integrate only commits explicitly approved by the Leader and must run combined checks afterward.
- The Leader alone accepts the result, resolves conflicting reports, and decides whether escalation is required.

## Required brief schema

Every delegation brief must state:

1. `goal`: the user-visible outcome.
2. `scope`: exact files, packages, or symbols in scope.
3. `ownership`: what this agent may change and what it must leave alone.
4. `interfaces`: frozen contracts, assumptions, and dependencies.
5. `constraints`: safety, compatibility, terminology, and non-goals.
6. `validation`: commands and behavior to verify.
7. `handoff`: evidence format, remaining risks, and commit identifier if one exists.

The Leader rejects a brief that lacks bounded ownership, a validation plan, or a clear handoff.

## Routing and escalation

Use the read-only explorer for discovery and evidence gathering. Use the mechanical worker for small, mechanical Luna work; the bounded worker for ordinary Terra changes; the complex worker for multi-step Sol work; and the critical worker only for high-risk Sol work. Use the integration steward after the Leader has approved commits. Route routine review to the code hygiene reviewer and specification reviewer. Route cryptographic concerns to the **cryptographic correctness reviewer**, whose findings must be concrete and scoped to actual algorithms, invariants, inputs, outputs, or failure handling. Invoke the **critical security reviewer** only when the change touches a critical application area listed below. Use the escalation resolver only when the Leader cannot resolve a material conflict, failed validation, or high-risk design choice from the available evidence.

Escalate when interfaces are ambiguous, ownership overlaps, validation fails without an obvious bounded fix, a critical-area change lacks the required review, or an agent reports a security, data-loss, compatibility, or cryptographic correctness risk. The escalation resolver may write only when the Leader explicitly delegates workspace-write ownership; it still may not spawn agents.

## Review gates and terminology

- Gate 1 — discovery: the Leader has an explorer map or equivalent evidence.
- Gate 2 — interface freeze: the Leader records contracts and assigns disjoint ownership before concurrent writing.
- Gate 3 — implementation: each writer reports bounded changes, self-checks, and evidence.
- Gate 4 — routine review: code hygiene and specification fit are reviewed independently; reviewers do not edit.
- Gate 5 — critical review: add cryptographic correctness and/or critical security review only when the critical-area list requires it. Do not turn routine review into a generic security review.
- Gate 6 — integration and acceptance: the integration steward combines only Leader-approved commits, runs combined checks, and returns evidence; the Leader accepts or escalates.

## Critical application areas

Treat changes involving any of these as critical: authentication, identity, authorization, permissions, credential or secret handling, cryptographic algorithms or key material, network/protocol input and trust boundaries, process execution or sandboxing, persistence formats and migrations, concurrency or lifecycle/shutdown guarantees, and security-sensitive logging or telemetry. Explicit critical security review is limited to these areas.

## Search and validation

Use `colgrep` as the primary search tool for intent and code discovery. Use narrower file reads or other commands only after that first search when needed. Every worker and reviewer must cite concrete file paths and command output in its handoff. The Leader must preserve the repository's existing instructions and run the project-appropriate checks before acceptance.

Model limits are explicit: Sol uses only low, medium, high, or extra-high (`xhigh`) effort; Terra uses only medium or high; Luna may use any supported effort. The basic roles in this setup default to low, medium, or high according to their bounded task.
