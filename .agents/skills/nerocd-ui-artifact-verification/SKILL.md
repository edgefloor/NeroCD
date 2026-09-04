---
name: nerocd-ui-artifact-verification
description: Verify NeroCD WebUI changes across source tests, responsive browser behavior, the generated web/dist artifact, and Go embedding. Use when changing UI layout, navigation, frontend build output, or embedded static delivery.
---

# NeroCD UI artifact verification

Treat the frontend source, the built `web/dist` tree, and the Go-served embedded UI as distinct verification layers.

1. Identify the changed operator workflow and the viewport classes it affects. Preserve keyboard access, semantic roles, overflow containment, safe-area padding, and both mobile and desktop navigation behavior.
2. Run focused Vitest coverage from `web/app`, then the frontend build. The Vite build writes to `web/dist`; do not assume source changes are present in a Go binary built before that output exists.
3. Because `web/dist` is ignored, record a deterministic file-and-SHA-256 manifest (including `index.html`, hashed assets, and `.gitkeep`) rather than expecting an ordinary Git diff. Keep `web/assets.go` behavior in mind: it serves embedded `dist` when `dist/index.html` exists and falls back to `static` only when the built entrypoint is absent.
4. For responsive or end-to-end behavior, run the existing browser smoke path. Exercise the affected flow at 320, 390, and 430 pixel mobile widths when the change can alter the mobile shell, and retain a desktop assertion.
5. Build or test the Go server only after the frontend artifact is current, so the embedded bytes match the UI under review. When diagnosing a stale deployed UI, compare the built manifest with the exact deployed binary or container digest, hashes of served HTML/assets, and relevant cache headers before claiming the deployment contains the fix.

Useful evidence includes the focused test command, frontend build result, `web/dist` hash manifest, browser assertion or screenshot when layout matters, and Go build/test result. Mark Docker, browser, or deployed-environment checks `not_run` when those prerequisites are unavailable, and state what remains unverified. This skill does not authorize release, deployment, version changes, or unrelated artifact regeneration; deployed-state inspection must stay within the user's existing access and requested scope.

Repository anchors: `web/app/package.json`, `web/app/vite.config.ts`, `web/app/tests/browser/operator-workflow.spec.ts`, `web/app/scripts/browser-server.sh`, and `web/assets.go`.
