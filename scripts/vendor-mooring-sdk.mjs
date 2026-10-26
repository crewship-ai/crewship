#!/usr/bin/env node
/** Reproduce GridCraft's native SDK module from the canonical editor client. */
import { build, version } from "esbuild"
import { createHash } from "node:crypto"
import { execFileSync } from "node:child_process"
import { mkdir, readFile, writeFile } from "node:fs/promises"
import { resolve, dirname } from "node:path"
import { fileURLToPath } from "node:url"
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
if (process.argv.length !== 3) throw new Error("Usage: node scripts/vendor-mooring-sdk.mjs <gridcraft-checkout>")
const target = resolve(process.argv[2], "apps/gridcraft-web/mooring")
const files = ["channel.ts", "editor-connection.ts", "editor-sdk.ts", "native-profile.ts", "gridcraft-native.ts"]
execFileSync("git", ["diff", "--quiet", "HEAD", "--", "lib/mooring"], { cwd: root })
const hash = value => createHash("sha256").update(value).digest("hex")
await mkdir(resolve(target, "src"), { recursive: true })
const sources = {}
for (const name of files) {
  const contents = await readFile(resolve(root, "lib/mooring", name))
  await writeFile(resolve(target, "src", name), contents)
  sources[name] = hash(contents)
}
await writeFile(resolve(target, "LICENSE"), await readFile(resolve(root, "LICENSE")))
// Build the copied files, so the vendored source is sufficient to reproduce bytes.
const output = await build({ entryPoints: [resolve(target, "src/gridcraft-native.ts")], bundle: true, write: false, format: "esm", platform: "browser", target: "es2022", minify: true, legalComments: "none" })
const compiled = output.outputFiles[0].contents
await writeFile(resolve(target, "native-sdk.js"), compiled)
const commit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim()
await writeFile(resolve(target, "provenance.json"), JSON.stringify({ repository: "https://github.com/crewship-ai/crewship", commit, esbuild: version, sources, compiled_sha256: hash(compiled), build: "esbuild src/gridcraft-native.ts --bundle --format=esm --platform=browser --target=es2022 --minify --legal-comments=none --outfile=native-sdk.js" }, null, 2) + "\n")
console.log(`Vendored shared editor SDK ${commit}: ${hash(compiled)}`)
