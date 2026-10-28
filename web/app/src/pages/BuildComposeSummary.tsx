import {
  composeServiceArtifact,
  type HostingBuild,
} from "@/api/hosting-builds";
export function BuildComposeSummary({ build }: { build: HostingBuild }) {
  const result = build.compose_result;
  if (!result) return null;
  return (
    <section
      aria-label="Prepared Compose result"
      className="min-w-0 space-y-2 rounded border p-3 text-sm"
    >
      <h6 className="font-medium">Prepared Compose result</h6>
      <dl className="space-y-1 break-all">
        <dt>Compose file in repository</dt>
        <dd>
          {[
            build.snapshot.source.settings.context_dir,
            build.snapshot.source.settings.compose_file,
          ]
            .filter(Boolean)
            .join("/")}
        </dd>
        <dt>Prepared result hash</dt>
        <dd>{result.hash}</dd>
        <dt>Public Compose service</dt>
        <dd>
          {result.source.public_component} · container port{" "}
          {result.source.container_port}
        </dd>
        <dt>Persistent named volumes</dt>
        <dd>{result.definition.volumes.join(", ") || "None"}</dd>
      </dl>
      <ul
        aria-label="Prepared Compose services"
        className="space-y-2 break-all"
      >
        {result.definition.services.map((service) => {
          const built = composeServiceArtifact(result, service.name);
          return (
            <li key={service.name}>
              <p>
                {service.name}:{" "}
                {built
                  ? built.owner === service.name
                    ? "Built from this Git commit"
                    : `Uses the image built for ${built.owner}`
                  : "Public registry image"}
              </p>
              <p>{built ? built.artifact.image_id : service.image}</p>
              {service.build ? (
                <p>
                  Build context: {service.build.context || "repository root"} ·
                  Dockerfile: {service.build.dockerfile}
                  {service.build.target
                    ? ` · target ${service.build.target}`
                    : ""}
                </p>
              ) : null}
              {service.build?.args && Object.keys(service.build.args).length ? <p>Resolved build arguments: {Object.entries(service.build.args).sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0).map(([name, value]) => `${name}=${JSON.stringify(value)}`).join(", ")}</p> : null}
              {built ? <p>Platform: {built.artifact.platform}</p> : null}
              {service.mounts?.length ? (
                <p>
                  Data mounts:{" "}
                  {service.mounts
                    .map((mount) => `${mount.volume} → ${mount.target}`)
                    .join(", ")}
                </p>
              ) : null}
            </li>
          );
        })}
      </ul>
      <details>
        <summary>Prepared Compose source</summary>
        <pre className="max-h-96 max-w-full overflow-auto whitespace-pre-wrap break-all rounded border p-2">
          {result.source.original}
        </pre>
      </details>
      <p>
        This build prepared the stack and its local images. Deployment,
        readiness and public traffic are separate recorded outcomes.
      </p>
    </section>
  );
}
