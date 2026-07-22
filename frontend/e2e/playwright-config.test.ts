import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import test from 'node:test'

const config = readFileSync(new URL('../playwright.config.ts', import.meta.url), 'utf8')
const fixture = readFileSync(new URL('./fixtures/production-api.ts', import.meta.url), 'utf8')
const spec = readFileSync(new URL('./production-workbench.spec.ts', import.meta.url), 'utf8')
const backendLauncher = readFileSync(new URL('./run-qa-backend.sh', import.meta.url), 'utf8')
const teardownURL = new URL('./qa-teardown.ts', import.meta.url)

test('Playwright uses bundled Chromium with one retry and an ephemeral QA backend', () => {
  assert.match(config, /retries:\s*1/)
  assert.doesNotMatch(config, /channel:\s*['"]chrome['"]/)
  assert.match(config, /validateProductionQaEnvironment/)
  assert.match(config, /const baseURL = qa\.frontendURL/)
  assert.doesNotMatch(config, /const baseURL = process\.env\.PLAYWRIGHT_BASE_URL/)
  assert.match(config, /run-qa-backend\.sh/)
  assert.match(config, /globalTeardown:\s*['"]\.\/e2e\/qa-teardown\.ts['"]/)
  assert.match(backendLauncher, /go run -tags sqlite_fts5 \.\/cmd\/server/)
  assert.equal(existsSync(teardownURL), true)
  const teardown = readFileSync(teardownURL, 'utf8')
  assert.match(teardown, /rm\(qa\.dataDir, \{ recursive: true, force: true \}\)/)
})

test('the workflow registers through UI and never intercepts business responses', () => {
  assert.doesNotMatch(fixture, /page\.request\.post\(['"]\/api\/v1\/auth\/register/)
  assert.match(fixture, /getByRole\(['"]button['"], \{ name: ['"]创建账户['"] \}\)/)
  assert.match(fixture, /getByRole\(['"]button['"], \{ name: ['"]注册['"], exact: true \}\)/)
  assert.doesNotMatch(`${fixture}\n${spec}`, /page\.route|route\.fulfill|route\.continue/)
})

test('the unavailable publication branch asserts disabled target and publish controls', () => {
  assert.match(spec, /processing_configuration_unavailable/)
  assert.match(spec, /targetRow\.getByRole\(['"]checkbox['"]\).*toBeDisabled/)
  assert.match(spec, /releaseDialog\.getByRole\(['"]button['"], \{ name: ['"]发布['"], exact: true \}\).*toBeDisabled/)
})
