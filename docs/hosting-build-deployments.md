# Deployment admission from local build artifacts

`POST /api/v1/resources/{id}/deployments/from-build` admits a successful build on the resource's bound Docker destination. The operation is executed by an upgraded Go agent explicitly configured with `--local-build-deploy` and the matching `local_build_deploy_v1` capability. Build capability and registry image deployment capability do not imply local deployment capability. See [same-host runtime configuration and recovery](hosting-local-build-runtime.md).

The request contains `build_id`, `expected_source_generation`, `expected_generation`, `idempotency_key`, `container_port`, optional `host_port`, and `readiness` (`path`, `timeout_seconds`). It cannot select an artifact, daemon, destination, registry image or policy. Port allocation and HTTP readiness limits match image deployments. The response is the existing HTTP 202 hosting operation document; the resource deployment history and operation read endpoints also return these operations.

Admission requires current project maintainer authority, current resource and Git source generations, a successful exact build artifact, active destination grant and enrollment, the same runner and daemon, and the new deployment capability. The operation, immutable artifact association, loopback port claim, audit event and existing River operation-ID job commit together. Idempotent replay still requires current authorization and the exact current artifact. A changed request under the same key conflicts.

Local snapshots append `local_build` containing source generation, artifact hash and exact artifact. Releases append `local_build` containing the artifact. Their registry image strings are empty. Image ID, OCI config digest, manifest digest and index digest remain distinct identities. The common image/platform/daemon fields must match the admitted artifact exactly. Registry snapshots and releases keep their original JSON and hash bytes when these fields are absent.

New application authority rechecks current resource/source generations and authorization at dispatch, claim, preparation, apply admission and first settlement. A source change after possible workload mutation cannot silently release ownership: the operation retains its mutation hold when current authority is lost. Previously committed receipt replay is acknowledgment only. Observation and explicitly authorized retained cleanup can inspect older source versions, with current requester authority, exact placement, the new capability and existing container and route protections. They cannot rebuild, reapply or clear restored ownership holds. Existing observation attempts retain their original requester attribution. A different owner receives a conflict rather than taking over that observation identity; attribution recovery requires a later explicit recovery workflow.

Runtime execution reads the exact admitted local ImageID with no pull fallback, validates daemon and platform before effects, and uses the existing fenced operation lifecycle. Local and nested retained evidence requires journal format 6, which older binaries reject. This does not provide image transfer, automatic routing from a successful build alone, or automatic traffic switching. A proxy backend still requires a settled healthy release. Retained cleanup never deletes images and cannot remove a release referenced by a route.

Migration 0050 is additive. It binds operation-to-build artifact identity with a composite foreign key, deferred snapshot/release consistency, immutable evidence, schema-bound predicates, and typed local lease admission. Startup requires both the existing build/Git predicates and the new deployment predicate. Restored bindings retain the existing durable admission hold; this feature adds no unlock or restore-resumption shortcut.

## Deploy through the resource UI

The resource UI supports public and granted private HTTPS Git builds on one host. Configure the bound destination, active project grant and enrolled runner before requesting a build. The agent needs the operator-owned build configuration, the `local_build_deploy_v1` capability and `--local-build-deploy`. All three refer to the same runner and Docker daemon. The UI cannot enable those prerequisites or transfer an image to another host.

1. Open the application's **Public Git builds** or **Private HTTPS Git builds** section. Request a build for the reviewed saved source and generations, or select an existing successful build with **View build**.
2. Review the recorded source commit and local artifact image ID. A successful build records an artifact. It does not establish a running application.
3. In **Deploy recorded local build**, choose the container port and ordinary HTTP readiness path and timeout. Prefer automatic per-release host port allocation. A fixed port can interrupt the current release before replacement readiness succeeds.
4. Acknowledge the exact build and settings, then select **Deploy selected build**. The request identifies the build and current generations. The server selects the immutable artifact and current authorized placement.
5. Review the shared deployment operation detail and deployment history. The detail identifies the local build, source generation, artifact hash and image ID. Wait for a succeeded operation with recorded readiness, no uncertainty and no mutation or restore hold.
6. In **Resource routes**, select that settled healthy deployment and configure a hostname permitted by the existing managed proxy. Save the desired route. Inspect its recorded HTTP readiness, trusted HTTPS readiness and certificate status before treating the route as ready. Deployment alone does not configure or switch HTTPS routing.

If delivery is uncertain, retry the identical local deployment request. The UI retains its build ID, generations, ports, readiness settings and idempotency key. Editing the submitted settings is paused. A generation or placement conflict requires explicit review. **Review this exact build for a separate request** checks the same build against current resource and source metadata, then requires a new acknowledgment and request key. It never rebuilds, chooses another build or deploys automatically. If the source generation changed, select a successful build for the current source instead.

A restore hold blocks admission until trusted recovery evidence exists. Refresh, reconciliation and a new request key cannot clear that hold. Permission denial pauses mutations. Reload after project access is restored. Actor, resource and permission changes discard pending UI state and ignore late mutation responses. Viewers can inspect build artifacts and deployment evidence but cannot deploy or change routes.

The opt-in real backend browser check is `NEROCD_LOCAL_BUILD_BROWSER=1 bash acceptance/runtime-hosting-local-build/gate.sh`, run from the repository root after the frontend build. It uses a disposable Docker daemon, actual CLI build and local deployment, real authenticated browser sessions and the existing test-only managed proxy CA. It checks keyboard submission, identical receipt recovery, viewer denial and layouts at 320, 390, 430 and 1440 pixels. It does not seed a successful build. The default public runtime gate remains unchanged. The separate private browser
mode is described below.

## Deploy a private Git artifact

First follow [private source grant and build selection](hosting-git-resources.md#request-a-private-https-git-build)
and the [operator private-execution setup](hosting-build-runtime.md#enable-private-https-git).
A successful private build uses the same exact-artifact deployment controls and
route review described above. It does not pass a credential to the application,
change the selected destination, rebuild at deployment time or select a route
automatically. Configure application variables and secret references separately
in the application's **Runtime configuration** section.

Run `NEROCD_PRIVATE_GIT_BROWSER=1 bash acceptance/runtime-hosting-local-build/gate.sh`
for the Dockerfile browser workflow. Add `NEROCD_PRIVATE_GIT_STRATEGY=railpack`
for the Railpack variant. The [fixture guide](../acceptance/runtime-hosting-local-build/README.md)
covers the compiled Vite SPA variant, exact grant retry, typed output, artifact
deployment, keyboard controls, responsive layouts, and viewer denial. HTTPS
checks use a local test CA. Production ACME is outside this fixture.
