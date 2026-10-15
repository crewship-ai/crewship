// Bundle local workers explicitly: Turbopack treats new URL("*.ts") as a raw
// static asset rather than compiling its TypeScript/import graph as a worker.
import { build } from "esbuild"

await build({
  entryPoints: ["components/features/chat/artifact/table-worker.ts"],
  outfile: "public/chat-workers/table-v1.js",
  bundle: true,
  platform: "browser",
  format: "esm",
  target: "es2020",
  minify: true,
  legalComments: "eof",
})
