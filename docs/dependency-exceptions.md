# Dependency Exceptions

This file is the review register for top-level dependencies. New top-level Go or frontend dependencies must be added here with a narrow justification before `make check` should pass.

## Go

| Dependency | Scope | License | Justification |
| --- | --- | --- | --- |
| `github.com/coreos/go-oidc/v3` | Runtime/auth | Apache-2.0 | Performs standards-based OIDC discovery and ID-token signature, issuer, audience, and expiry verification for the explicitly configured enterprise provider. Version 3.21.0 is pinned for the Go 1.25 toolchain. |
| `github.com/getkin/kin-openapi` | Dev/check | MIT | Loads and validates `openapi.yaml` during the contract check so API route, auth, request body, and response metadata are derived from a real OpenAPI parser instead of ad hoc YAML scanning. |
| `github.com/jackc/pgx/v5` | Runtime/store | MIT | Native PostgreSQL protocol, pooling, transactions, arrays, nullable values, and JSON transport for the repository implementation without an ORM or compatibility adapter. |
| `github.com/sqlc-dev/sqlc` | Dev/code generation | MIT | Version 1.24.0 is pinned as a Go tool and runs inside the digest-pinned Go 1.25.7 Debian image declared in the Makefile. The supported Linux compiler environment makes checked-in pgx query generation portable and deterministic without making sqlc a runtime dependency or domain-model source. |
| `golang.org/x/oauth2` | Runtime/auth | BSD-3-Clause | Implements the authorization-code exchange and S256 PKCE request parameters used with the verified OIDC provider; no OAuth access or refresh token is persisted. Version 0.36.0 is pinned. |
| `golang.org/x/sys` | Runtime/runner | BSD-3-Clause | Provides the narrow Unix `openat`, `O_NOFOLLOW`, ownership/mode inspection, atomic rename, and directory fsync primitives required for a symlink-resistant, crash-consistent runner journal without storing bearer credentials. |

## Frontend

The WebUI allows a narrow reviewed runtime surface for React and local shadcn/ui-style primitives. Components are copied into the repository and styled through NeroCD-owned design tokens; CDN imports and large pre-styled UI frameworks remain blocked.

| Dependency | Scope | License | Justification |
| --- | --- | --- | --- |
| `react` | Runtime | MIT | Component runtime for the WebUI so mobile/desktop shells, forms, tables, approvals, and log surfaces can be implemented as typed components instead of brittle HTML string templates. |
| `react-dom` | Runtime | MIT | Browser renderer for the React WebUI while preserving Vite static asset output for Go embedding. |
| `@tanstack/react-router` | Runtime | MIT | Client-only file-based route matching and browser-history navigation for the static Go-served WebUI; no Start, SSR, or server runtime is included. |
| `@tanstack/react-query` | Runtime | MIT | Owns in-memory browser remote state with abortable generated-client requests, bounded retries, route-local cache entries, and targeted mutation invalidation; no persistence plugin or browser storage is used. |
| `lucide-react` | Runtime | ISC | Small open icon set for operator actions and status affordances, replacing placeholder text glyphs and hand-rolled icons. |
| `@radix-ui/react-dialog` | Runtime | MIT | Provides the owned modal primitive's focus trap, Escape handling, and accessible dialog semantics without pulling in the Radix umbrella package. |
| `@radix-ui/react-slot` | Runtime | MIT | Supports the local button and badge `asChild` composition API without pulling in the Radix umbrella package. |
| `class-variance-authority` | Runtime | Apache-2.0 | Variant helper used by local shadcn/ui-style components for typed button and badge variants. |
| `clsx` | Runtime | MIT | Conditional class composition helper used by the local `cn` utility. |
| `cmdk` | Runtime | MIT | Accessible command menu primitive used by the local command palette for keyboard navigation across WebUI views and actions. |
| `tailwind-merge` | Runtime | MIT | Resolves conflicting Tailwind utility classes in local shadcn/ui-style components. |
| `tw-animate-css` | Runtime | MIT | CSS animation utilities imported by the selected shadcn/ui preset for component state transitions. |
| `sonner` | Runtime | MIT | Lightweight toast notification primitive used for mutation success/error feedback in the WebUI shell. |
| `@fontsource-variable/geist` | Runtime | OFL-1.1 | Self-hosted variable interface font selected by the `b45BjM6Wn` shadcn/ui preset, avoiding CDN font loading. |
| `@playwright/test` | Dev/test | Apache-2.0 | Runs committed browser smoke coverage against the embedded Go-served WebUI so operator login/navigation regressions are caught in CI. |
| `openapi-typescript` | Dev/code generation | MIT | Generates the checked-in frontend API contract from repository-root `openapi.yaml`, eliminating a handwritten duplicate transport model. |
| `vitest` | Dev/test | MIT | Version 4.1.11 runs TypeScript unit and component tests in the Vite 8-compatible frontend test environment. |
| `jsdom` | Dev/test | MIT | Provides the browser DOM implementation required for frontend component behavior tests. |
| `@testing-library/react` | Dev/test | MIT | Exercises rendered React component behavior through accessible user-facing DOM interactions. |
| `@testing-library/user-event` | Dev/test | MIT | Simulates realistic keyboard and pointer interactions in component tests. |
| `tailwindcss` | Dev/build | MIT | Utility compiler used by shadcn/ui-style components and NeroCD design tokens. |
| `@tailwindcss/vite` | Dev/build | MIT | Vite integration for Tailwind CSS v4 during static WebUI builds. |
| `@tanstack/router-plugin` | Dev/build | MIT | Version 1.168.34 deterministically generates the checked-in client route tree from reviewed file routes during Vite 8 builds. |
| `@vitejs/plugin-react` | Dev/build | MIT | Version 5.2.0 provides the Vite 8-compatible React transform for JSX/TSX development and production builds. |
| `@types/react` | Dev/build | MIT | TypeScript declarations for React components. |
| `@types/react-dom` | Dev/build | MIT | TypeScript declarations for the React DOM renderer. |
| `@types/node` | Dev/build | MIT | TypeScript declarations needed by Vite/shadcn-compatible config and tooling. |
| `typescript` | Dev/build | Apache-2.0 | Type-checks the Vite WebUI and keeps the API client contract explicit. |
| `vite` | Dev/build | MIT | Version 8.2.2 emits static assets for Go embedding without adding an SSR or Node runtime. |

## Policy

- Runtime frontend dependencies are blocked unless listed above with a narrow product justification.
- The `radix-ui` umbrella package, `@fontsource-variable/outfit`, `@fontsource-variable/merriweather`, TanStack Start, Axios, Redux, and Zustand are forbidden unless a later architectural decision explicitly changes this policy.
- Package lifecycle scripts are blocked unless a dependency is explicitly trusted and documented here.
- CDN runtime imports are blocked; application assets must be built and embedded.
- Allowed licenses are `MIT`, `MIT-0`, `Apache-2.0`, `0BSD`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `OFL-1.1`, `CC-BY-4.0`, `MPL-2.0`, `Python-2.0`, `BlueOak-1.0.0`, and `Unlicense`. `MIT-0` is allowed for the CSS parsing helper pulled transitively by the reviewed jsdom test environment. `OFL-1.1` is allowed for self-hosted fonts. `CC-BY-4.0` is allowed for browser compatibility data packages pulled by the frontend toolchain, not for application source libraries. `MPL-2.0` is allowed for CSS build tooling such as `lightningcss`, not for NeroCD application source. `Python-2.0`, `BlueOak-1.0.0`, and `0BSD` are allowed for CLI/toolchain transitive dependencies such as `argparse`, `isexe`, and `tslib`. `Unlicense` is allowed only for the router plugin's transitive `isbot` browser-detection utility.
