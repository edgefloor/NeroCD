import type { ComposeApplicationDraft } from "./ComposeApplicationFields";
export const emptyComposeDraft = (): ComposeApplicationDraft => ({
  original: "",
  public_component: "",
  container_port: 80,
});
