export function responsiveSidebarCollapsed(compactViewport: boolean, persistedPreference: boolean): boolean {
  return compactViewport || persistedPreference
}
