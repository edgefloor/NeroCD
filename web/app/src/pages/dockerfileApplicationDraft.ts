import type { DockerfileApplicationDraft } from "./DockerfileApplicationFields";
export function dockerfileContentError(content: string): string {
  if (!content.trim()) return "Dockerfile content is required.";
  if (content.includes("\0")) return "Dockerfile content cannot contain NUL.";
  if (new TextEncoder().encode(content).length > 1_048_576)
    return "Dockerfile content allows at most 1 MiB of UTF-8 text.";
  return "";
}
export const emptyDockerfileDraft = (): DockerfileApplicationDraft => ({
  content: "",
  settings: { strategy: "dockerfile" },
});
