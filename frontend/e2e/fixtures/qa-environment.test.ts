import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { validateProductionQaEnvironment } from './qa-environment'

const safeEnvironment = () => ({
  PLAYWRIGHT_PRODUCTION_QA: '1',
  PLAYWRIGHT_QA_BACKEND_MODE: 'ephemeral-sqlite',
  PLAYWRIGHT_BACKEND_URL: 'http://127.0.0.1:18080',
  PLAYWRIGHT_PORT: '15177',
  PLAYWRIGHT_BASE_URL: 'http://127.0.0.1:15177',
  PLAYWRIGHT_QA_DATA_DIR: join(tmpdir(), 'weknora-playwright-qa'),
})

test('accepts explicit ephemeral frontend and backend origins on loopback', () => {
  const environment = safeEnvironment()
  assert.deepEqual(validateProductionQaEnvironment(environment), {
    backendURL: environment.PLAYWRIGHT_BACKEND_URL,
    frontendURL: environment.PLAYWRIGHT_BASE_URL,
    dataDir: environment.PLAYWRIGHT_QA_DATA_DIR,
    mode: 'ephemeral-sqlite',
  })
})

test('rejects missing opt-in, non-loopback targets, ordinary ports, and persistent paths', () => {
  assert.throws(() => validateProductionQaEnvironment({ ...safeEnvironment(), PLAYWRIGHT_PRODUCTION_QA: '' }), /explicit opt-in/)
  assert.throws(() => validateProductionQaEnvironment({ ...safeEnvironment(), PLAYWRIGHT_BACKEND_URL: 'https://prod.example.com' }), /loopback/)
  assert.throws(() => validateProductionQaEnvironment({ ...safeEnvironment(), PLAYWRIGHT_BACKEND_URL: 'http://127.0.0.1:8080' }), /dedicated port/)
  assert.throws(() => validateProductionQaEnvironment({ ...safeEnvironment(), PLAYWRIGHT_QA_BACKEND_MODE: 'shared' }), /ephemeral-sqlite/)
  assert.throws(() => validateProductionQaEnvironment({ ...safeEnvironment(), PLAYWRIGHT_QA_DATA_DIR: '/var/lib/weknora' }), /temporary directory/)
})

test('rejects unsafe or mismatched frontend origins', () => {
  for (const frontendURL of [
    'https://prod.example.com',
    'http://localhost:15177',
    'http://127.0.0.1:15177/app',
    'http://user:password@127.0.0.1:15177',
    'http://127.0.0.1:15177?qa=1',
    'http://127.0.0.1:15177#qa',
  ]) {
    assert.throws(
      () => validateProductionQaEnvironment({ ...safeEnvironment(), PLAYWRIGHT_BASE_URL: frontendURL }),
      /PLAYWRIGHT_BASE_URL/,
      frontendURL,
    )
  }
  assert.throws(
    () => validateProductionQaEnvironment({ ...safeEnvironment(), PLAYWRIGHT_BASE_URL: 'http://127.0.0.1:15178' }),
    /PLAYWRIGHT_PORT/,
  )
})
