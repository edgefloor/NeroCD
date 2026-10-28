const features: Record<string, string> = {
  image_deploy: "Container deployment",
  hosting_owned_storage_v1: "Persistent volume provisioning",
  hosting_owned_storage_runtime_v1: "Persistent volume attachment",
  hosting_runtime_env_v1: "Runtime variables and managed secrets",
  image_release_ports_v1: "Port allocation",
  hosting_managed_compose_v1: "Managed Compose",
};
export const catalogFeatureName = (name: string) => features[name] ?? name;
