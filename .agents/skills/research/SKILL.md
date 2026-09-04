---
name: research
description: Investigate a bounded question against high-trust primary sources and capture evidence-backed findings in a repository Markdown file. Invoke only when the user explicitly requests $research.
---

Research the question directly; this skill does not authorize delegation or background agents.

1. Restate the exact question, output path, time horizon, and material exclusions. Ask only when an unresolved choice would change the result.
2. Prefer primary sources: official documentation, specifications, source code, first-party APIs, and original papers. Use secondary sources only to locate or contextualize primary evidence, and label that use.
3. Record each material claim with a direct citation near the claim. Separate sourced facts, reasonable inferences, unresolved uncertainty, and recommendations.
4. Write one focused Markdown report at the requested path or the repository's established research-notes location. Do not modify product code or unrelated documentation.
5. Conclude with the answer, evidence limits, and any decisions that still require the user.

Keep the investigation within the user's stated scope and authorization. This procedure does not grant permission to mutate external systems, install dependencies, change orchestration settings, or widen the requested deliverable.
