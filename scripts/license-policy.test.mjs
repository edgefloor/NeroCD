import assert from "node:assert/strict";
import test from "node:test";

import { assessLicenseDeclaration } from "./license-policy.mjs";

const allowed = new Set(["Apache-2.0", "MIT"]);

function assess(value) {
  return assessLicenseDeclaration(value, allowed);
}

test("keeps a simple approved SPDX ID as a CycloneDX license ID", () => {
  assert.deepEqual(assess("MIT"), {
    approved: true,
    cycloneDXLicense: { license: { id: "MIT" } },
  });
});

test("requires every branch of an AND expression to be approved", () => {
  assert.equal(assess("MIT AND GPL-3.0-only").approved, false);
  assert.equal(assess("GPL-3.0-only AND MIT").approved, false);
  assert.equal(assess("MIT AND Apache-2.0").approved, true);
});

test("accepts an OR expression when either branch is approved", () => {
  assert.equal(assess("MIT OR GPL-3.0-only").approved, true);
  assert.equal(assess("GPL-3.0-only OR MIT").approved, true);
  assert.equal(assess("GPL-2.0-only OR GPL-3.0-only").approved, false);
});

test("applies AND precedence and parentheses", () => {
  assert.equal(assess("MIT OR GPL-2.0-only AND GPL-3.0-only").approved, true);
  assert.equal(assess("(MIT OR GPL-2.0-only) AND GPL-3.0-only").approved, false);
  assert.equal(assess("((MIT OR GPL-2.0-only) AND Apache-2.0)").approved, true);
});

test("parses the entire declaration before returning an approval result", () => {
  assert.throws(() => assess("MIT OR GPL-3.0-only trailing"), /license expression/i);
  assert.throws(() => assess("MIT OR"), /license expression/i);
  assert.throws(() => assess("MIT OR (GPL-3.0-only"), /license expression/i);
});

test("rejects empty, non-string, and unsupported declarations", () => {
  for (const value of ["", "   ", null, undefined, 42, "MIT+", "MIT WITH LLVM-exception", "LicenseRef-internal"]) {
    assert.throws(() => assess(value), /license expression/i);
  }
});

test("preserves compound declarations as CycloneDX expressions", () => {
  assert.deepEqual(assess("(MIT OR CC0-1.0)"), {
    approved: true,
    cycloneDXLicense: { expression: "(MIT OR CC0-1.0)" },
  });
  assert.deepEqual(assess("(MIT OR Apache-2.0) AND GPL-3.0-only").cycloneDXLicense, {
    expression: "(MIT OR Apache-2.0) AND GPL-3.0-only",
  });
});
