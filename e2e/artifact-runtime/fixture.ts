import https from 'node:https'
import http from 'node:http'
import net from 'node:net'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import type { AddressInfo } from 'node:net'

export const studio = 'https://studio.crewship.test'
export const runtime = 'https://runtime.artifacts.test'
export const sink = 'https://sink.observer.test'
export const csp = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'"
export const script = (code: string) => `<!doctype html><p>artifact</p><script>${code}</script>`

// No DNS, /etc/hosts changes, public services or trusted certificate install.
// The browser sees distinct HTTPS registrable domains through a local tunnel.
export async function startFixture() {
  const dir = mkdtempSync(join(tmpdir(), 'artifact-runtime-'))
  const sockets = new Set<net.Socket>()
  const hits: string[] = []
  const tunnels: string[] = []
  const documents = new Map<string, string>()
  let tls: https.Server | undefined
  let proxy: http.Server | undefined
  try {
    execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', join(dir, 'key'), '-out', join(dir, 'cert'), '-days', '1', '-subj', '/CN=runtime.artifacts.test'], { stdio: 'ignore', timeout: 10000 })
    tls = https.createServer({ key: readFileSync(join(dir, 'key')), cert: readFileSync(join(dir, 'cert')) }, (req, res) => {
      const host = req.headers.host
      if (host === 'sink.observer.test') {
        hits.push(req.url!)
        if (req.url!.startsWith('/stall')) return // Deliberately no response; teardown destroys sockets.
        if (req.url!.startsWith('/204')) { res.writeHead(204); res.end(); return }
        if (req.url!.startsWith('/redirect')) { res.writeHead(302, { location: `${sink}/204` }); res.end(); return }
        res.end('sink'); return
      }
      if (host === 'studio.crewship.test') {
        res.setHeader('content-type', 'text/html')
        res.end(`<!doctype html><title>studio</title><script>window.ready=[];addEventListener("message",e=>window.ready.push(e.data));window.ticks=0;setInterval(()=>window.ticks++,20)</script>`); return
      }
      const url = new URL(req.url!, runtime)
      const body = documents.get(url.pathname)
      if (!body) { res.writeHead(404); res.end(); return }
      res.writeHead(200, { 'content-type': 'text/html', 'content-security-policy': `${url.searchParams.has('top') ? 'sandbox allow-scripts; ' : ''}${csp}; frame-ancestors ${studio}`, 'referrer-policy': 'no-referrer', 'x-content-type-options': 'nosniff', 'cache-control': 'no-store' })
      res.end(`<script>window.__ready=true;parent.postMessage(${JSON.stringify(url.pathname)},"*")</script>${body}`)
    })
    const track = (socket: net.Socket) => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)) }
    tls.on('connection', track)
    await new Promise<void>((resolve, reject) => { tls!.once('error', reject); tls!.listen(0, '127.0.0.1', resolve) })
    const tlsPort = (tls.address() as AddressInfo).port
    proxy = http.createServer((_req, res) => { res.writeHead(403); res.end() })
    proxy.on('connection', track)
    proxy.on('connect', (req, socket, head) => {
      tunnels.push(req.url!)
      if (!['studio.crewship.test:443', 'runtime.artifacts.test:443', 'sink.observer.test:443'].includes(req.url!)) { socket.destroy(); return }
      const upstream = net.connect(tlsPort, '127.0.0.1', () => { socket.write('HTTP/1.1 200 Connection Established\r\n\r\n'); upstream.write(head); socket.pipe(upstream); upstream.pipe(socket) })
      track(upstream)
      upstream.on('error', () => socket.destroy())
      socket.on('error', () => upstream.destroy())
    })
    await new Promise<void>((resolve, reject) => { proxy!.once('error', reject); proxy!.listen(0, '127.0.0.1', resolve) })
    return { hits, tunnels, documents, proxy: `http://127.0.0.1:${(proxy.address() as AddressInfo).port}`, close: async () => {
      for (const socket of sockets) socket.destroy()
      await Promise.all([new Promise<void>(r => tls!.close(() => r())), new Promise<void>(r => proxy!.close(() => r()))])
      rmSync(dir, { recursive: true })
    } }
  } catch (error) {
    for (const socket of sockets) socket.destroy()
    tls?.close(); proxy?.close(); rmSync(dir, { recursive: true }); throw error
  }
}
