// Test-only static export server; not a Crewship application instance.
import { createServer } from "node:http"
import { readFile, stat } from "node:fs/promises"
import { resolve, extname, sep } from "node:path"
const root = resolve("out")
const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2", ".ico": "image/x-icon" }
createServer(async (req, res) => {
  try {
    const path = resolve(root, "." + decodeURIComponent(new URL(req.url, "http://localhost").pathname))
    if (path !== root && !path.startsWith(root + sep)) { res.writeHead(403); res.end(); return }
    let file = path
    const info = await stat(file).catch(() => null)
    if (info?.isDirectory()) file = await stat(path + ".html").then(() => path + ".html").catch(() => resolve(path, "index.html"))
    else if (!info) file += ".html"
    const data = await readFile(file)
    res.writeHead(200, { "Content-Type": types[extname(file)] ?? "application/octet-stream" }); res.end(data)
  } catch { res.writeHead(404); res.end() }
}).listen(3913, "127.0.0.1")
