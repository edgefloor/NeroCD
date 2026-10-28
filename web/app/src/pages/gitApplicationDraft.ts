import type { GitApplicationDraft } from "./GitApplicationFields";
export const emptyGitDraft = (): GitApplicationDraft => ({
  repository_url: "",
  requested_ref: "main",
  credential_kind: "public",
  settings: { strategy: "dockerfile" },
});
