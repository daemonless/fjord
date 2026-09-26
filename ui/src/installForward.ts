// The App Store forwards the wizard's whole deploy event: a hand-picked field
// list once dropped the network plan, so every chosen network was lost.
export function forwardDeploy<T extends { name?: string }>(detail: T, appId: string): T & { name: string; appId: string } {
  return { ...detail, name: detail.name || appId, appId };
}
