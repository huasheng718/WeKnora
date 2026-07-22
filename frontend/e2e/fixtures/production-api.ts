import { expect, type APIResponse, type Page } from '@playwright/test'

type JsonRecord = Record<string, unknown>

interface ApiEnvelope<T> {
  success: boolean
  data?: T
  message?: string
}

export interface QaIdentity {
  email: string
  password: string
  username: string
}

export interface ProductionQaData {
  projectId: string
  documentId: string
  versionId: string
  annotationId: string
  sourceSetId: string
  knowledgeBaseId?: string
  knowledgeBaseError?: string
}

export interface ProductionQaReleasePreflightTarget {
  knowledge_base_id: string
  knowledge_base_name: string
  ready: boolean
  reason?: string
  config_snapshot?: JsonRecord
}

export interface ProductionQaReleasePreflight {
  rendered_markdown: string
  targets: ProductionQaReleasePreflightTarget[]
}

export interface ProductionQaReleaseTarget {
  id: string
  status: 'building' | 'ready' | 'active' | 'failed' | 'rolled_back' | 'cleanup_pending' | 'cleaned'
  failure_reason?: string
  head_lock_version?: number
}

export function createQaIdentity(): QaIdentity {
  const suffix = crypto.randomUUID().replaceAll('-', '').slice(0, 12)
  return {
    username: `qa_${suffix}`,
    email: `qa.production.${suffix}@example.test`,
    password: `Qa1${suffix}x`,
  }
}

async function responseBody(response: APIResponse): Promise<JsonRecord> {
  const text = await response.text()
  if (!text) return {}
  try {
    return JSON.parse(text) as JsonRecord
  } catch {
    return { message: text }
  }
}

async function requireSuccess<T>(response: APIResponse, operation: string): Promise<T> {
  const body = await responseBody(response) as ApiEnvelope<T>
  if (!response.ok() || !body.success) {
    throw new Error(`${operation} failed (${response.status()}): ${body.message ?? JSON.stringify(body)}`)
  }
  return body.data as T
}

function authenticatedHeaders(token: string, mutation = false, extra: Record<string, string> = {}) {
  return {
    Authorization: `Bearer ${token}`,
    ...(mutation ? { 'Idempotency-Key': crypto.randomUUID() } : {}),
    ...extra,
  }
}

async function post<T>(page: Page, token: string, path: string, data?: unknown): Promise<T> {
  const response = await page.request.post(path, {
    headers: authenticatedHeaders(token, true),
    ...(data === undefined ? {} : { data }),
  })
  return requireSuccess<T>(response, `POST ${path}`)
}

async function put<T>(page: Page, token: string, path: string, data?: unknown): Promise<T> {
  const response = await page.request.put(path, {
    headers: authenticatedHeaders(token, true),
    ...(data === undefined ? {} : { data }),
  })
  return requireSuccess<T>(response, `PUT ${path}`)
}

async function get<T>(page: Page, token: string, path: string): Promise<T> {
  const response = await page.request.get(path, { headers: authenticatedHeaders(token) })
  return requireSuccess<T>(response, `GET ${path}`)
}

export async function registerQaIdentity(page: Page, identity: QaIdentity): Promise<void> {
  await page.addInitScript(() => {
    localStorage.setItem('weknora_auto_setup_failed', 'true')
  })
  await page.goto('/login')
  await page.getByRole('button', { name: '创建账户' }).click()
  await page.getByPlaceholder('输入用户名').fill(identity.username)
  await page.getByPlaceholder('输入邮箱地址').fill(identity.email)
  await page.getByPlaceholder('输入密码（8-32个字符，包含字母和数字）').fill(identity.password)
  await page.getByPlaceholder('再次输入密码').fill(identity.password)
  const [response] = await Promise.all([
    page.waitForResponse(candidate => candidate.url().endsWith('/api/v1/auth/register')),
    page.getByRole('button', { name: '注册', exact: true }).click(),
  ])
  const body = await responseBody(response)
  expect(response.status(), JSON.stringify(body)).toBe(201)
  expect(body.success).toBe(true)
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible()
  await expect(page.getByPlaceholder('输入邮箱地址')).toHaveValue(identity.email)
}

export async function loginQaIdentity(page: Page, identity: QaIdentity): Promise<{ token: string; userId: string }> {
  await page.getByPlaceholder(/输入密码/).fill(identity.password)
  await Promise.all([
    page.waitForResponse(response => response.url().includes('/api/v1/auth/login') && response.status() === 200),
    page.getByRole('button', { name: '登录', exact: true }).click(),
  ])
  await expect(page).toHaveURL(/\/platform\/knowledge-bases/)
  const onboarding = page.getByRole('dialog', { name: '欢迎使用 WeKnora' })
  if (await onboarding.isVisible()) {
    await onboarding.getByRole('button', { name: '跳过引导' }).last().click()
    await expect(onboarding).toBeHidden()
  }
  return page.evaluate(() => {
    const token = localStorage.getItem('weknora_token') ?? ''
    const user = JSON.parse(localStorage.getItem('weknora_user') ?? '{}') as { id?: string }
    if (!token || !user.id) throw new Error('login did not persist the authenticated identity')
    return { token, userId: user.id }
  })
}

export async function createProjectThroughUi(page: Page, projectName: string): Promise<string> {
  await page.goto('/platform/knowledge-production')
  await page.getByRole('button', { name: '新建项目' }).first().click()
  await page.getByPlaceholder('例如：服务交付基线').fill(projectName)
  await page.getByPlaceholder('说明范围、读者和预期产物').fill('Task 6 真实后端浏览器回归数据')
  await Promise.all([
    page.waitForResponse(response => response.url().endsWith('/api/v1/production/projects') && response.status() === 201),
    page.getByRole('button', { name: '创建', exact: true }).click(),
  ])
  await expect(page).toHaveURL(/\/platform\/knowledge-production\/projects\/[0-9a-f-]+/)
  return page.url().split('/').pop() ?? ''
}

async function tryCreateKnowledgeBase(page: Page, token: string, name: string) {
  const path = '/api/v1/knowledge-bases'
  const response = await page.request.post(path, {
    headers: authenticatedHeaders(token, true),
    data: {
      name,
      description: 'Task 6 publication target',
      type: 'document',
      indexing_strategy: {
        vector_enabled: true,
        keyword_enabled: true,
        wiki_enabled: false,
        graph_enabled: false,
      },
    },
  })
  const body = await responseBody(response) as ApiEnvelope<{ id: string }>
  if (!response.ok() || !body.success || !body.data?.id) {
    return { error: `POST ${path} failed (${response.status()}): ${body.message ?? JSON.stringify(body)}` }
  }
  return { id: body.data.id }
}

export async function seedProductionProject(
  page: Page,
  token: string,
  userId: string,
  projectId: string,
  suffix: string,
): Promise<ProductionQaData> {
  const retrospectiveSections = [
    '项目背景和目标',
    '关联研发基线',
    '计划与实际结果',
    '需求和范围变化',
    '质量、交付和运营数据',
    '事故、偏差和影响',
    '根因分析',
    '有效实践和经验',
    '改进行动项、负责人和截止时间',
    '证据清单',
  ]
  for (const role of ['author', 'business_reviewer', 'publisher'] as const) {
    await post(page, token, `/api/v1/production/projects/${projectId}/members`, { user_id: userId, role })
  }

  const documentType = await post<{ id: string }>(page, token, '/api/v1/production/document-types', {
    code: 'project-retrospective',
    name: `Task 6 治理文档 ${suffix}`,
    description: 'Task 6 real workflow document type',
    schema_version: 1,
    block_schema: { type: 'object' },
    source_requirements: {},
    skill_bindings: {},
    workflow_plan: { version: 1, steps: [] },
    quality_rules: {},
    review_policy: { steps: ['business_reviewer'] },
    publication_policy: {},
  })
  await put(page, token, `/api/v1/production/document-types/${documentType.id}/activate`)

  const sourceSet = await post<{ id: string }>(page, token, `/api/v1/production/projects/${projectId}/source-sets`, {
    document_type_id: documentType.id,
  })
  await post(page, token, `/api/v1/production/source-sets/${sourceSet.id}/freeze`)

  const document = await post<{ id: string; current_version_id: string }>(
    page,
    token,
    `/api/v1/production/projects/${projectId}/documents`,
    {
      document_type_id: documentType.id,
      source_set_id: sourceSet.id,
      title: `AI 企微复盘 ${suffix}`,
    },
  )
  const versionPath = `/api/v1/production/documents/${document.id}/versions`
  const versionResponse = await page.request.post(versionPath, {
    headers: authenticatedHeaders(token, true, { 'If-Match': document.current_version_id }),
    data: {
      source_set_id: sourceSet.id,
      origin: 'human',
      change_summary: 'Task 6 author draft',
      blocks: [
        ...retrospectiveSections.map(section => ({
          logical_block_id: crypto.randomUUID(),
          block_type: 'heading',
          content: section,
          attributes: {},
          evidence_refs: [],
          ai_provenance: {},
        })),
        {
          logical_block_id: crypto.randomUUID(),
          block_type: 'paragraph',
          content: 'Task 6 使用真实持久化数据验证作者、审核人与发布人流程。',
          attributes: { needs_confirmation: true },
          evidence_refs: [],
          ai_provenance: {},
        },
      ],
      lineage: [],
    },
  })
  const version = await requireSuccess<{ id: string; blocks: Array<{ id: string }> }>(
    versionResponse,
    `POST ${versionPath}`,
  )
  const detail = await get<{ id: string; blocks: Array<{ id: string }> }>(
    page,
    token,
    `/api/v1/production/documents/${document.id}/versions/${version.id}`,
  )
  const annotationBlockId = detail.blocks.at(-1)?.id
  if (!annotationBlockId) throw new Error('persisted production version has no annotatable block')
  const annotation = await post<{ id: string }>(page, token, `/api/v1/production/documents/${document.id}/annotations`, {
    version_id: version.id,
    block_id: annotationBlockId,
    annotation_type: 'quality_tag',
    quality_tag: 'missing_evidence',
    severity: 'blocking',
    anchor: {},
    body: '发布前必须解决的 Task 6 阻断标注',
  })

  const kb = await tryCreateKnowledgeBase(page, token, `Task 6 发布目标 ${suffix}`)
  return {
    projectId,
    documentId: document.id,
    versionId: version.id,
    annotationId: annotation.id,
    sourceSetId: sourceSet.id,
    knowledgeBaseId: kb.id,
    knowledgeBaseError: kb.error,
  }
}

export async function getReleasePreflight(
  page: Page,
  token: string,
  documentId: string,
  versionId: string,
): Promise<{ ok: boolean; data?: ProductionQaReleasePreflight; error?: string }> {
  const path = `/api/v1/production/documents/${documentId}/release-preflight?version_id=${versionId}&page=1&page_size=100`
  const response = await page.request.get(path, { headers: authenticatedHeaders(token) })
  const body = await responseBody(response) as ApiEnvelope<ProductionQaReleasePreflight>
  if (!response.ok() || !body.success) {
    return { ok: false, error: `GET ${path} failed (${response.status()}): ${body.message ?? JSON.stringify(body)}` }
  }
  return { ok: true, data: body.data }
}

export async function getReleaseTarget(
  page: Page,
  token: string,
  targetId: string,
): Promise<ProductionQaReleaseTarget> {
  return get<ProductionQaReleaseTarget>(page, token, `/api/v1/production/release-targets/${targetId}`)
}
