import { act, cleanup, render } from "@testing-library/react";
import {
  QueryClient,
  QueryClientProvider,
  type QueryKey,
} from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { databaseDenied } from "@/api/hosting-databases";
import { ApiError } from "@/api/errors";
import { useQueryAccessDenial } from "./useQueryAccessDenial";

let client: QueryClient;
afterEach(() => {
  cleanup();
  client?.clear();
});

function Boundary({
  keys,
  onDenied,
  principal,
}: {
  keys: QueryKey[];
  onDenied: () => void;
  principal?: { key: QueryKey; actor: string };
}) {
  useQueryAccessDenial(keys, onDenied, principal);
  return null;
}
function setup(keys: QueryKey[], principal?: { key: QueryKey; actor: string }) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onDenied = vi.fn();
  const view = render(
    <QueryClientProvider client={client}>
      <Boundary keys={keys} onDenied={onDenied} principal={principal} />
    </QueryClientProvider>,
  );
  return { onDenied, ...view };
}
async function fail(key: QueryKey, status: number) {
  await client
    .fetchQuery({
      queryKey: key,
      queryFn: async () => {
        throw new ApiError(status, "Unavailable", "request");
      },
    })
    .catch(() => undefined);
}

test("a denied read retires authority before a late success can replace the error", async () => {
  const key = ["resource", "actor", "project"];
  const view = setup([key]);
  await act(async () => {
    await fail(key, 403);
    expect(view.onDenied).toHaveBeenCalledTimes(1);
    client.setQueryData(key, { generation: 2 });
    await fail(key, 401);
  });
  expect(view.onDenied).toHaveBeenCalledTimes(1);
});

test("unrelated scopes and transient read failures do not retire authority", async () => {
  const key = ["resource", "actor", "project"];
  const view = setup([key]);
  await act(async () => {
    await fail(["resource", "other-actor", "project"], 403);
    await fail(key, 503);
  });
  expect(view.onDenied).not.toHaveBeenCalled();
  await act(async () => fail(key, 404));
  expect(view.onDenied).toHaveBeenCalledTimes(1);
});

test("a cached denial is checked when a boundary mounts and removed queries do not reenter", async () => {
  const key = ["source-grants", "actor"];
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await fail(key, 403);
  const onDenied = vi.fn(() => client.removeQueries({ queryKey: key }));
  render(
    <QueryClientProvider client={client}>
      <Boundary keys={[key]} onDenied={onDenied} />
    </QueryClientProvider>,
  );
  expect(onDenied).toHaveBeenCalledTimes(1);
});

test("principal replacement retires the original actor even when the new read succeeds", async () => {
  const key = ["principal"];
  const view = setup([key], { key, actor: JSON.stringify(["oidc", "first"]) });
  act(() => client.setQueryData(key, { provider: "oidc", id: "first" }));
  expect(view.onDenied).not.toHaveBeenCalled();
  act(() => client.setQueryData(key, { provider: "oidc", id: "second" }));
  expect(view.onDenied).toHaveBeenCalledTimes(1);
});

test("retired component scopes no longer observe query completion", async () => {
  const key = ["resource", "actor"];
  const view = setup([key]);
  view.unmount();
  await act(async () => fail(key, 403));
  expect(view.onDenied).not.toHaveBeenCalled();
});

test("database absence remains unavailable metadata instead of revoking project access", async () => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onDenied = vi.fn();
  function DatabaseBoundary() {
    useQueryAccessDenial(
      [["database", "project"]],
      onDenied,
      undefined,
      databaseDenied,
    );
    return null;
  }
  render(
    <QueryClientProvider client={client}>
      <DatabaseBoundary />
    </QueryClientProvider>,
  );
  await act(async () => fail(["database", "project"], 404));
  expect(onDenied).not.toHaveBeenCalled();
  await act(async () => fail(["database", "project"], 403));
  expect(onDenied).toHaveBeenCalledTimes(1);
});
