import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { ComposeStorageBackups } from "./ComposeStorageBackups";
const scope = {
  actor: "u",
  project: "p",
  resource: "r",
  binding: "b",
  destination: "d",
};
const evidence = {
  resource_id: "r",
  resource_generation: 2,
  operation_id: "deploy",
  available: true,
  volumes: [
    {
      volume: "uploads",
      eligible: true,
      mounts: [{ component: "web", mount_path: "/files", read_only: false }],
    },
  ],
};
const backup = {
  id: "backup",
  project_id: "p",
  resource_id: "r",
  operation_id: "op",
  state: "queued",
  created_by: "u",
  created_at: "2026-10-27T00:00:00Z",
  volume: "uploads",
  source_deployed_operation_id: "deploy",
};
let client: QueryClient;
afterEach(() => {
  cleanup();
  client?.clear();
  vi.unstubAllGlobals();
});
function setup(
  handler?: (
    u: string,
    o: RequestInit,
  ) => Response | Promise<Response> | undefined,
  owner = true,
) {
  const fetch = vi.fn(
    async (u: string, o: RequestInit) =>
      (await handler?.(u, o)) ??
      new Response(
        JSON.stringify(
          u.endsWith("storage-backup-volumes")
            ? evidence
            : o.method === "POST"
              ? backup
              : u.includes("?limit=")
                ? { items: [], limit: 20, offset: 0, count: 0, total: 0 }
                : backup,
        ),
      ),
  );
  vi.stubGlobal("fetch", fetch);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onDenied = vi.fn();
  const view = render(
    <QueryClientProvider client={client}>
      <ComposeStorageBackups
        scope={scope}
        generation={2}
        owner={owner}
        blocked={false}
        onDenied={onDenied}
      />
    </QueryClientProvider>,
  );
  return { fetch, onDenied, view };
}
async function review() {
  await screen.findByRole("option", { name: "uploads" });
  fireEvent.change(screen.getByRole("combobox"), {
    target: { value: "uploads" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Review Compose volume backup" }),
  );
  await screen.findByRole("region", { name: "Reviewed Compose volume backup" });
}
test("fresh review requires interruption consent before sending", async () => {
  const { fetch } = setup();
  await review();
  const submit = screen.getByRole<HTMLButtonElement>("button", {
    name: "Confirm Compose volume backup",
  });
  expect(submit.disabled).toBe(true);
  expect(fetch.mock.calls.filter(([, o]) => o.method === "POST")).toHaveLength(
    0,
  );
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(submit);
  await screen.findByText(/Volume backup backup/);
  expect(
    JSON.parse(
      String(fetch.mock.calls.find(([, o]) => o.method === "POST")![1].body),
    ),
  ).toMatchObject({
    volume: "uploads",
    expected_resource_generation: 2,
    expected_deployed_operation_id: "deploy",
    acknowledge_interruption: true,
  });
});
test("lost response retries identical body", async () => {
  let n = 0;
  const { fetch } = setup((_u, o) =>
    o.method === "POST" && ++n === 1
      ? Promise.reject(Error("lost"))
      : undefined,
  );
  await review();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(
    screen.getByRole("button", { name: "Confirm Compose volume backup" }),
  );
  await screen.findByRole("alert");
  fireEvent.click(
    screen.getByRole("button", {
      name: "Retry identical Compose backup request",
    }),
  );
  await screen.findByText(/Volume backup backup/);
  const writes = fetch.mock.calls.filter(([, o]) => o.method === "POST");
  expect(writes).toHaveLength(2);
  expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("fresh generation mismatch blocks submission", async () => {
  let n = 0;
  const { fetch } = setup((u) =>
    u.endsWith("storage-backup-volumes")
      ? new Response(
          JSON.stringify({
            ...evidence,
            resource_generation: ++n === 1 ? 2 : 3,
          }),
        )
      : undefined,
  );
  await screen.findByRole("option", { name: "uploads" });
  fireEvent.change(screen.getByRole("combobox"), {
    target: { value: "uploads" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Review Compose volume backup" }),
  );
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("button", { name: "Confirm Compose volume backup" }),
  ).toBeNull();
  expect(fetch.mock.calls.filter(([, o]) => o.method === "POST")).toHaveLength(
    0,
  );
});
test("viewer retains history", async () => {
  setup(undefined, false);
  await screen.findByRole("option", { name: "uploads" });
  expect(
    screen.queryByRole("button", { name: "Review Compose volume backup" }),
  ).toBeNull();
  expect(
    screen.getByRole("list", { name: "Compose volume backup history" }),
  ).toBeTruthy();
});
test("denial retires review", async () => {
  const { onDenied } = setup((_u, o) =>
    o.method === "POST" ? new Response("{}", { status: 403 }) : undefined,
  );
  await review();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(
    screen.getByRole("button", { name: "Confirm Compose volume backup" }),
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "Reviewed Compose volume backup" }),
    ).toBeNull(),
  );
  expect(onDenied).toHaveBeenCalled();
});

test("unmount aborts a pending mutation and ignores its late receipt", async () => {
  let signal: AbortSignal | undefined;
  let resolve!: (response: Response) => void;
  const pending = new Promise<Response>((done) => {
    resolve = done;
  });
  const { fetch, view, onDenied } = setup((_url, options) => {
    if (options.method === "POST") {
      signal = options.signal as AbortSignal;
      return pending;
    }
  });
  await review();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(
    screen.getByRole("button", { name: "Confirm Compose volume backup" }),
  );
  await waitFor(() => expect(signal).toBeDefined());
  expect(signal!.aborted).toBe(false);
  const readsBefore = fetch.mock.calls.length;
  view.unmount();
  expect(signal!.aborted).toBe(true);
  await act(async () => {
    resolve(new Response(JSON.stringify(backup)));
    await pending;
  });
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "Compose volume backups" }),
    ).toBeNull(),
  );
  expect(fetch.mock.calls).toHaveLength(readsBefore);
  expect(onDenied).not.toHaveBeenCalled();
});
