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
    // Read directly instead of checking stat and then opening a path that may
    // have changed in between. Every fallback stays inside the export root.
    for (const file of [path, path + ".html", resolve(path, "index.html")]) {
      if (file !== root && !file.startsWith(root + sep)) continue
      try {
        const data = await readFile(file)
        res.writeHead(200, { "Content-Type": types[extname(file)] ?? "application/octet-stream" }); res.end(data)
        return
      } catch (error) {
        if (error.code !== "ENOENT" && error.code !== "EISDIR" && error.code !== "ENOTDIR") throw error
      }
    }
    res.writeHead(404); res.end()
  } catch { res.writeHead(404); res.end() }
}).listen(3914, "127.0.0.1")
