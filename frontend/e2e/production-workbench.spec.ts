import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import {
  createProjectThroughUi,
  createQaIdentity,
  getReleasePreflight,
  getReleaseTarget,
  loginQaIdentity,
  registerQaIdentity,
  seedProductionProject,
} from './fixtures/production-api'

test('author to publisher workflow uses the live production backend', async ({ page }) => {
  const consoleErrors: string[] = []
  const pageErrors: string[] = []
  page.on('console', message => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', error => pageErrors.push(error.message))

  const identity = createQaIdentity()
  const suffix = identity.username.slice(-8)
  await registerQaIdentity(page, identity)
  const { token, userId } = await loginQaIdentity(page, identity)
  const projectId = await createProjectThroughUi(page, `Task 6 生产工作台 ${suffix}`)
  const qa = await seedProductionProject(page, token, userId, projectId, suffix)

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/platform/knowledge-production/documents/${qa.documentId}`)
  await dismissOnboarding(page)
  await expect(page.getByRole('heading', { name: `AI 企微复盘 ${suffix}` })).toBeVisible()
  await expect(page.getByText('Task 6 使用真实持久化数据验证作者、审核人与发布人流程。').first()).toBeVisible()
  await assertLayoutIntegrity(page)

  const screenshotDir = resolve('dist/playwright/screenshots')
  await mkdir(screenshotDir, { recursive: true })

  const rightRail = page.locator('.right-rail')
  await rightRail.getByText('标注', { exact: true }).click()
  await expect(rightRail.getByText('发布前必须解决的 Task 6 阻断标注')).toBeVisible()
  await rightRail.getByText('审核', { exact: true }).click()
  await expect(rightRail.getByText('1 条未解决阻断标注')).toBeVisible()
  await expect(rightRail.getByRole('button', { name: '提交审核' })).toBeDisabled()
  expect(page.viewportSize()).toEqual({ width: 1440, height: 900 })
  await page.screenshot({ path: resolve(screenshotDir, 'production-desktop.png') })

  await rightRail.getByText('标注', { exact: true }).click()
  const blockingAnnotation = rightRail.locator('.annotation-row').filter({ hasText: '发布前必须解决的 Task 6 阻断标注' })
  const resolveResponsePromise = page.waitForResponse(response =>
    response.url().endsWith(`/api/v1/production/annotations/${qa.annotationId}/status`) && response.status() === 200,
  )
  await blockingAnnotation.getByRole('button', { name: '解决' }).click()
  await resolveResponsePromise
  await expect(blockingAnnotation).toContainText('已解决')
  await page.reload()
  await dismissOnboarding(page)
  const refreshedRightRail = page.locator('.right-rail')
  await refreshedRightRail.getByText('审核', { exact: true }).click()
  const submitReview = refreshedRightRail.getByRole('button', { name: '提交审核' })
  await expect(submitReview).toBeEnabled()
  await Promise.all([
    page.waitForResponse(response => response.url().endsWith(`/api/v1/production/documents/${qa.documentId}/reviews`) && response.status() === 201),
    submitReview.click(),
  ])
  await expect(refreshedRightRail.getByText('待处理').first()).toBeVisible()
  await Promise.all([
    page.waitForResponse(response => response.url().includes('/decision') && response.status() === 200),
    refreshedRightRail.getByRole('button', { name: '通过', exact: true }).click(),
  ])
  await expect(refreshedRightRail.getByText('已通过').first()).toBeVisible()

  const preflight = await getReleasePreflight(page, token, qa.documentId, qa.versionId)
  expect(preflight.ok, preflight.error).toBe(true)
  expect(preflight.data?.rendered_markdown).toContain('Task 6 使用真实持久化数据验证作者、审核人与发布人流程。')

  await page.goto(`/platform/knowledge-production/projects/${projectId}`)
  await dismissOnboarding(page)
  await openReleaseTab(page)
  await page.getByRole('button', { name: '新建发布' }).click()
  const releaseDialog = page.locator('.release-dialog')
  await expect(releaseDialog).toBeVisible()
  await expect(releaseDialog.locator('.snapshot-pane pre')).toContainText('Task 6 使用真实持久化数据验证作者、审核人与发布人流程。')

  expect(qa.knowledgeBaseId, qa.knowledgeBaseError).toBeTruthy()
  const preflightTarget = preflight.data?.targets.find(target => target.knowledge_base_id === qa.knowledgeBaseId)
  expect(preflightTarget, 'created knowledge base was not returned by release preflight').toBeDefined()
  if (preflightTarget) {
    const targetRow = releaseDialog.locator('.target-row').filter({ hasText: preflightTarget.knowledge_base_name })
    await expect(targetRow).toBeVisible()
    if (!preflightTarget.ready) {
      expect(preflightTarget.reason).toBeTruthy()
      await expect(targetRow).toContainText(preflightTarget.reason ?? '')
      test.info().annotations.push({ type: 'publication limitation', description: preflightTarget.reason })
    } else {
      const embeddingModelId = preflightTarget.config_snapshot?.embedding_model_id
      expect(embeddingModelId).toBeTruthy()
      await expect(targetRow.locator('.config-grid')).toContainText(String(embeddingModelId))
      await targetRow.getByRole('checkbox').check()
      await releaseDialog.getByText('我确认此冻结发布快照。').click()
      const releaseResponsePromise = page.waitForResponse(response =>
        response.url().endsWith(`/api/v1/production/documents/${qa.documentId}/releases`) && response.status() === 201,
      )
      await releaseDialog.getByRole('button', { name: '发布', exact: true }).click()
      const releaseResponse = await releaseResponsePromise
      const releaseBody = await releaseResponse.json() as {
        data?: { targets?: Array<{ id: string }> }
      }
      const releaseTargetId = releaseBody.data?.targets?.[0]?.id
      expect(releaseTargetId).toBeTruthy()
      if (releaseTargetId) {
        let releaseTarget = await getReleaseTarget(page, token, releaseTargetId)
        for (let attempt = 0; attempt < 15 && releaseTarget.status === 'building'; attempt += 1) {
          await page.waitForTimeout(1_000)
          releaseTarget = await getReleaseTarget(page, token, releaseTargetId)
        }
        await page.reload()
        await dismissOnboarding(page)
        await openReleaseTab(page)
        const releaseRow = page.locator('.release-row').filter({ hasText: releaseTargetId.slice(0, 8) })
        await expect(releaseRow).toBeVisible()
        if (releaseTarget.status === 'failed') {
          await expect(releaseRow).toContainText(releaseTarget.failure_reason ?? '失败')
          const retryResponsePromise = page.waitForResponse(response =>
            response.url().endsWith(`/api/v1/production/release-targets/${releaseTargetId}/retry`) && response.status() === 200,
          )
          await releaseRow.getByRole('button', { name: '重试' }).click()
          await retryResponsePromise
          test.info().annotations.push({ type: 'publication limitation', description: releaseTarget.failure_reason })
        } else if (releaseTarget.status === 'ready') {
          const activateResponsePromise = page.waitForResponse(response =>
            response.url().endsWith(`/api/v1/production/release-targets/${releaseTargetId}/activate`) && response.status() === 200,
          )
          await releaseRow.getByRole('button', { name: '激活' }).click()
          await activateResponsePromise
          await expect(releaseRow).toContainText('已激活')
        } else {
          test.info().annotations.push({
            type: 'publication limitation',
            description: `release target remained ${releaseTarget.status} after 15 seconds`,
          })
        }
      }
    }
  }

  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`/platform/knowledge-production/documents/${qa.documentId}`)
  await dismissOnboarding(page)
  await expect(page.getByRole('heading', { name: `AI 企微复盘 ${suffix}` })).toBeVisible()
  await assertLayoutIntegrity(page)
  expect(page.viewportSize()).toEqual({ width: 390, height: 844 })
  await page.screenshot({ path: resolve(screenshotDir, 'production-mobile.png') })

  expect(pageErrors).toEqual([])
  expect(consoleErrors).toEqual([])
})

async function openReleaseTab(page: Page) {
  const releaseTab = page.locator('.production-project-tabs .t-tabs__nav-item').filter({ hasText: '发布' })
  await expect(releaseTab).toBeVisible()
  await releaseTab.click()
}

async function dismissOnboarding(page: Page) {
  const onboarding = page.getByRole('dialog', { name: '欢迎使用 WeKnora' })
  await onboarding.waitFor({ state: 'visible', timeout: 1_000 }).catch(() => undefined)
  if (await onboarding.isVisible()) {
    await onboarding.getByRole('button', { name: '跳过引导' }).last().click()
    await expect(onboarding).toBeHidden()
  }
}

async function assertLayoutIntegrity(page: Page) {
  const issues = await page.evaluate(() => {
    const visible = (element: HTMLElement) => {
      const style = getComputedStyle(element)
      const rect = element.getBoundingClientRect()
      return style.visibility !== 'hidden' && style.display !== 'none' && rect.width > 1 && rect.height > 1
    }
    const controls = Array.from(document.querySelectorAll<HTMLElement>('button, input, textarea, [role="tab"]'))
      .filter(visible)
      .map(element => ({ element, rect: element.getBoundingClientRect() }))
    const overlaps: string[] = []
    for (let index = 0; index < controls.length; index += 1) {
      for (let next = index + 1; next < controls.length; next += 1) {
        const a = controls[index]
        const b = controls[next]
        if (a.element.contains(b.element) || b.element.contains(a.element)) continue
        const width = Math.min(a.rect.right, b.rect.right) - Math.max(a.rect.left, b.rect.left)
        const height = Math.min(a.rect.bottom, b.rect.bottom) - Math.max(a.rect.top, b.rect.top)
        if (width > 3 && height > 3) overlaps.push(`${a.element.tagName}/${b.element.tagName}`)
      }
    }
    return {
      horizontalOverflow: document.documentElement.scrollWidth - window.innerWidth,
      clippedControls: controls
        .filter(({ rect }) => rect.left < -1 || rect.right > window.innerWidth + 1)
        .map(({ element }) => element.getAttribute('aria-label') || element.textContent?.trim() || element.tagName),
      overlaps,
    }
  })
  expect(issues.horizontalOverflow, JSON.stringify(issues)).toBeLessThanOrEqual(0)
  expect(issues.clippedControls, JSON.stringify(issues)).toEqual([])
  expect(issues.overlaps, JSON.stringify(issues)).toEqual([])
}
