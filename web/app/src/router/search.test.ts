import { expect, test } from "vitest";
import { validateSearch } from "./search";

test("list URLs retain a bounded page offset alongside their filter", () => {
  expect(validateSearch({ offset: 100, q: "release", environment_id: "env_one" })).toEqual({ offset: 100, q: "release", environment_id: "env_one" });
});

test.each([-1, 0, 1.5, Infinity, NaN, 2147483648, "50", null])("invalid or first-page offset %s is omitted", (offset) => {
  expect(validateSearch({ offset })).toEqual({});
});

test("recovery locator is bounded, paired and preserves unrelated search state", () => {
  const scope = JSON.stringify(["local", "user", "project", "proxy"]);
  expect(validateSearch({ q: "host", offset: 50, proxy_recovery_scope: scope, proxy_recovery_operation: "pop_original" })).toMatchObject({ q: "host", offset: 50, proxy_recovery_scope: scope, proxy_recovery_operation: "pop_original" });
  for (const locator of [{ proxy_recovery_scope: "{" }, { proxy_recovery_scope: "x".repeat(1025) }, { proxy_recovery_scope: JSON.stringify(["local", "user", "project"]) }, { proxy_recovery_operation: "../foreign" }, { proxy_recovery_operation: "x".repeat(129) }]) {
    expect(validateSearch({ q: "host", proxy_recovery_scope: scope, proxy_recovery_operation: "pop_original", ...locator })).toEqual({ q: "host" });
  }
});

test("backup recovery locator is complete, strictly bounded and carries no action", () => {
  const locator = { project_id: "project", environment_id: "env", resource_id: "db", backup_id: "backup", transfer_id: "upload" };
  expect(validateSearch({ ...locator, action: "retry", q: "preserved" })).toEqual({ ...locator, q: "preserved" });
  for (const key of ["project_id", "environment_id", "resource_id", "backup_id", "transfer_id"]) {
    expect(validateSearch({ ...locator, [key]: "../foreign" }).backup_id).toBeUndefined();
    expect(validateSearch({ ...locator, [key]: "x".repeat(129) }).backup_id).toBeUndefined();
  }
  expect(validateSearch({ project_id: "project", backup_id: "backup" })).toEqual({});
});
