import { expect, test } from "vitest";
import { parseInternalDestination, validatedInternalRedirect } from "./sign-in";

test("OIDC return destinations retain only known internal application routes", () => {
  expect(validatedInternalRedirect("/runs?q=deploy")).toBe("/runs?q=deploy");
  expect(parseInternalDestination("/runs/run_1?q=deploy")).toEqual({ to: "/runs/$runId", params: { runId: "run_1" }, search: { q: "deploy" } });
  for (const value of ["https://attacker.invalid/", "//attacker.invalid/", "/unknown", "/sign-in", "/runs%2f..", "/runs\\evil"]) {
    expect(validatedInternalRedirect(value)).toBe("/");
  }
});
