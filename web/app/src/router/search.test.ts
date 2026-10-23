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
