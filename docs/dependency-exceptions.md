# Dependency Exceptions

This file is the review register for top-level dependencies. New top-level Go or frontend dependencies must be added here with a narrow justification before `make check` should pass.

## Go

| Dependency | Scope | License | Justification |
| --- | --- | --- | --- |
| `github.com/aarondl/opt` | Generated store code | BSD-3-Clause | Nullable value helper emitted by Bob's generated schema metadata. It is checked in as part of the generated PostgreSQL store boundary and avoids hand-maintaining nullable column descriptors. |
| `github.com/getkin/kin-openapi` | Dev/check | MIT | Loads and validates `openapi.yaml` during the contract check so API route, auth, request body, and response metadata are derived from a real OpenAPI parser instead of ad hoc YAML scanning. |
| `github.com/google/go-cmp` | Dev/test | BSD-3-Clause | Used by generated Bob query tests for precise structural diffs in SQL/query output assertions. |
| `github.com/jackc/pgx/v5` | Runtime | MIT | PostgreSQL driver for the server database adapter. It is maintained, widely used in Go services, and avoids adding an ORM layer. |
| `github.com/jaswdr/faker/v2` | Generated test fixtures | MIT | Fixture data generator used by Bob-generated factories and query tests; it keeps generated store tests deterministic without adding application runtime behavior. |
| `github.com/lib/pq` | Runtime/store | MIT | Provides PostgreSQL array and driver value helpers used by the current PostgreSQL store and Bob-generated models while array handling is migrated behind generated adapters. |
| `github.com/stephenafamo/bob` | Runtime/store and codegen | MIT | Adopted SQL toolkit for PostgreSQL query builders, generated models, generated factories, and named queries behind the repository interfaces. |
| `github.com/stephenafamo/scan` | Runtime/store | MIT | Row scanning abstraction required by Bob-generated queries and the store wrappers that adapt generated query results back to domain structs. |
| `github.com/wasilibs/go-pgquery` | Generated query tests | Apache-2.0 | PostgreSQL parser used by Bob-generated query tests to compare SQL structure rather than brittle raw strings. |

## Frontend

The WebUI allows a narrow reviewed runtime surface for React and local shadcn/ui-style primitives. Components are copied into the repository and styled through NeroCD-owned design tokens; CDN imports and large pre-styled UI frameworks remain blocked.

| Dependency | Scope | License | Justification |
| --- | --- | --- | --- |
| `react` | Runtime | MIT | Component runtime for the WebUI so mobile/desktop shells, forms, tables, approvals, and log surfaces can be implemented as typed components instead of brittle HTML string templates. |
| `react-dom` | Runtime | MIT | Browser renderer for the React WebUI while preserving Vite static asset output for Go embedding. |
| `lucide-react` | Runtime | ISC | Small open icon set for operator actions and status affordances, replacing placeholder text glyphs and hand-rolled icons. |
| `radix-ui` | Runtime | MIT | Provides the accessible primitive foundation used by the selected shadcn/ui preset and generated local components. |
| `class-variance-authority` | Runtime | Apache-2.0 | Variant helper used by local shadcn/ui-style components for typed button and badge variants. |
| `clsx` | Runtime | MIT | Conditional class composition helper used by the local `cn` utility. |
| `cmdk` | Runtime | MIT | Accessible command menu primitive used by the local command palette for keyboard navigation across WebUI views and actions. |
| `tailwind-merge` | Runtime | MIT | Resolves conflicting Tailwind utility classes in local shadcn/ui-style components. |
| `tw-animate-css` | Runtime | MIT | CSS animation utilities imported by the selected shadcn/ui preset for component state transitions. |
| `sonner` | Runtime | MIT | Lightweight toast notification primitive used for mutation success/error feedback in the WebUI shell. |
| `@fontsource-variable/outfit` | Runtime | OFL-1.1 | Self-hosted variable sans font selected by the shadcn/ui preset, avoiding CDN font loading. |
| `@fontsource-variable/merriweather` | Runtime | OFL-1.1 | Self-hosted variable heading font selected by the shadcn/ui preset, avoiding CDN font loading. |
| `@fontsource-variable/geist` | Runtime | OFL-1.1 | Self-hosted variable interface font selected by the `b45BjM6Wn` shadcn/ui preset, avoiding CDN font loading. |
| `@playwright/test` | Dev/test | Apache-2.0 | Runs committed browser smoke coverage against the embedded Go-served WebUI so operator login/navigation regressions are caught in CI. |
| `tailwindcss` | Dev/build | MIT | Utility compiler used by shadcn/ui-style components and NeroCD design tokens. |
| `@tailwindcss/vite` | Dev/build | MIT | Vite integration for Tailwind CSS v4 during static WebUI builds. |
| `shadcn` | Dev/build | MIT | CLI used to apply the reviewed shadcn/ui preset and regenerate local UI component files. |
| `@vitejs/plugin-react` | Dev/build | MIT | Vite React transform plugin for JSX/TSX development and production builds. |
| `@types/react` | Dev/build | MIT | TypeScript declarations for React components. |
| `@types/react-dom` | Dev/build | MIT | TypeScript declarations for the React DOM renderer. |
| `@types/node` | Dev/build | MIT | TypeScript declarations needed by Vite/shadcn-compatible config and tooling. |
| `typescript` | Dev/build | Apache-2.0 | Type-checks the Vite WebUI and keeps the API client contract explicit. |
| `vite` | Dev/build | MIT | Minimal frontend build tool that emits static assets for Go embedding. |

## Policy

- Runtime frontend dependencies are blocked unless listed above with a narrow product justification.
- Package lifecycle scripts are blocked unless a dependency is explicitly trusted and documented here.
- CDN runtime imports are blocked; application assets must be built and embedded.
- Allowed licenses are `MIT`, `Apache-2.0`, `0BSD`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `OFL-1.1`, `CC-BY-4.0`, `MPL-2.0`, `Python-2.0`, and `BlueOak-1.0.0`. `OFL-1.1` is allowed for self-hosted fonts. `CC-BY-4.0` is allowed for browser compatibility data packages pulled by the frontend toolchain, not for application source libraries. `MPL-2.0` is allowed for CSS build tooling such as `lightningcss`, not for NeroCD application source. `Python-2.0`, `BlueOak-1.0.0`, and `0BSD` are allowed for CLI/toolchain transitive dependencies such as `argparse`, `isexe`, and `tslib`.
