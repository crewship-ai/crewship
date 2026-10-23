// Measure already-built web assets; invoke through crewship-heavy.
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { stripTypeScriptTypes } from "node:module";
import { createHash } from "node:crypto";
import { gzipSync, brotliCompressSync, constants } from "node:zlib";

const [bindingsDir, rawWasmPath, reportPath] = process.argv.slice(2);
if (!bindingsDir || !rawWasmPath || !reportPath) {
  throw new Error("Usage: node mooring/client/measure.mjs BINDINGS_DIR BEFORE_OPT_WASM REPORT_JSON");
}
const thin = stripTypeScriptTypes(readFileSync(new URL("./index.ts", import.meta.url), "utf8"), { mode: "transform" });
const stats = (data) => ({
  raw: data.length,
  gzip: gzipSync(data, { level: 9 }).length,
  brotli: brotliCompressSync(data, { params: { [constants.BROTLI_PARAM_QUALITY]: 11 } }).length,
  sha256: createHash("sha256").update(data).digest("hex"),
});
const assets = {
  wasm: stats(readFileSync(join(bindingsDir, "mooring_client_bg.wasm"))),
  glue: stats(readFileSync(join(bindingsDir, "mooring_client.js"))),
  thin_ts_runtime: stats(Buffer.from(thin)),
};
const totals = Object.fromEntries(["raw", "gzip", "brotli"].map((kind) => [kind, Object.values(assets).reduce((sum, value) => sum + value[kind], 0)]));
const report = {
  wasm_before_opt: stats(readFileSync(rawWasmPath)),
  assets,
  totals,
  method: "Per-asset gzip level 9 and Brotli quality 11; compile-time TypeScript types excluded, Node built-in TS transform for thin runtime.",
  limitations: "Minimal client primitives and fixture runner only. No production transport, reconnect, app adapter, dependency extractor, or complete SDK."
};
writeFileSync(reportPath, `${JSON.stringify(report, null, 2)}\n`);
console.log(JSON.stringify(report));
