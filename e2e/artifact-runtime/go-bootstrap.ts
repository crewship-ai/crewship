import http from 'node:http'
import { spawn } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { studio, runtime } from './fixture'

export const artifactPreparationPath = '/api/v1/pages/runtime/artifact-preparation'

export function parseGoRuntimePort(line: string): number | null {
  const match = /^CREWSHIP_RUNTIME_READY_PORT=([1-9][0-9]{0,4})$/.exec(line)
  if (!match || match[0] !== line) return null
  const port = Number(match[1])
  return port <= 65535 ? port : null
}

// Every response byte/header comes from the Go implementation, not a TS copy.
export async function startGoArtifactBootstrap() {
  const binary = process.env.CREWSHIP_TEST_RUNTIME_BINARY
  if (!binary) throw new Error('Precompile internal/pagebuild and set CREWSHIP_TEST_RUNTIME_BINARY; see e2e/README.md')
  const directory = await mkdtemp(join(tmpdir(), 'artifact-go-bootstrap-'))
  const file = join(directory, 'url')
  const child = spawn(binary, ['-test.run=^TestServeRuntimeBrowserHarness$'], {
    env: { ...process.env, CREWSHIP_TEST_RUNTIME_FILE: file, CREWSHIP_TEST_STUDIO_ORIGIN: studio, CREWSHIP_TEST_RUNTIME_ORIGIN: runtime, CREWSHIP_TEST_RUNTIME_PROFILE: 'artifact-preparation' },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let log = ''
  let exited = false
  const exit = new Promise<void>(resolve => {
    child.once('exit', () => { exited = true; resolve() })
    child.once('error', error => { log += error.message; exited = true; resolve() })
  })
  let port = 0
  let output = ''
  let readinessError = ''
  child.stdout.on('data', data => {
    const chunk = String(data)
    log = (log + chunk).slice(-32768)
    output += chunk
    if (output.length > 32768) { readinessError = 'Go bootstrap readiness output exceeds limit'; output = ''; return }
    const lines = output.split('\n'); output = lines.pop()!
    for (const line of lines) {
      if (!line.startsWith('CREWSHIP_RUNTIME_READY_PORT=')) continue
      const advertised = parseGoRuntimePort(line)
      if (advertised === null || port !== 0) readinessError = 'Go bootstrap advertised an invalid or duplicate readiness port'
      else port = advertised
    }
  })
  child.stderr.on('data', data => { log = (log + String(data)).slice(-32768) })
  let closing: Promise<void> | undefined
  const waitForExit = async (milliseconds: number) => {
    let timer: ReturnType<typeof setTimeout> | undefined
    await Promise.race([exit, new Promise<void>(resolve => { timer = setTimeout(resolve, milliseconds) })])
    clearTimeout(timer)
  }
  const close = () => {
    closing ??= (async () => {
      if (!exited && port) await new Promise<void>(resolve => {
        const request = http.get({ hostname: '127.0.0.2', port, path: '/__stop' }, response => { response.resume(); response.once('end', () => { clearTimeout(deadline); resolve() }) })
        const deadline = setTimeout(() => request.destroy(), 1000)
        request.once('close', () => clearTimeout(deadline))
        request.once('error', () => resolve())
        request.once('close', () => resolve())
      })
      if (!exited) { child.kill('SIGTERM'); await waitForExit(1000) }
      if (!exited) { child.kill('SIGKILL'); await waitForExit(1000) }
      await rm(directory, { recursive: true, force: true })
      if (!exited) throw new Error('Suite-owned Go bootstrap did not exit after SIGKILL')
    })()
    return closing
  }
  try {
    const deadline = Date.now() + 5000
    while (!port && !readinessError && Date.now() < deadline) {
      if (exited) throw new Error(`Go bootstrap exited: ${log}`)
      await new Promise(resolve => setTimeout(resolve, 25))
    }
    if (readinessError || !port) throw new Error(readinessError || `Go bootstrap did not announce its readiness port: ${log}`)
    return {
      response: () => new Promise<{ status: number; headers: Record<string, string>; body: string }>((resolve, reject) => {
        const request = http.get({ hostname: '127.0.0.2', port, path: artifactPreparationPath, headers: { Host: new URL(runtime).host } }, response => {
          const chunks: Buffer[] = []
          let bytes = 0
          response.on('data', chunk => {
            bytes += chunk.length
            if (bytes > 64 * 1024) { request.destroy(new Error('Go bootstrap exceeds constant response limit')); return }
            chunks.push(Buffer.from(chunk))
          })
          response.on('error', reject)
          response.on('end', () => {
            const body = Buffer.concat(chunks).toString('utf8')
            if (response.statusCode !== 200) { reject(new Error(`Go bootstrap refused configured host: ${response.statusCode} ${body}`)); return }
            if (response.headers['set-cookie']) { reject(new Error('Constant bootstrap must not establish a session')); return }
            const headers: Record<string, string> = {}
            for (const [key, value] of Object.entries(response.headers)) {
              if (typeof value === 'string' && !['connection', 'transfer-encoding'].includes(key)) headers[key] = value
            }
            resolve({ status: 200, headers, body })
          })
        })
        const deadline = setTimeout(() => request.destroy(new Error('Go bootstrap response timed out')), 2000)
        request.once('close', () => clearTimeout(deadline))
        request.on('error', reject)
      }),
      close,
    }
  } catch (error) { await close(); throw error }
}
