import { expect, test } from "vitest";
import { formatRunnerTags, parseRunnerTags } from "./runnerTags";

test.each([
  ["linux, prod, ,", ["linux", "prod"]],
  ['["linux", "zone=west,east", "line\\nbreak"]', ["linux", "zone=west,east", "line\nbreak"]],
  ['[" prod ", "", "prod"]', ["prod", "prod"]],
  ["", []],
] as [string, string[]][])("reads tags from %s", (input, expected) => {
  expect(parseRunnerTags(input)).toEqual(expected);
});

test.each(['["unfinished"', '["linux", 1]', '[null]', '[{}]'])("rejects malformed tag input %s", (input) => {
  expect(() => parseRunnerTags(input)).toThrow("Enter comma-separated tags or a JSON array of strings.");
});

test.each([
  ["linux", "prod"],
  ["region=west,east", "line\r\nbreak", 'quote"tag', "\\backslash"],
  ["[special]", "other"],
])("tag formatting preserves %j", (...tags) => {
  expect(parseRunnerTags(formatRunnerTags(tags))).toEqual(tags);
});
