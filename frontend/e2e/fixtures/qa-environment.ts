import { tmpdir } from 'node:os'
import { isAbsolute, relative, resolve } from 'node:path'

export interface ProductionQaEnvironment {
  backendURL: string
  dataDir: string
  mode: 'ephemeral-sqlite'
}

export function validateProductionQaEnvironment(
  environment: Record<string, string | undefined>,
): ProductionQaEnvironment {
  if (environment.PLAYWRIGHT_PRODUCTION_QA !== '1') {
    throw new Error('production QA requires explicit opt-in with PLAYWRIGHT_PRODUCTION_QA=1')
  }
  if (environment.PLAYWRIGHT_QA_BACKEND_MODE !== 'ephemeral-sqlite') {
    throw new Error('production QA requires PLAYWRIGHT_QA_BACKEND_MODE=ephemeral-sqlite')
  }

  const backendURL = parseLoopbackBackendURL(environment.PLAYWRIGHT_BACKEND_URL)
  const dataDir = validateTemporaryDataDirectory(environment.PLAYWRIGHT_QA_DATA_DIR)

  return { backendURL: backendURL.toString().replace(/\/$/, ''), dataDir, mode: 'ephemeral-sqlite' }
}

function parseLoopbackBackendURL(value: string | undefined): URL {
  let parsed: URL
  try {
    parsed = new URL(value ?? '')
  } catch {
    throw new Error('PLAYWRIGHT_BACKEND_URL must be an HTTP loopback URL')
  }
  if (parsed.protocol !== 'http:' || parsed.hostname !== '127.0.0.1') {
    throw new Error('PLAYWRIGHT_BACKEND_URL must use HTTP on the 127.0.0.1 loopback address')
  }
  const port = Number(parsed.port)
  if (!Number.isInteger(port) || port < 10_000 || port > 65_535) {
    throw new Error('PLAYWRIGHT_BACKEND_URL must use a dedicated port from 10000 to 65535')
  }
  if (parsed.username || parsed.password || parsed.pathname !== '/' || parsed.search || parsed.hash) {
    throw new Error('PLAYWRIGHT_BACKEND_URL must contain only the loopback origin and dedicated port')
  }
  return parsed
}

function validateTemporaryDataDirectory(value: string | undefined): string {
  if (!value || !isAbsolute(value)) {
    throw new Error('PLAYWRIGHT_QA_DATA_DIR must be an absolute temporary directory')
  }
  const temporaryRoot = resolve(tmpdir())
  const dataDir = resolve(value)
  const pathFromTemporaryRoot = relative(temporaryRoot, dataDir)
  if (!pathFromTemporaryRoot || pathFromTemporaryRoot.startsWith('..') || isAbsolute(pathFromTemporaryRoot)) {
    throw new Error('PLAYWRIGHT_QA_DATA_DIR must be a child of the operating system temporary directory')
  }
  return dataDir
}
