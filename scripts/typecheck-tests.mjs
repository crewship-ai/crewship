#!/usr/bin/env node
import fs from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"
import ts from "typescript"
import { createHash } from "node:crypto"
import { execFileSync } from "node:child_process"

// Counts are per file, diagnostic code, message AND code-site anchor. Fixing one error cannot
// finance a different one, even when the overall total goes down. Line numbers
// are deliberately excluded so unrelated edits do not churn the baseline.
export function compareDiagnostics(actual, baseline) {
  const key = entry => JSON.stringify([entry.file, entry.code, entry.message, entry.anchor ?? null])
  const allowed = new Map(baseline.map(entry => [key(entry), entry.count]))
  return actual.filter(entry => entry.count > (allowed.get(key(entry)) ?? 0))
}

// Identify the code site, not its line number. Moving unchanged code keeps its
// identity; replacing an error in another test/declaration no longer spends it.
export function diagnosticAnchor(diagnostic) {
  if (!diagnostic.file || diagnostic.start === undefined) return "<configuration>"
  const file = diagnostic.file
  let node = file
  function descend(parent) {
    ts.forEachChild(parent, child => {
      if (child.getFullStart() <= diagnostic.start && child.end > diagnostic.start) {
        node = child
        descend(child)
      }
    })
  }
  descend(file)
  const context = []
  let statement
  for (let parent = node; parent && parent !== file; parent = parent.parent) {
    if (!statement && ts.isStatement(parent)) statement = parent.getText(file)
    if (ts.isCallExpression(parent) && parent.arguments.length && ts.isStringLiteralLike(parent.arguments[0])) {
      context.push(parent.expression.getText(file), parent.arguments[0].text)
    }
    if (ts.isFunctionDeclaration(parent) && parent.name) context.push(parent.name.text)
  }
  return createHash("sha256").update(JSON.stringify([statement ?? node.getText(file), context])).digest("hex")
}

export function compareBaseline(proposed, trusted) {
  // One-time upgrade of the old count-only baseline. It may gain anchors but
  // no existing file/code/message allowance may grow during that migration.
  if (trusted.every(entry => entry.anchor !== undefined)) return compareDiagnostics(proposed, trusted)
  const collapse = entries => {
    const grouped = new Map()
    for (const entry of entries) {
      const key = JSON.stringify([entry.file, entry.code, entry.message])
      const value = grouped.get(key) ?? {file:entry.file, code:entry.code, message:entry.message, count:0}
      value.count += entry.count
      grouped.set(key, value)
    }
    return [...grouped.values()]
  }
  return compareDiagnostics(collapse(proposed), collapse(trusted))
}

function main() {
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..")
  const configPath = path.join(root, "tsconfig.tests.json")
  const baselinePath = path.join(root, "scripts/test-types-baseline.json")
  const config = ts.readConfigFile(configPath, ts.sys.readFile)
  if (config.error) throw new Error(ts.flattenDiagnosticMessageText(config.error.messageText, "\n"))
  const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, root)
  if (parsed.errors.length) throw new Error(parsed.errors.map(e => ts.flattenDiagnosticMessageText(e.messageText, "\n")).join("\n"))
  const program = ts.createProgram(parsed.fileNames, parsed.options)
  const diagnostics = ts.getPreEmitDiagnostics(program)
  const grouped = new Map()
  for (const diagnostic of diagnostics) {
    const entry = {
      file: diagnostic.file ? path.relative(root, diagnostic.file.fileName).split(path.sep).join("/") : "<configuration>",
      code: diagnostic.code,
      message: ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n"),
      anchor: diagnosticAnchor(diagnostic),
      count: 0,
    }
    const key = JSON.stringify([entry.file, entry.code, entry.message, entry.anchor ?? null])
    const value = grouped.get(key) ?? entry
    value.count++
    grouped.set(key, value)
  }
  const entries = [...grouped.values()].sort((a, b) => a.file.localeCompare(b.file) || a.code - b.code || a.message.localeCompare(b.message))
  if (process.argv.includes("--write-baseline")) {
    fs.writeFileSync(baselinePath, JSON.stringify({ tracking_issue: 2493, diagnostics: entries }, null, 2) + "\n")
    console.log(`Recorded ${diagnostics.length} existing diagnostics in ${path.relative(root, baselinePath)}. Review baseline changes; this does not fix the errors.`)
    return
  }
  const baseline = JSON.parse(fs.readFileSync(baselinePath, "utf8"))
  const baseRef = process.env.TEST_TYPES_BASE_REF
  if (baseRef) {
    if (!/^[a-f0-9]{40}$/.test(baseRef)) throw new Error("TEST_TYPES_BASE_REF must be a commit SHA")
    const trusted = JSON.parse(execFileSync("git", ["show", `${baseRef}:scripts/test-types-baseline.json`], {cwd:root, encoding:"utf8"}))
    if (compareBaseline(baseline.diagnostics, trusted.diagnostics).length) {
      console.error("Test type baseline adds allowances relative to the target branch. Fix the errors instead.")
      process.exitCode = 1
    }
  }
  const added = compareDiagnostics(entries, baseline.diagnostics)
  if (added.length) {
    for (const entry of added) console.error(`${entry.file}: TS${entry.code} (${entry.count} occurrence(s)): ${entry.message}`)
    console.error("New test type errors. Fix them; do not raise the baseline to bypass this gate.")
    process.exitCode = 1
  } else {
    console.log(`Test types: no new diagnostics; ${diagnostics.length} existing errors remain (tracked in #2493).`)
    const removed = compareDiagnostics(baseline.diagnostics, entries)
    if (removed.length) console.log("Some baseline allowances are no longer needed. Run with --write-baseline to remove their entries.")
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
