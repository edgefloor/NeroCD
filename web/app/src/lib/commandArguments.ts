/** Read argument quoting without evaluating variables, substitutions, or operators. */
export function parseCommandArguments(input: string): string[] {
  const args: string[] = [];
  let word = "";
  let started = false;
  let quote = "";
  for (let index = 0; index < input.length; index++) {
    const char = input[index]!;
    if (quote === "'") {
      if (char === "'") quote = "";
      else word += char;
      continue;
    }
    if (char === "\\") {
      const next = input[index + 1];
      if (next === undefined) throw new Error("Complete the escape after the final backslash.");
      if (!quote || ['"', "\\", "$", "`", "\n"].includes(next)) {
        index++;
        if (next !== "\n") { word += next; started = true; }
      } else word += char;
      continue;
    }
    if (quote) {
      if (char === quote) quote = "";
      else word += char;
    } else if (char === "'" || char === '"') {
      quote = char;
      started = true;
    } else if (/\s/.test(char)) {
      if (started) args.push(word);
      word = "";
      started = false;
    } else {
      word += char;
      started = true;
    }
  }
  if (quote) throw new Error(`Close the ${quote === "'" ? "single" : "double"} quote in the command.`);
  if (started) args.push(word);
  return args;
}

export function formatCommandArguments(args: readonly string[]): string {
  return args.map((arg) => /^[A-Za-z0-9_./:@%+=,-]+$/.test(arg) ? arg : `'${arg.replaceAll("'", "'\\''")}'`).join(" ");
}
