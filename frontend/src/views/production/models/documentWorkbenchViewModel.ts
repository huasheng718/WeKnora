export type DocumentWorkbenchLayoutMode = 'rails' | 'drawers'

export function documentWorkbenchLayout(viewportWidth: number): {
  mode: DocumentWorkbenchLayoutMode
  editorMinWidth: 320 | 520
} {
  return viewportWidth >= 1100
    ? { mode: 'rails', editorMinWidth: 520 }
    : { mode: 'drawers', editorMinWidth: 320 }
}
