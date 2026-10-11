import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { useSnapshotMutation } from "./compat";

const mocks = vi.hoisted(() => ({ invalidateQueries: vi.fn(), success: vi.fn(), error: vi.fn() }));
vi.mock("@tanstack/react-query", () => ({ useQueryClient: () => ({ invalidateQueries: mocks.invalidateQueries }) }));
vi.mock("sonner", () => ({ toast: { success: mocks.success, error: mocks.error } }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });

test("overlapping actions cannot replace the active action or run before it finishes", async () => {
  let release!: (value: string) => void;
  const action = vi.fn(() => new Promise<string>((resolve) => { release = resolve; }));
  const competing = vi.fn(async () => "second");
  const { result } = renderHook(useSnapshotMutation);
  let first!: Promise<string | undefined>;
  let second!: Promise<string | undefined>;
  act(() => {
    first = result.current.mutate("first", action, "First saved");
    second = result.current.mutate("second", competing, "Second saved");
  });
  expect(await second).toBeUndefined();
  expect(competing).not.toHaveBeenCalled();
  expect(result.current.busy).toBe("first");
  await act(async () => { release("first"); expect(await first).toBe("first"); });
  expect(result.current.busy).toBe("");
  expect(mocks.invalidateQueries).toHaveBeenCalledOnce();
  expect(mocks.success).toHaveBeenCalledExactlyOnceWith("First saved");
  await act(async () => { expect(await result.current.mutate("second", competing, "Second saved")).toBe("second"); });
  expect(competing).toHaveBeenCalledOnce();
});

test("the action remains locked until its cache refresh finishes", async () => {
  let release!: () => void;
  mocks.invalidateQueries.mockImplementationOnce(() => new Promise<void>((resolve) => { release = resolve; }));
  const { result } = renderHook(useSnapshotMutation);
  let pending!: Promise<string | undefined>;
  await act(async () => { pending = result.current.mutate("first", async () => "saved", "Saved"); });
  const competing = vi.fn(async () => "second");
  await act(async () => { await result.current.mutate("second", competing, "Saved again"); });
  expect(competing).not.toHaveBeenCalled();
  expect(result.current.busy).toBe("first");
  await act(async () => { release(); await pending; });
  expect(result.current.busy).toBe("");
});

test("failure releases the action so the user can retry", async () => {
  const { result } = renderHook(useSnapshotMutation);
  await act(async () => { expect(await result.current.mutate("save", async () => { throw new Error("Temporary failure"); }, "Saved")).toBeUndefined(); });
  expect(result.current.busy).toBe("");
  expect(mocks.error).toHaveBeenCalledExactlyOnceWith("Temporary failure");
  await act(async () => { expect(await result.current.mutate("save", async () => "retried", "Saved")).toBe("retried"); });
  expect(mocks.success).toHaveBeenCalledExactlyOnceWith("Saved");
});
