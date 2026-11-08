// Regenerates internal/slides/lucide_icons.json, the closed icon allow-list of the
// artifact Slides profile, from the installed lucide-react icon nodes.
// The browser renderer and the PDF renderer both draw from this one file; no
// author-supplied SVG is ever rendered. Run from the repository root:
//   node scripts/gen-slides-icons.mjs
import { readFile, writeFile } from "node:fs/promises"

const NAMES = [
  "arrow-down", "arrow-left", "arrow-right", "arrow-up", "bell", "bot", "briefcase", "building",
  "calendar", "chart-column", "chart-line", "chart-pie", "check", "circle-check", "clock", "cloud",
  "code", "database", "download", "file-text", "flag", "folder", "globe", "heart", "house", "info",
  "layers", "lightbulb", "link", "lock", "mail", "map-pin", "message-circle", "message-square",
  "phone", "play", "presentation", "rocket", "search", "send", "settings", "shield", "sparkles",
  "star", "target", "terminal", "trending-up", "triangle-alert", "upload", "user", "users", "x", "zap",
]
const ATTRS = { path: ["d"], circle: ["cx", "cy", "r"], ellipse: ["cx", "cy", "rx", "ry"], line: ["x1", "y1", "x2", "y2"], rect: ["x", "y", "width", "height", "rx", "ry"], polyline: ["points"], polygon: ["points"] }

const pkg = JSON.parse(await readFile("node_modules/lucide-react/package.json", "utf8"))
const icons = {}
for (const name of NAMES) {
  const { __iconData } = await import(`../node_modules/lucide-react/dist/esm/icons/${name}.mjs`)
  if (__iconData.size !== 24) throw new Error(`${name}: unexpected viewBox size ${__iconData.size}`)
  icons[name] = __iconData.node.map(([tag, attrs]) => {
    const allowed = ATTRS[tag]
    if (!allowed) throw new Error(`${name}: unsupported tag ${tag}`)
    const out = {}
    for (const [key, value] of Object.entries(attrs)) {
      if (key === "key") continue
      if (!allowed.includes(key)) throw new Error(`${name}: unsupported ${tag} attribute ${key}`)
      out[key] = String(value)
    }
    return [tag, out]
  })
}
const body = { source: `lucide-react@${pkg.version}`, license: "ISC", view_box: 24, stroke_width: 2, icons }
await writeFile("internal/slides/lucide_icons.json", JSON.stringify(body, null, 1) + "\n")
console.log(`wrote ${NAMES.length} icons from lucide-react@${pkg.version}`)
