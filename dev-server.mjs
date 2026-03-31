/**
 * Custom Next.js dev server with WebSocket proxy.
 *
 * Problem: Next.js rewrites only handle HTTP, not WebSocket upgrade.
 * In dev mode the frontend runs on :3001 while the Go backend listens on
 * :8080.  Browsers that reach the frontend via SSH tunnel, LAN IP, or
 * localhost all need WebSocket to work transparently on the *same* origin
 * as the page — otherwise the connection fails or gets blocked by origin
 * checks.
 *
 * This server wraps `next dev` and intercepts `upgrade` requests for `/ws`
 * to proxy them to the Go backend.  No extra npm dependencies required.
 */

import { createServer, request as httpRequest } from "node:http"
import next from "next"

const port = parseInt(process.env.PORT || process.argv[2] || "3001", 10)
const goPort = parseInt(process.env.NEXT_PUBLIC_GO_PORT || "8080", 10)
const hostname = "0.0.0.0"

const app = next({ dev: true, hostname, port })
const handle = app.getRequestHandler()
const handleUpgrade = app.getUpgradeHandler()

await app.prepare()

const server = createServer((req, res) => handle(req, res))

server.on("upgrade", (req, socket, head) => {
  // Only proxy the /ws path; delegate everything else (e.g. HMR at
  // /_next/webpack-hmr) to Next.js so Turbopack hot-reload keeps working.
  if (!req.url?.startsWith("/ws")) {
    handleUpgrade(req, socket, head)
    return
  }

  const proxyReq = httpRequest({
    hostname: "localhost",
    port: goPort,
    path: req.url,
    method: req.method,
    headers: {
      ...req.headers,
      // Forward the real client host so the backend can log it.
      "x-forwarded-host": req.headers.host,
    },
  })

  proxyReq.on("upgrade", (proxyRes, proxySocket, proxyHead) => {
    // Relay the 101 Switching Protocols response.
    const statusLine = `HTTP/1.1 101 Switching Protocols\r\n`
    const headers = Object.entries(proxyRes.headers)
      .map(([k, v]) => `${k}: ${v}`)
      .join("\r\n")
    socket.write(statusLine + headers + "\r\n\r\n")
    if (proxyHead.length) socket.write(proxyHead)

    proxySocket.pipe(socket)
    socket.pipe(proxySocket)

    proxySocket.on("error", () => socket.destroy())
    socket.on("error", () => proxySocket.destroy())
  })

  // If the backend responds with a normal HTTP error (e.g. 401) instead of
  // upgrading, forward that error and close the socket.
  proxyReq.on("response", (proxyRes) => {
    const statusLine = `HTTP/1.1 ${proxyRes.statusCode} ${proxyRes.statusMessage}\r\n`
    const headers = Object.entries(proxyRes.headers)
      .map(([k, v]) => `${k}: ${v}`)
      .join("\r\n")
    socket.write(statusLine + headers + "\r\n\r\n")
    proxyRes.pipe(socket)
  })

  proxyReq.on("error", () => {
    socket.destroy()
  })

  if (head.length) proxyReq.write(head)
  proxyReq.end()
})

server.listen(port, hostname, () => {
  console.log(`> Dev server ready on http://${hostname}:${port}`)
  console.log(`> WebSocket /ws proxied to Go backend on :${goPort}`)
})
