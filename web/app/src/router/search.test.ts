import { expect, test } from "vitest";
import { validateSearch } from "./search";

test("list URLs retain a bounded page offset alongside their filter", () => {
  expect(validateSearch({ offset: 100, q: "release", environment_id: "env_one" })).toEqual({ offset: 100, q: "release", environment_id: "env_one" });
});

test.each([-1, 0, 1.5, Infinity, NaN, 2147483648, "50", null])("invalid or first-page offset %s is omitted", (offset) => {
  expect(validateSearch({ offset })).toEqual({});
});
