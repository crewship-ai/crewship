#!/usr/bin/env node
// Keep the entire project context for every diagnostic. Bound checker caches
// by checking batches in sequential fresh processes, without raising V8 limits.
import path from "node:path"
import fs from "node:fs"
import crypto from "node:crypto"
import { spawnSync } from "node:child_process"
import { fileURLToPath } from "node:url"
import ts from "typescript"

function createProgram(configPath) {
  const root = path.dirname(configPath)
  const config = ts.readConfigFile(configPath, ts.sys.readFile)
  if (config.error) throw new Error(ts.flattenDiagnosticMessageText(config.error.messageText, "\n"))
  const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, root)
  if (parsed.errors.length) throw new Error(parsed.errors.map(e => ts.flattenDiagnosticMessageText(e.messageText, "\n")).join("\n"))
  return { root, program: ts.createProgram(parsed.fileNames, parsed.options) }
}

function normalizeDiagnostic(diagnostic, root) {
  const position = diagnostic.file && diagnostic.start !== undefined
    ? diagnostic.file.getLineAndCharacterOfPosition(diagnostic.start)
    : null
  return {
    file: diagnostic.file ? path.relative(root, diagnostic.file.fileName).split(path.sep).join("/") : "<configuration>",
    line: position ? position.line + 1 : null,
    code: diagnostic.code,
    message: ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n"),
  }
}

/** Reference implementation: every diagnostic tsc reports for this project. */
export function checkTestTypes(configPath) {
  const { root, program } = createProgram(configPath)
  return ts.getPreEmitDiagnostics(program).map(d => normalizeDiagnostic(d, root))
}

function encodeDiagnostic(d, root) {
  // Retain TypeScript's actual sort/dedup keys until all batches are collected.
  return {
    file: d.file ? { fileName: d.file.fileName, path: d.file.path } : undefined,
    start: d.start, length: d.length, code: d.code, category: d.category,
    messageText: d.messageText, canonicalHead: d.canonicalHead,
    relatedInformation: d.relatedInformation?.map(info => encodeDiagnostic(info, root)),
    normalized: normalizeDiagnostic(d, root),
  }
}

function worker(request) {
  const { root, program } = createProgram(request.configPath)
  const sources = program.getSourceFiles()
  const fingerprint = crypto.createHash("sha256")
    .update(JSON.stringify({ version: ts.version, roots: program.getRootFileNames(), options: program.getCompilerOptions() }))
  for (const source of sources) fingerprint.update(source.fileName).update("\0").update(source.text).update("\0")
  const graph = fingerprint.digest("hex")
  if (request.mode === "discover") {
    const diagnostics = [
      ...program.getConfigFileParsingDiagnostics(), ...program.getOptionsDiagnostics(),
      ...program.getSyntacticDiagnostics(), ...program.getGlobalDiagnostics(),
    ]
    return {
      graph,
      // skipLibCheck suppresses declaration-file semantic diagnostics, as in
      // TypeScript itself. Such files remain loaded into EVERY program, so
      // their globals and augmentations are never removed from the context.
      files: sources.filter(source => !(program.getCompilerOptions().skipLibCheck && source.isDeclarationFile)).map(source => source.fileName),
      diagnostics: diagnostics.map(d => encodeDiagnostic(d, root)),
    }
  }
  if (request.mode !== "batch" || request.graph !== graph || !Array.isArray(request.files)) {
    throw new Error("Test type project changed or invalid worker request; no checks accepted")
  }
  // Match getPreEmitDiagnostics: bind and diagnose the complete global scope
  // before requesting per-file diagnostics, including cross-root conflicts.
  program.getGlobalDiagnostics()
  const diagnostics = []
  for (const name of request.files) {
    const source = program.getSourceFile(name)
    if (!source) throw new Error(`Test type source disappeared: ${name}`)
    diagnostics.push(...program.getSemanticDiagnostics(source))
    if (program.getCompilerOptions().declaration || program.getCompilerOptions().composite) {
      diagnostics.push(...program.getDeclarationDiagnostics(source))
    }
  }
  return { graph, files: request.files, diagnostics: diagnostics.map(d => encodeDiagnostic(d, root)) }
}

function runWorker(request) {
  const child = spawnSync(process.execPath, [fileURLToPath(import.meta.url), "--worker"], {
    input: JSON.stringify(request), encoding: "utf8", maxBuffer: 64 * 1024 * 1024,
    // NODE_OPTIONS and the resource policy are inherited unchanged.
  })
  if (child.error || child.status !== 0 || child.signal) {
    throw new Error(`Test type worker failed (${child.signal ?? child.status ?? "spawn"}): ${child.error?.message ?? child.stderr}`)
  }
  let result
  try { result = JSON.parse(child.stdout) } catch { throw new Error("Invalid test type worker response") }
  if (!result || !Array.isArray(result.files) || !Array.isArray(result.diagnostics) || typeof result.graph !== "string") {
    throw new Error("Incomplete test type worker response")
  }
  return result
}

/** Same complete root set in every child; only the diagnostic work is batched. */
export function checkTestTypesBounded(configPath, batchSize = 128) {
  if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 128) throw new Error("Invalid test type batch size")
  configPath = path.resolve(configPath)
  const discovery = runWorker({ configPath, mode: "discover" })
  const diagnostics = [...discovery.diagnostics]
  const started = Date.now()
  console.error(`Test types: ${discovery.files.length} source files, complete graph ${discovery.graph}, batch ${batchSize}`)
  for (let offset = 0; offset < discovery.files.length; offset += batchSize) {
    const files = discovery.files.slice(offset, offset + batchSize)
    const result = runWorker({ configPath, mode: "batch", graph: discovery.graph, files })
    if (result.graph !== discovery.graph || JSON.stringify(result.files) !== JSON.stringify(files)) {
      throw new Error("Test type worker did not check the complete assigned batch")
    }
    diagnostics.push(...result.diagnostics)
    console.error(`Test types: checked ${Math.min(offset + batchSize, discovery.files.length)}/${discovery.files.length} (${((Date.now() - started) / 1000).toFixed(1)}s)`)
  }
  return ts.sortAndDeduplicateDiagnostics(diagnostics).map(d => d.normalized)
}

export function formatDiagnostic({ file, line, code, message }) {
  return `${file}${line === null ? "" : `:${line}`}: TS${code}: ${message}`
}

function main() {
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..")
  const diagnostics = checkTestTypesBounded(path.join(root, "tsconfig.tests.json"))
  if (diagnostics.length) {
    for (const diagnostic of diagnostics) console.error(formatDiagnostic(diagnostic))
    console.error(`${diagnostics.length} test type error(s). Fix the test (or the product type it exposes); this gate has no baseline.`)
    process.exitCode = 1
  } else {
    console.log("Test types: clean (complete project context, sequential bounded diagnostic batches).")
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv[2] === "--worker") {
    try { process.stdout.write(JSON.stringify(worker(JSON.parse(fs.readFileSync(0, "utf8"))))) }
    catch (error) { console.error(error instanceof Error ? error.message : String(error)); process.exitCode = 2 }
  } else main()
}
