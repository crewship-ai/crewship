// A TCP proxy in front of a test server whose connections can be cut for real.
// Chromium's offline emulation leaves an established stream running, so it cannot
// show what a browser learns while disconnected.
//   node revocation-proxy.mjs LISTEN_PORT TARGET_PORT CONTROL_PORT
//   POST /cut    closes every connection and refuses new ones
//   POST /restore accepts connections again
import * as http from "node:http"
import * as net from "node:net"

const [listen, target, control] = process.argv.slice(2).map(Number)
const sockets = new Set()
let cut = false

net.createServer((client) => {
  if (cut) { client.destroy(); return }
  const upstream = net.connect(target, "127.0.0.1")
  for (const socket of [client, upstream]) {
    sockets.add(socket)
    socket.on("error", () => {})
    socket.on("close", () => { sockets.delete(socket); client.destroy(); upstream.destroy() })
  }
  client.pipe(upstream)
  upstream.pipe(client)
}).listen(listen, "127.0.0.1")

http.createServer((request, response) => {
  cut = request.url === "/cut"
  if (cut) for (const socket of sockets) socket.destroy()
  response.end("ok")
}).listen(control, "127.0.0.1")
