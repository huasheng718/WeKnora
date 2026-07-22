import type { ProductionAnnotation, ProductionAnnotationListResponse } from '@/api/production'

export const productionAnnotationPageSize = 100
// The annotation endpoint accepts offsets through 10,000; at 100 rows per page
// that makes pages 1 through 101 the complete reachable range.
const maximumProductionAnnotationPages = 101

export async function loadProductionAnnotationPages(
  loadPage: (page: number) => Promise<ProductionAnnotationListResponse>,
): Promise<ProductionAnnotation[]> {
  const annotations: ProductionAnnotation[] = []
  const annotationIds = new Set<string>()

  for (let page = 1; page <= maximumProductionAnnotationPages; page++) {
    const response = await loadPage(page)
    if (!response.success) throw new Error(response.message || 'failed to load production annotations')
    if (!Array.isArray(response.data) || response.page !== page || response.page_size !== productionAnnotationPageSize
      || !Number.isSafeInteger(response.total) || response.total < 0 || typeof response.has_more !== 'boolean') {
      throw new Error('invalid production annotation page response')
    }

    for (const annotation of response.data) {
      if (!annotationIds.has(annotation.id)) {
        annotationIds.add(annotation.id)
        annotations.push(annotation)
      }
    }
    if (!response.has_more) return annotations
    if (response.data.length === 0) throw new Error('production annotation pagination made no progress')
  }

  throw new Error('production annotation pagination exceeded the server offset limit')
}
