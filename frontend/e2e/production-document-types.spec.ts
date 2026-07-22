import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import { expect, test, type Locator, type Page } from '@playwright/test'
import {
  createQaIdentity,
  loginQaIdentity,
  openProductionDocumentTypes,
  registerQaIdentity,
} from './fixtures/production-api'

test('workspace owner governs a derived SOP document type through the live backend', async ({ page }) => {
  const consoleErrors: string[] = []
  const pageErrors: string[] = []
  page.on('console', message => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', error => pageErrors.push(error.message))

  const identity = createQaIdentity()
  const suffix = identity.username.slice(-8)
  const customName = `Task 8 自定义 SOP ${suffix}`
  await registerQaIdentity(page, identity)
  await loginQaIdentity(page, identity)

  await page.setViewportSize({ width: 1440, height: 900 })
  const table = await openProductionDocumentTypes(page)
  await expect(table.getByRole('row')).toHaveCount(6)
  await expect(page.getByText('共 5 个版本', { exact: true })).toBeVisible()
  await expect(page.getByText('5 个生效版本', { exact: true })).toBeVisible()
  const initialRows = table.locator('tbody tr')
  await expect(initialRows).toHaveCount(5)
  await expect(initialRows.getByText('内置', { exact: true })).toHaveCount(5)
  await expect(initialRows.getByRole('cell', { name: 'v1', exact: true })).toHaveCount(5)
  await expect(initialRows.getByText('生效', { exact: true })).toHaveCount(5)

  const sopV1 = documentTypeRow(table, 'sop', 1)
  await expect(sopV1).toContainText('标准作业程序')
  await expect(sopV1).toContainText('内置')
  await expect(sopV1).toContainText('生效')
  await expect(sopV1.getByRole('button', { name: '查看配置' })).toBeVisible()
  await expect(sopV1.getByRole('button', { name: '派生草稿' })).toBeVisible()

  await sopV1.getByRole('button', { name: '查看配置' }).click()
  const configurationDrawer = visibleDrawer(page, '文档类型配置')
  await expect(configurationDrawer).toContainText('标准作业程序')
  await expect(configurationDrawer).toContainText('内置')
  await expect(configurationDrawer).toContainText('生效')
  await expect(configurationDrawer.getByRole('heading', { name: '治理摘要' })).toBeVisible()
  await expect(configurationDrawer).toContainText('共 8 个必需章节')
  await expect(configurationDrawer).toContainText('business_reviewer')
  await expect(configurationDrawer.getByRole('heading', { name: '原始 JSON 配置' })).toBeVisible()
  await expect(configurationDrawer).toContainText('sop_exception_path')
  await closeDrawer(page, configurationDrawer)

  await sopV1.getByRole('button', { name: '派生草稿' }).click()
  const deriveDrawer = visibleDrawer(page, '派生文档类型草稿')
  await expect(deriveDrawer).toContainText('sop')
  await expect(deriveDrawer).toContainText('基础版本')
  await expect(deriveDrawer).toContainText('v1')
  await expect(deriveDrawer).toContainText('由服务器生成')
  await deriveDrawer.getByPlaceholder('例如：服务交付基线').fill(customName)
  await deriveDrawer.getByPlaceholder('说明该类型的适用范围').fill('Task 8 真实浏览器派生与激活验收')
  await Promise.all([
    page.waitForResponse(response =>
      response.request().method() === 'POST'
        && response.url().includes('/api/v1/production/document-types/')
        && response.url().endsWith('/drafts')
        && response.status() === 201,
      { timeout: 5_000 },
    ),
    deriveDrawer.getByRole('button', { name: '派生草稿' }).click(),
  ])
  await expect(deriveDrawer).not.toHaveClass(/t-drawer--open/)

  await expect(page.getByText('共 6 个版本', { exact: true })).toBeVisible()
  await expect(page.getByText('5 个生效版本', { exact: true })).toBeVisible()
  const customV2 = documentTypeRow(table, 'sop', 2)
  await expect(documentTypeRow(table, 'sop', 1)).toContainText('生效')
  await expect(customV2).toContainText(customName)
  await expect(customV2).toContainText('自定义')
  await expect(customV2).toContainText('草稿')
  await expect(customV2.getByRole('button', { name: '激活' })).toBeVisible()
  await expect(customV2.getByRole('button', { name: '派生草稿' })).toHaveCount(0)

  await customV2.getByRole('button', { name: '激活' }).click()
  await assertActionColumnReachable(page, customName, ['查看配置', '激活'])
  const activationDialog = page.locator('.t-dialog:visible').filter({ hasText: '激活文档类型？' })
  await expect(activationDialog).toContainText(`${customName}”v2`)
  await Promise.all([
    page.waitForResponse(response =>
      response.request().method() === 'PUT'
        && response.url().includes('/api/v1/production/document-types/')
        && response.url().endsWith('/activate')
        && response.status() === 200,
      { timeout: 5_000 },
    ),
    activationDialog.getByRole('button', { name: '激活', exact: true }).click(),
  ])
  await expect(activationDialog).toBeHidden()

  await expect(page.getByText('5 个生效版本', { exact: true })).toBeVisible()
  await expect(documentTypeRow(table, 'sop', 1)).toContainText('已退役')
  await expect(documentTypeRow(table, 'sop', 2)).toContainText('生效')
  await expect(documentTypeRow(table, 'sop', 2).getByRole('button', { name: '派生草稿' })).toBeVisible()
  await expect(page.getByRole('button', { name: '刷新' })).toBeVisible()
  await expect(page.getByRole('button', { name: '创建草稿' })).toBeVisible()

  const screenshotDir = resolve('dist/playwright/screenshots')
  await mkdir(screenshotDir, { recursive: true })
  await expect(page.locator('.t-message')).toHaveCount(0, { timeout: 6_000 })
  await resetDocumentTypeTableScroll(page)
  await assertRenderedDocumentTypePage(page)
  const desktopScreenshot = await page.screenshot({
    path: resolve(screenshotDir, 'production-document-types-desktop.png'),
    fullPage: true,
  })
  expect(desktopScreenshot.byteLength).toBeGreaterThan(20_000)

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole('heading', { name: '文档类型管理' })).toBeVisible()
  await expect(page.getByRole('button', { name: '刷新' })).toBeVisible()
  await expect(page.getByRole('button', { name: '创建草稿' })).toBeVisible()
  await resetDocumentTypeTableScroll(page)
  await assertRenderedDocumentTypePage(page)
  const mobileScroller = await documentTypeScrollerMetrics(page)
  expect(mobileScroller.overflowX).toMatch(/^(auto|scroll)$/)
  expect(mobileScroller.scrollWidth).toBeGreaterThan(mobileScroller.clientWidth)
  expect(mobileScroller.scrollLeft).toBe(0)
  await scrollDocumentTypeTableToEnd(page)
  const mobileScrollerAtEnd = await documentTypeScrollerMetrics(page)
  expect(mobileScrollerAtEnd.scrollLeft).toBeCloseTo(
    mobileScrollerAtEnd.scrollWidth - mobileScrollerAtEnd.clientWidth,
    0,
  )
  await assertActionColumnReachable(page, customName, ['查看配置', '派生草稿'])
  await assertRenderedDocumentTypePage(page)
  await resetDocumentTypeTableScroll(page)
  await assertRenderedDocumentTypePage(page)
  const mobileScreenshot = await page.screenshot({
    path: resolve(screenshotDir, 'production-document-types-mobile.png'),
    fullPage: true,
  })
  expect(mobileScreenshot.byteLength).toBeGreaterThan(20_000)

  expect(pageErrors).toEqual([])
  expect(consoleErrors).toEqual([])
})

function documentTypeRow(table: Locator, code: string, schemaVersion: number) {
  return table.locator(
    `tbody tr:has(td:nth-child(3) code:text-is("${code}")):has(td:nth-child(4):text-is("v${schemaVersion}"))`,
  )
}

function visibleDrawer(page: Page, heading: string) {
  return page.locator('.t-drawer:visible').filter({ hasText: heading })
}

async function closeDrawer(page: Page, drawer: Locator) {
  await page.keyboard.press('Escape')
  await expect(drawer).not.toHaveClass(/t-drawer--open/)
}

async function resetDocumentTypeTableScroll(page: Page) {
  await page.locator('.table-scroll').evaluate(element => {
    element.scrollLeft = 0
  })
}

async function scrollDocumentTypeTableToEnd(page: Page) {
  await page.locator('.table-scroll').evaluate(element => {
    element.scrollLeft = element.scrollWidth
  })
}

async function documentTypeScrollerMetrics(page: Page) {
  return page.locator('.table-scroll').evaluate(element => ({
    clientWidth: element.clientWidth,
    overflowX: getComputedStyle(element).overflowX,
    scrollLeft: element.scrollLeft,
    scrollWidth: element.scrollWidth,
  }))
}

async function assertActionColumnReachable(page: Page, customName: string, expectedActions: string[]) {
  const result = await page.evaluate((name) => {
    const scroller = document.querySelector<HTMLElement>('.table-scroll')
    const table = scroller?.querySelector<HTMLTableElement>('table')
    const row = Array.from(table?.tBodies[0]?.rows ?? [])
      .find(candidate => candidate.cells[0]?.textContent?.trim() === name)
    const actionHeader = table?.tHead?.rows[0]?.cells[7] as HTMLElement | undefined
    const actionCell = row?.cells[7] as HTMLElement | undefined
    if (!scroller || !table || !row || !actionHeader || !actionCell) {
      throw new Error('document type action column is incomplete')
    }
    const rect = (element: Element) => {
      const value = element.getBoundingClientRect()
      return { top: value.top, right: value.right, bottom: value.bottom, left: value.left, width: value.width, height: value.height }
    }
    const textRect = (element: Element) => {
      const range = document.createRange()
      range.selectNodeContents(element)
      const value = range.getBoundingClientRect()
      return { top: value.top, right: value.right, bottom: value.bottom, left: value.left, width: value.width, height: value.height }
    }
    const buttons = Array.from(actionCell.querySelectorAll<HTMLElement>('button'))
    const buttonMetrics = buttons.map(button => ({
      label: button.textContent?.trim() ?? '',
      rect: rect(button),
      textRect: textRect(button),
      textFits: button.scrollWidth <= button.clientWidth + 1 && button.scrollHeight <= button.clientHeight + 1,
    }))
    const overlaps: string[] = []
    for (let index = 0; index < buttonMetrics.length; index += 1) {
      for (let next = index + 1; next < buttonMetrics.length; next += 1) {
        const a = buttonMetrics[index]
        const b = buttonMetrics[next]
        const width = Math.min(a.rect.right, b.rect.right) - Math.max(a.rect.left, b.rect.left)
        const height = Math.min(a.rect.bottom, b.rect.bottom) - Math.max(a.rect.top, b.rect.top)
        if (width > 1 && height > 1) overlaps.push(`${a.label}/${b.label}`)
      }
    }
    return {
      actionCell: rect(actionCell),
      actionHeaderText: textRect(actionHeader),
      buttons: buttonMetrics,
      overlaps,
      pageWidth: document.documentElement.scrollWidth,
      row: rect(row),
      scroller: rect(scroller),
      scrollLeft: scroller.scrollLeft,
      scrollMax: scroller.scrollWidth - scroller.clientWidth,
      viewportWidth: window.innerWidth,
    }
  }, customName)
  const visibleLeft = Math.max(0, result.scroller.left)
  const visibleRight = Math.min(result.viewportWidth, result.scroller.right)
  expect(result.actionHeaderText.left, JSON.stringify(result)).toBeGreaterThanOrEqual(visibleLeft - 1)
  expect(result.actionHeaderText.right, JSON.stringify(result)).toBeLessThanOrEqual(visibleRight + 1)
  expect(result.actionHeaderText.top, JSON.stringify(result)).toBeGreaterThanOrEqual(result.scroller.top - 1)
  expect(result.actionHeaderText.bottom, JSON.stringify(result)).toBeLessThanOrEqual(result.scroller.bottom + 1)
  expect(result.buttons.map(button => button.label)).toEqual(expectedActions)
  for (const button of result.buttons) {
    expect(button.rect.left, JSON.stringify(result)).toBeGreaterThanOrEqual(visibleLeft - 1)
    expect(button.rect.right, JSON.stringify(result)).toBeLessThanOrEqual(visibleRight + 1)
    expect(button.rect.top, JSON.stringify(result)).toBeGreaterThanOrEqual(result.actionCell.top - 1)
    expect(button.rect.bottom, JSON.stringify(result)).toBeLessThanOrEqual(result.actionCell.bottom + 1)
    expect(button.rect.top, JSON.stringify(result)).toBeGreaterThanOrEqual(result.row.top - 1)
    expect(button.rect.bottom, JSON.stringify(result)).toBeLessThanOrEqual(result.row.bottom + 1)
    expect(button.textRect.left, JSON.stringify(result)).toBeGreaterThanOrEqual(button.rect.left - 1)
    expect(button.textRect.right, JSON.stringify(result)).toBeLessThanOrEqual(button.rect.right + 1)
    expect(button.textRect.top, JSON.stringify(result)).toBeGreaterThanOrEqual(button.rect.top - 1)
    expect(button.textRect.bottom, JSON.stringify(result)).toBeLessThanOrEqual(button.rect.bottom + 1)
    expect(button.textFits, JSON.stringify(result)).toBe(true)
  }
  expect(result.overlaps, JSON.stringify(result)).toEqual([])
  expect(result.pageWidth, JSON.stringify(result)).toBeLessThanOrEqual(result.viewportWidth)
}

async function assertRenderedDocumentTypePage(page: Page) {
  const result = await page.evaluate(() => {
    const pageRoot = document.querySelector<HTMLElement>('.document-types-page')
    if (!pageRoot) throw new Error('document type page root is missing')
    const visible = (element: HTMLElement) => {
      const style = getComputedStyle(element)
      const rect = element.getBoundingClientRect()
      if (style.visibility === 'hidden' || style.display === 'none' || rect.width <= 1 || rect.height <= 1) return false
      return rect.bottom > 0 && rect.top < window.innerHeight && rect.right > 0 && rect.left < window.innerWidth
    }
    const controls = Array.from(pageRoot.querySelectorAll<HTMLElement>('button, input, textarea, [role="tab"]'))
      .filter(visible)
      .map(element => ({ element, rect: element.getBoundingClientRect() }))
    const overlaps: string[] = []
    for (let index = 0; index < controls.length; index += 1) {
      for (let next = index + 1; next < controls.length; next += 1) {
        const a = controls[index]
        const b = controls[next]
        if (a.element.contains(b.element) || b.element.contains(a.element)) continue
        const overlapWidth = Math.min(a.rect.right, b.rect.right) - Math.max(a.rect.left, b.rect.left)
        const overlapHeight = Math.min(a.rect.bottom, b.rect.bottom) - Math.max(a.rect.top, b.rect.top)
        if (overlapWidth > 3 && overlapHeight > 3) {
          overlaps.push(`${a.element.outerHTML.slice(0, 100)} / ${b.element.outerHTML.slice(0, 100)}`)
        }
      }
    }
    return {
      bodyText: pageRoot.innerText.trim(),
      pageWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
      clippedControls: controls
        .filter(({ rect }) => rect.left < -1 || rect.right > window.innerWidth + 1)
        .map(({ element }) => element.getAttribute('aria-label') || element.textContent?.trim() || element.tagName),
      overlaps,
    }
  })
  expect(result.bodyText.length).toBeGreaterThan(200)
  expect(result.bodyText).not.toMatch(/(?:production|common|layout)\.[A-Za-z0-9_.]+/)
  expect(result.pageWidth, JSON.stringify(result)).toBeLessThanOrEqual(result.viewportWidth)
  expect(result.clippedControls, JSON.stringify(result)).toEqual([])
  expect(result.overlaps, JSON.stringify(result)).toEqual([])
}
