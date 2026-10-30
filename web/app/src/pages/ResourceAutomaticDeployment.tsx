import {
  automaticDeploymentAvailability,
  type AutomaticDeploymentConfiguration,
  type AutomaticDeploymentOptions,
  type AutomaticDeploymentStatus,
} from "@/api/hosting-github-push";
import { Button } from "@/components/ui/button";
import { useId } from "react";

export type AutomaticDeploymentDraft = {
  mode: "build_only" | "automatic_unprotected";
  routeIDs: string[];
  baselineKey: string;
};

export function ResourceAutomaticDeployment(props: {
  options?: AutomaticDeploymentOptions;
  saved?: AutomaticDeploymentConfiguration;
  draft: AutomaticDeploymentDraft;
  locked: boolean;
  onChange: (draft: AutomaticDeploymentDraft) => void;
}) {
  const viewModel = useResourceAutomaticDeployment(props);
  const { options, saved, draft, locked, onChange, id, baseline, baselineKey } =
    viewModel;
  return (
    <fieldset
      className="min-w-0 space-y-3 rounded border p-3"
      disabled={locked}
    >
      <legend className="font-medium">Deployment after a GitHub push</legend>
      <label className="flex items-start gap-2 text-sm">
        <input
          type="radio"
          name={`${id}-mode`}
          checked={draft.mode === "build_only"}
          onChange={() =>
            onChange({ mode: "build_only", routeIDs: [], baselineKey: "" })
          }
        />
        <span>Build only. Deploy each successful build manually.</span>
      </label>
      <label className="flex items-start gap-2 text-sm">
        <input
          type="radio"
          name={`${id}-mode`}
          checked={draft.mode === "automatic_unprotected"}
          disabled={!baseline && draft.mode !== "automatic_unprotected"}
          onChange={() =>
            onChange({
              mode: "automatic_unprotected",
              routeIDs: [],
              baselineKey,
            })
          }
        />
        <span>Automatic deployment without approval</span>
      </label>
      <p className="text-sm">
        Automatic deployment has no approval step. It does not configure or
        enforce GitHub branch protection. Only enable it for a trusted
        repository and branch.
      </p>
      {saved ? (
        <p className="text-sm">
          Saved policy: automatic deployment without approval, with{" "}
          {saved.routes.length} reviewed HTTPS{" "}
          {saved.routes.length === 1 ? "route" : "routes"}. Changing this mode
          requires an explicit configuration decision.
        </p>
      ) : (
        <p className="text-xs">
          Build only is the default. Saving an automatic configuration still
          disables the policy until verification and explicit enablement.
        </p>
      )}
      {!baseline ? (
        <p role="status" className="text-sm">
          {options
            ? automaticDeploymentAvailability[options.reason]
            : "Fresh owner deployment options are unavailable. Refresh build-on-push metadata before a new automatic deployment review."}
          {saved ? " The saved automatic configuration is retained." : ""}
        </p>
      ) : null}
      <AutomaticDeploymentBaseline {...viewModel} />
    </fieldset>
  );
}

const stateText: Record<AutomaticDeploymentStatus["state"], string> = {
  idle: "Waiting for a qualifying push",
  waiting_build: "Waiting for the recorded build",
  waiting_deployment: "Waiting for the recorded deployment",
  waiting_switch: "Waiting for the recorded HTTPS switch",
  succeeded: "Recorded HTTPS switch succeeded",
  failed: "Recorded automatic deployment failed",
  blocked: "Automatic deployment is blocked",
  expired: "Automatic deployment window expired",
};
const reasonText: Record<string, string> = {
  awaiting_push: "No automatic deployment demand has been recorded yet.",
  build_queued: "The push queued a build for this automatic deployment.",
  build_pending: "The build has not completed.",
  build_failed: "The build failed. A failed build is not a deployed release.",
  deployment_queued: "The recorded build was admitted for deployment.",
  deployment_pending:
    "Deployment has not completed; the HTTPS switch is not yet confirmed.",
  deployment_failed:
    "Deployment failed. Review the recorded operation before another decision.",
  deployment_hold:
    "The deployment has an unresolved hold. This panel cannot release it.",
  switch_queued: "A switch was admitted for the reviewed routes.",
  switch_pending: "The HTTPS switch is not yet confirmed.",
  switch_hold:
    "The switch has an unresolved hold. This panel cannot release it.",
  switch_failed:
    "The switch failed. Do not assume the new release is receiving traffic.",
  https_verified:
    "The recorded operation verified the selected routes at the observed loaded generation.",
  authority_changed:
    "Authority changed; the accepted automatic deployment cannot continue.",
  review_invalidated:
    "The saved deployment review is no longer current. Review the application's manual deployment and route state.",
  deadline_expired: "The original automatic deployment window expired.",
  budget_exhausted: "The automatic deployment attempt budget is exhausted.",
  artifact_mismatch:
    "The build artifact does not match the accepted deployment evidence.",
  route_drift:
    "A reviewed route changed. Automatic deployment cannot substitute a different route baseline.",
  admission_deferred:
    "Admission is deferred. The durable controller retains its original bounded demand.",
};

export function AutomaticDeploymentSummary({
  status,
}: {
  status: AutomaticDeploymentStatus;
}) {
  return (
    <section
      className="min-w-0 space-y-2 rounded border p-3"
      aria-label="Recorded automatic deployment"
    >
      <h6 className="font-medium">Automatic deployment without approval</h6>
      <p className="text-sm">{stateText[status.state]}</p>
      <p className="text-sm">
        {reasonText[status.reason] ??
          "An unrecognized outcome was recorded. Refresh and inspect the deployment and switch operations before another decision."}
      </p>
      <p className="text-xs">
        This is the last operation result. Check runtime status for current health.
        Deployment completion and HTTPS switch completion are separate outcomes.
      </p>
      <p className="text-xs">
        An operation identity alone does not prove that deployment or switching
        started.
      </p>
      <dl className="grid min-w-0 grid-cols-1 gap-1 break-all text-sm">
        <dt>Desired commit</dt>
        <dd>{status.desired_commit || "No commit recorded"}</dd>
        <dt>Build operation</dt>
        <dd>{status.build_id || "Not admitted"}</dd>
        <dt>Deployment operation</dt>
        <dd>{status.deployment_operation_id || "Not admitted"}</dd>
        <dt>HTTPS switch operation</dt>
        <dd>{status.switch_operation_id || "Not admitted"}</dd>
        <dt>Observed loaded generation</dt>
        <dd>
          {status.observed_loaded_generation || "No verified switch recorded"}
        </dd>
        <dt>Automatic demand attempts</dt>
        <dd>
          {status.attempts}/32
          {status.expires_at ? `; expires ${status.expires_at}` : ""}
        </dd>
      </dl>
      <p className="text-xs">
        Retrying a push delivery does not reset this automatic deployment's
        attempt budget or original expiry. There is no approval, generic unlock
        or reset control here.
      </p>
    </section>
  );
}

function useResourceAutomaticDeployment({
  options,
  saved,
  draft,
  locked,
  onChange,
}: {
  options?: AutomaticDeploymentOptions;
  saved?: AutomaticDeploymentConfiguration;
  draft: AutomaticDeploymentDraft;
  locked: boolean;
  onChange: (draft: AutomaticDeploymentDraft) => void;
}) {
  const id = useId();
  const baseline = options?.available ? options.baseline : undefined;
  const baselineKey = baseline ? JSON.stringify(baseline) : "";
  const stale = Boolean(baseline && draft.baselineKey !== baselineKey);
  const selected = new Set(draft.routeIDs);
  return {
    options,
    saved,
    draft,
    locked,
    onChange,
    id,
    baseline,
    baselineKey,
    stale,
    selected,
  };
}

type AutomaticDeploymentBaselineModel = Pick<
  ReturnType<typeof useResourceAutomaticDeployment>,
  "draft" | "baseline" | "stale" | "onChange" | "baselineKey" | "selected"
>;
function AutomaticDeploymentBaseline(
  viewModel: AutomaticDeploymentBaselineModel,
) {
  const { draft, baseline, stale, onChange, baselineKey, selected } = viewModel;

  if (!(draft.mode === "automatic_unprotected" && baseline)) return null;
  return (
    <>
      <dl
        className="grid min-w-0 grid-cols-1 gap-1 break-all text-sm"
        aria-label="Current automatic deployment baseline"
      >
        <dt>Healthy release settings</dt>
        <dd>
          Container port {baseline.container_port}; readiness{" "}
          {baseline.readiness.path}, timeout{" "}
          {baseline.readiness.timeout_seconds} seconds
        </dd>
        <dt>Runtime configuration</dt>
        <dd>Generation {baseline.runtime_configuration_generation}</dd>
        <dt>Loaded HTTPS proxy</dt>
        <dd>Generation {baseline.loaded_generation}</dd>
      </dl>
      <p className="text-xs">
        These settings come from the healthy manual deployment and cannot be
        edited here. To change the port, readiness or runtime configuration,
        complete a new healthy manual deployment first.
      </p>
      {stale ? (
        <div className="space-y-2">
          <p role="alert" className="text-sm">
            The deployment baseline changed. Review it again and select routes
            before saving a new configuration.
          </p>
          <Button
            className="h-auto max-w-full whitespace-normal"
            type="button"
            variant="outline"
            onClick={() =>
              onChange({
                mode: "automatic_unprotected",
                routeIDs: [],
                baselineKey,
              })
            }
          >
            Review current automatic deployment baseline
          </Button>
        </div>
      ) : (
        <fieldset className="min-w-0 space-y-2">
          <legend className="text-sm font-medium">
            Choose existing HTTPS routes for automatic switching
          </legend>
          {baseline.routes.map((route) => (
            <label
              key={route.route_id}
              className="flex min-w-0 items-start gap-2 break-all text-sm"
            >
              <input
                type="checkbox"
                checked={selected.has(route.route_id)}
                onChange={(event) =>
                  onChange({
                    ...draft,
                    routeIDs: event.target.checked
                      ? [...draft.routeIDs, route.route_id].sort()
                      : draft.routeIDs.filter(
                          (value) => value !== route.route_id,
                        ),
                  })
                }
              />
              <span>
                https://{route.hostname}
                {route.path_prefix}
              </span>
            </label>
          ))}
          <p className="text-xs">
            Select 1 to 32 routes. New releases switch only these reviewed
            routes after the build and deployment succeed.
          </p>
        </fieldset>
      )}
    </>
  );
}
