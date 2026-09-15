#!/usr/bin/env node
// Type-checks the test files (tsconfig.tests.json) and fails on any
// diagnostic. There is no baseline: the debt #2493 was filed for is paid,
// and a test that renders a component without a required prop, or asserts
// against a wire shape the product does not have, fails here.
import path from "node:path"
import { fileURLToPath } from "node:url"
import ts from "typescript"

/** Every diagnostic tsc would report for the project at configPath. */
export function checkTestTypes(configPath) {
  const root = path.dirname(configPath)
  const config = ts.readConfigFile(configPath, ts.sys.readFile)
  if (config.error) throw new Error(ts.flattenDiagnosticMessageText(config.error.messageText, "\n"))
  const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, root)
  if (parsed.errors.length) throw new Error(parsed.errors.map(e => ts.flattenDiagnosticMessageText(e.messageText, "\n")).join("\n"))
  const program = ts.createProgram(parsed.fileNames, parsed.options)
  return ts.getPreEmitDiagnostics(program).map(diagnostic => {
    const position = diagnostic.file && diagnostic.start !== undefined
      ? diagnostic.file.getLineAndCharacterOfPosition(diagnostic.start)
      : null
    return {
      file: diagnostic.file ? path.relative(root, diagnostic.file.fileName).split(path.sep).join("/") : "<configuration>",
      line: position ? position.line + 1 : null,
      code: diagnostic.code,
      message: ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n"),
    }
  })
}

export function formatDiagnostic({ file, line, code, message }) {
  return `${file}${line === null ? "" : `:${line}`}: TS${code}: ${message}`
}

function main() {
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..")
  const diagnostics = checkTestTypes(path.join(root, "tsconfig.tests.json"))
  if (diagnostics.length) {
    for (const diagnostic of diagnostics) console.error(formatDiagnostic(diagnostic))
    console.error(`${diagnostics.length} test type error(s). Fix the test (or the product type it exposes); this gate has no baseline.`)
    process.exitCode = 1
  } else {
    console.log("Test types: clean.")
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
