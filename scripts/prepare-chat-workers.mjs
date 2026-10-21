// Bundle local workers explicitly: Turbopack treats new URL("*.ts") as a raw
// static asset rather than compiling its TypeScript/import graph as a worker.
import { build } from "esbuild"
import { readFile } from "node:fs/promises"

// Preserve MIT notices in the distributed static worker, not only node_modules.
const licenses = await Promise.all(["read-excel-file", "fflate", "saxen"].map(async name => `${name}\n${await readFile(`node_modules/${name}/LICENSE`, "utf8")}`))

for (const name of ["table", "xlsx"]) await build({
  entryPoints: [`components/features/chat/artifact/${name}-worker.ts`],
  outfile: `public/chat-workers/${name}-v1.js`,
  bundle: true,
  platform: "browser",
  format: "esm",
  target: "es2020",
  minify: true,
  legalComments: "eof",
  banner: name === "xlsx" ? { js: `/*!\n${licenses.join("\n\n")}\n*/` } : undefined,
})
