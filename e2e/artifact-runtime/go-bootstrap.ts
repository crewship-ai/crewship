import http from 'node:http'
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { studio, runtime } from './fixture'

export const artifactPreparationPath = '/api/v1/pages/runtime/artifact-preparation'

// The Go server supplies every byte and header, not a TS copy of its policy.
// Run via crewship-heavy with a precompiled pagebuild test binary (see README).
export async function startGoArtifactBootstrap() {
  const binary = process.env.CREWSHIP_TEST_RUNTIME_BINARY
  if (!binary) throw new Error('Precompile internal/pagebuild and set CREWSHIP_TEST_RUNTIME_BINARY; see e2e/README.md')
  const directory = await mkdtemp(join(tmpdir(), 'artifact-go-bootstrap-'))
  const file = join(directory, 'url')
  const child = spawn(binary, ['-test.run=^TestServeRuntimeBrowserHarness$'], { env: { ...process.env, CREWSHIP_TEST_RUNTIME_FILE: file, CREWSHIP_TEST_STUDIO_ORIGIN: studio, CREWSHIP_TEST_RUNTIME_ORIGIN: runtime, CREWSHIP_TEST_RUNTIME_PROFILE: 'artifact-preparation' }, stdio: ['ignore', 'pipe', 'pipe'] })
  let log = ''; let exited = false
  const exit = new Promise<void>(resolve => { child.once('exit', () => { exited = true; resolve() }); child.once('error', error => { log += error.message; exited = true; resolve() }) })
  child.stdout.on('data', data => { log = (log + String(data)).slice(-32768) })
  child.stderr.on('data', data => { log = (log + String(data)).slice(-32768) })
  let address = ''
  const close = async () => {
    if (!exited && address) await fetch(new URL('/__stop', address)).catch(() => {})
    if (!exited) child.kill('SIGTERM')
    await exit
    await rm(directory, { recursive: true, force: true })
  }
  try {
    for (let attempt = 0; attempt < 200; attempt++) {
      try { address = await readFile(file, 'utf8'); break } catch { if (exited) throw new Error(`Go bootstrap exited: ${log}`); await new Promise(resolve => setTimeout(resolve, 25)) }
    }
    if (!address || new URL(address).pathname !== artifactPreparationPath) throw new Error(`Go bootstrap did not advertise the preparation path: ${log}`)
    return { response: () => new Promise<{ status: number; headers: Record<string,string>; body: string }>((resolve,reject) => {
      const request=http.get(address,{headers:{Host:new URL(runtime).host}},response=>{
        const chunks: Buffer[]=[]
        response.on('data',chunk=>chunks.push(Buffer.from(chunk)))
        response.on('error',reject)
        response.on('end',()=>{
          const body=Buffer.concat(chunks).toString('utf8')
          if(response.statusCode!==200){reject(new Error(`Go bootstrap refused configured host: ${response.statusCode} ${body}`));return}
          if(response.headers['set-cookie']){reject(new Error('Constant bootstrap must not establish a session'));return}
          const headers:Record<string,string>={}
          for(const [key,value] of Object.entries(response.headers))if(typeof value==='string' && !['connection','transfer-encoding'].includes(key))headers[key]=value
          resolve({status:200,headers,body})
        })
      });request.on('error',reject)
    }), close }
  } catch (error) { await close(); throw error }
}
