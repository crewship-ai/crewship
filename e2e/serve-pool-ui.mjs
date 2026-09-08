// Test-only static export server; not a Crewship application instance.
import { createServer } from "node:http"
import { readFile } from "node:fs/promises"
import { resolve, extname, sep } from "node:path"
const root = resolve("out")
const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2", ".ico": "image/x-icon" }
createServer(async (req, res) => {
  try {
    const path = resolve(root, "." + decodeURIComponent(new URL(req.url, "http://localhost").pathname))
    if (path !== root && !path.startsWith(root + sep)) { res.writeHead(403); res.end(); return }
    // No stat-then-read: checking a path and then opening it is a
    // check-then-use race (CodeQL js/file-system-race). Try the candidates
    // in the order the export lays them out and let the open decide —
    // a directory fails with EISDIR and falls through like anything else.
    let file = path
    let data = null
    for (const candidate of [path, path + ".html", resolve(path, "index.html")]) {
      if (candidate !== root && !candidate.startsWith(root + sep)) continue
      data = await readFile(candidate).catch(() => null)
      if (data) { file = candidate; break }
    }
    if (!data) { res.writeHead(404); res.end(); return }
    res.writeHead(200, { "Content-Type": types[extname(file)] ?? "application/octet-stream" }); res.end(data)
  } catch { res.writeHead(404); res.end() }
}).listen(3913, "127.0.0.1")
