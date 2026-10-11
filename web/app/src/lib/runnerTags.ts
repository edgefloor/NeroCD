export function parseRunnerTags(input: string): string[] {
  const value = input.trim();
  if (!value.startsWith("[")) return value.split(",").map((tag) => tag.trim()).filter(Boolean);
  try {
    const tags: unknown = JSON.parse(value);
    if (!Array.isArray(tags) || !tags.every((tag) => typeof tag === "string")) throw new Error();
    return tags.map((tag: string) => tag.trim()).filter(Boolean);
  } catch {
    throw new Error("Enter comma-separated tags or a JSON array of strings.");
  }
}

export function formatRunnerTags(tags: readonly string[]): string {
  if (tags.some((tag) => /[,\r\n]/.test(tag)) || tags[0]?.trimStart().startsWith("[")) return JSON.stringify(tags);
  return tags.join(", ");
}
