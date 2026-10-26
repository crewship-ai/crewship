// Deterministically compile and pin the small reference editor. The output is
// embedded by Go's generic editor package loader, not served as a Studio page.
import { build } from "esbuild"
import { createHash } from "node:crypto"
import { mkdir, readFile, writeFile } from "node:fs/promises"
const sha = body => createHash("sha256").update(body).digest("hex")
const source = "examples/mooring/hello"
const compiled = await build({ entryPoints: [`${source}/editor.ts`], bundle: true, write: false, platform: "browser", format: "esm", target: "es2022", minify: true, legalComments: "eof" })
const files = { "index.html": await readFile(`${source}/index.html`), "editor.css": await readFile(`${source}/editor.css`), "editor.js": compiled.outputFiles[0].contents }
const manifest = JSON.stringify({ toolchain: "Mooring TypeScript pilot / esbuild", files: Object.fromEntries(Object.entries(files).map(([name, bytes]) => [name, sha(bytes)])) })
const digest = sha(manifest)
const target = `internal/editororigin/packages/${digest}`
await mkdir(target, { recursive: true })
for (const [name, bytes] of Object.entries(files)) await writeFile(`${target}/${name}`, bytes)
await writeFile(`${target}/manifest.json`, manifest)
process.stdout.write(`${digest}\n`)
