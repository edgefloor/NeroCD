import { describe, expect, test } from "vitest";
import { parseInternalDestination } from "../routes/sign-in";

describe("parseInternalDestination", () => {
  test("returns only known internal routes and preserves validated q", () => {
    expect(parseInternalDestination("/runs/run_1?q=terraform")).toEqual({ to: "/runs/$runId", params: { runId: "run_1" }, search: { q: "terraform" } });
    expect(parseInternalDestination("/projects?q=platform")).toEqual({ to: "/projects", search: { q: "platform" } });
    expect(parseInternalDestination("/deployments/dep_1?q=rollback")).toEqual({ to: "/deployments/$deploymentId", params: { deploymentId: "dep_1" }, search: { q: "rollback" } });
  });

  test.each(["https://evil.example/", "//evil.example/", "/%2f%2fevil", "/runs%2fother", "/\\evil", "/sign-in", "/sign-in?redirect=/projects", "/unknown"]) ("rejects malicious or unknown destination %s", (value) => {
    expect(parseInternalDestination(value)).toEqual({ to: "/" });
  });
});
