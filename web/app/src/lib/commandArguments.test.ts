import { expect, test } from "vitest";
import { formatCommandArguments, parseCommandArguments } from "./commandArguments";

test.each([
  ["echo hello", ["echo", "hello"]],
  ['echo "hello world" \'\'', ["echo", "hello world", ""]],
  ["echo 'it'\\''s ready'", ["echo", "it's ready"]],
  ['echo before" middle "after', ["echo", "before middle after"]],
  [String.raw`echo path\ with\ spaces "C:\folder\file"`, ["echo", "path with spaces", String.raw`C:\folder\file`]],
  ['echo "$HOME" $(date) *.txt | cat', ["echo", "$HOME", "$(date)", "*.txt", "|", "cat"]],
  ["echo one\\\ntwo", ["echo", "onetwo"]],
  ["  \n\t", []],
] as [string, string[]][])("parses argument boundaries in %s", (input, expected) => {
  expect(parseCommandArguments(input)).toEqual(expected);
});

test.each(["echo 'unfinished", 'echo "unfinished', "echo trailing\\"])("rejects incomplete quoting in %s", (input) => {
  expect(() => parseCommandArguments(input)).toThrow();
});

test("formatting round trips arbitrary argument content without evaluation", () => {
  const args = ["", "echo", "hello world", "'\"\\", "$HOME", "$(date)", "line\r\nbreak", "tab\there", "🍀", "a;b"];
  expect(parseCommandArguments(formatCommandArguments(args))).toEqual(args);
});
