// CodeMirror extensions that make the routine DSL editable rather than
// merely typeable: inline diagnostics and schema-driven completion.
//
// Most of "JSON is hard to write" is not the punctuation — it is
// writing it blind. You cannot see that `htpp` is not a step kind, or
// remember whether the field is `items` or `over`, until a save fails.
// The schema knows both. Wiring it in removes the guessing without
// changing anything about the format.

import { autocompletion, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete"
import { linter, type Diagnostic } from "@codemirror/lint"
import type { Extension } from "@codemirror/state"
import type { EditorView } from "@codemirror/view"

import { diagnose } from "./routine-dsl-diagnostics"
import type { DslFormat } from "./routine-dsl-format"
import { keysForKind, stepKeys, stepKinds, topLevelKeys } from "./routine-dsl-schema"

/**
 * Inline error markers, from the DSL's own semantics.
 *
 * Maps our 1-indexed lines onto document positions, and marks the whole
 * line rather than a guessed span: a squiggle under an arbitrary
 * substring reads as precision the diagnostic does not have.
 */
export function dslLinter(format: DslFormat): Extension {
  return linter((view: EditorView): Diagnostic[] => {
    const text = view.state.doc.toString()
    const out: Diagnostic[] = []
    for (const d of diagnose(text, format)) {
      const lineNo = Math.min(Math.max(d.line, 1), view.state.doc.lines)
      const line = view.state.doc.line(lineNo)
      out.push({
        from: line.from,
        to: line.to,
        severity: d.severity,
        message: d.message,
      })
    }
    return out
  })
}

// `type:` on this line, capturing whatever has been typed after it.
// Handles both formats: `type: htt` and `"type": "htt`.
const TYPE_LINE = /(?:^|[{,\s])"?type"?\s*:\s*"?([A-Za-z_]*)$/

/** Nearest `type:` above the cursor — which kind's body we are inside. */
function enclosingKind(view: EditorView, pos: number): string | null {
  const upto = view.state.doc.sliceString(0, pos)
  const matches = [...upto.matchAll(/"?type"?\s*:\s*"?([a-z_]+)"?/g)]
  const last = matches[matches.length - 1]
  return last ? last[1] : null
}

/**
 * Completion for step kinds and field names.
 *
 * Three contexts, in the order they are checked: after `type:` offer
 * the ten kinds; inside a known kind's body offer that kind's fields;
 * otherwise offer the step-level and routine-level fields together.
 * The last one is deliberately unfussy — distinguishing "inside a step"
 * from "at the routine root" reliably needs a full parse of a buffer
 * that is, by definition, mid-edit.
 */
export function dslCompletion(): Extension {
  return autocompletion({
    override: [
      (context: CompletionContext): CompletionResult | null => {
        const line = context.state.doc.lineAt(context.pos)
        const before = line.text.slice(0, context.pos - line.from)

        const typeMatch = before.match(TYPE_LINE)
        if (typeMatch) {
          return {
            from: context.pos - typeMatch[1].length,
            options: stepKinds().map((k) => ({
              label: k.kind,
              type: "enum",
              detail: k.detail,
            })),
            validFor: /^[A-Za-z_]*$/,
          }
        }

        const word = context.matchBefore(/[A-Za-z_$][\w$]*/)
        if (!word && !context.explicit) return null
        const from = word ? word.from : context.pos

        const kind = enclosingKind(context.view!, context.pos)
        const bodyKeys = kind ? keysForKind(kind) : []
        const items = bodyKeys.length > 0 ? bodyKeys : [...stepKeys(), ...topLevelKeys()]

        return {
          from,
          options: items.map((k) => ({
            label: k.key,
            type: "property",
            detail: k.required ? `${k.detail} (required)`.trim() : k.detail,
            // Required fields first, then alphabetical — the fields you
            // cannot omit should not be buried under optional ones.
            boost: k.required ? 1 : 0,
          })),
          validFor: /^[\w$]*$/,
        }
      },
    ],
  })
}

/** Everything a routine DSL buffer wants, in one call. */
export function routineDslExtensions(format: DslFormat): Extension[] {
  return [dslLinter(format), dslCompletion()]
}
