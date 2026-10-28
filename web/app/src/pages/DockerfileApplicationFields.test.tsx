import { expect, test } from "vitest";
import { dockerfileContentError } from "./dockerfileApplicationDraft";

test("Dockerfile draft validation counts UTF-8 bytes and permits the exact server limit", () => {
 expect(dockerfileContentError(" \n\t")).toMatch(/required/);
 expect(dockerfileContentError("FROM nginx\0")).toMatch(/NUL/);
 expect(dockerfileContentError("é".repeat(524288))).toBe("");
 expect(dockerfileContentError("é".repeat(524288)+"x")).toMatch(/1 MiB/);
});
