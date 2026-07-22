type MenuRouteName = string | symbol | null | undefined

const MENU_ROUTE_NAMES: Readonly<Record<string, readonly string[]>> = {
  'knowledge-bases': ['knowledgeBaseList', 'knowledgeBaseDetail', 'knowledgeBaseSettings'],
  'knowledge-production': ['productionProjects', 'productionDocumentTypes', 'productionProject', 'productionDocument'],
  agents: ['agentList'],
  organizations: ['organizationList'],
  creatChat: ['kbCreatChat', 'globalCreatChat'],
  settings: ['settings'],
}

export function isMenuPathActive(itemPath: string, routeName: MenuRouteName): boolean {
  const currentRoute = typeof routeName === 'string'
    ? routeName
    : routeName == null
      ? ''
      : String(routeName)
  const namedRoutes = MENU_ROUTE_NAMES[itemPath]

  return namedRoutes ? namedRoutes.includes(currentRoute) : itemPath === currentRoute
}
