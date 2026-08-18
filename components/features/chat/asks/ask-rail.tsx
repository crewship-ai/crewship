"use client"

import { useEffect, useRef, useState, type ReactNode } from "react"
import { motion } from "motion/react"
import { ClipboardList } from "lucide-react"

import { Suggestion, Suggestions } from "@/components/ai-elements/suggestion"
import { spring, stagger } from "@/lib/motion"
import { emitChatEvent, emitChatEventOnce, hashedId } from "@/lib/telemetry"
import { cn } from "@/lib/utils"

import { MAX_ASK_LABEL_LENGTH, truncateAskLabel, type AskForm } from "./types"

/**
 * The bottom rail: prepared questions and questionnaire forms, side by side.
 *
 * The one visual rule this component exists to enforce (PRD §5.1):
 *
 *   **A chip that opens something must not look like a chip that sends
 *   something.**
 *
 * A question chip sends its text the instant it is clicked. A form chip opens
 * a sheet and sends nothing. A user who taps "Add a receipt", expecting a
 * form, and finds a message already on its way to the agent has been lied to
 * — and it is not recoverable, because the message is gone.
 *
 * So a form chip is marked four ways, none of which is a colour or a class:
 *
 *   1. `aria-haspopup="dialog"` — the assistive-technology contract.
 *   2. An accessible name that says "opens a form" in words.
 *   3. A visible form glyph.
 *   4. A trailing ellipsis, the long-standing convention for "this opens
 *      something else before it does anything".
 *
 * Overflow: the rail shows at most `limit` chips (6 on a cold start — two rows
 * at 1280px — and 3 as follow-ups) and collapses the remainder into `+N`,
 * which opens the full catalogue. Everything stays reachable; the rail just
 * stops being a wall.
 *
 * With no forms and nothing to overflow, this renders the same `Suggestions` /
 * `Suggestion` pair the chat rendered before ask forms existed. That is a
 * requirement, not a coincidence: an agent nobody has configured must behave
 * exactly as it does today.
 */

export interface AskRailProps {
  /** Plain prompts — clicking one sends it. */
  questions: string[]
  /** Form definitions — clicking one opens the sheet. */
  forms: AskForm[]
  /** Chips shown before the rest collapses into `+N`. */
  limit: number
  onPickQuestion: (text: string) => void
  onPickForm: (form: AskForm) => void
  disabled?: boolean
  className?: string
  /** Stagger the chips in. Follow-ups have always done this; the cold-start
   *  rail has never done it, and this feature is not the place to change
   *  either of them. */
  animateChips?: boolean

  /* -- Measurement (lib/telemetry.ts). None of it changes what renders. ---- */

  /** Context for the chip events. Optional because the rail is mounted by
   *  chat-panel.tsx, which owns these ids and does not pass them yet; without
   *  them the events still carry chip identity, kind and position, which is
   *  the funnel. See docs/guides/chat-telemetry. */
  sessionId?: string
  agentId?: string
  /** Which list these chips came from. The cold-start rail and the follow-ups
   *  rail are two different offers at two different moments, and the whole
   *  question "do chips start conversations" is unanswerable if they are
   *  counted as one. */
  chipSource?: "pack" | "fallback" | "followup"
}

const CHIP_VARIANTS = {
  hidden: { opacity: 0, scale: 0.95, y: 4 },
  show: { opacity: 1, scale: 1, y: 0 },
}

type RailItem =
  | { kind: "question"; key: string; index: number; label: string; text: string }
  | { kind: "form"; key: string; label: string; form: AskForm }

/**
 * What a chip is called in telemetry.
 *
 * A form has a row and therefore an id. A question is only ever a string an
 * author wrote, so it is fingerprinted rather than carried: the same question
 * is the same chip across sessions and machines, and the question itself never
 * reaches an event. Never the label, which is the same text truncated.
 */
const chipId = (item: RailItem) => (item.kind === "form" ? item.form.id : hashedId("q", item.text))

export function AskRail({
  questions,
  forms,
  limit,
  onPickQuestion,
  onPickForm,
  disabled,
  className,
  animateChips,
  sessionId,
  agentId,
  chipSource = "pack",
}: AskRailProps) {
  const [overflowOpen, setOverflowOpen] = useState(false)
  const overflowRef = useRef<HTMLDivElement | null>(null)

  // Forms lead. They are the ones somebody sat down and authored for this
  // agent; a static question is the cheaper thing and can wait a slot.
  const items: RailItem[] = [
    ...forms.map<RailItem>((form) => ({
      kind: "form",
      key: `form-${form.id}`,
      label: form.label,
      form,
    })),
    ...questions.map<RailItem>((text, i) => ({
      kind: "question",
      key: `question-${i}`,
      index: i,
      label: text,
      text,
    })),
  ]

  // Close the catalogue when the rail's contents change out from under it
  // (a session swap, an assistant turn arriving) — a panel left open over a
  // list that no longer matches it is a lie about what is on offer.
  useEffect(() => {
    setOverflowOpen(false)
  }, [items.length])

  const shown = items.slice(0, limit)
  const hiddenCount = items.length - shown.length

  // What a person could actually see: the rail, plus the catalogue while it is
  // open. Counting the chips behind `+N` as shown would put every configured
  // ask in the denominator of "shown vs clicked" and make the rate meaningless.
  const visible = overflowOpen ? items : shown

  // One impression per chip per page, not per render. React renders the rail
  // on every keystroke in the composer below it; without the dedupe the
  // numerator of the chip funnel would be a render count.
  useEffect(() => {
    visible.forEach((item, position) => {
      emitChatEventOnce(`${sessionId ?? "-"}:${item.key}`, "ask_chip_shown", {
        session_id: sessionId,
        agent_id: agentId,
        chip_id: chipId(item),
        chip_kind: item.kind,
        position,
        source: chipSource,
      })
    })
  }, [visible, sessionId, agentId, chipSource])

  if (items.length === 0) return null

  const pick = (item: RailItem) => {
    emitChatEvent("ask_chip_clicked", {
      session_id: sessionId,
      agent_id: agentId,
      chip_id: chipId(item),
      chip_kind: item.kind,
      position: items.indexOf(item),
      source: chipSource,
    })
    setOverflowOpen(false)
    if (item.kind === "form") onPickForm(item.form)
    else onPickQuestion(item.text)
  }

  const chip = (key: string, node: ReactNode) =>
    animateChips ? (
      <motion.div key={key} variants={CHIP_VARIANTS} transition={spring.snappy}>
        {node}
      </motion.div>
    ) : (
      node
    )

  const chips = (
    <>
      {shown.map((item) =>
        item.kind === "form"
          ? chip(
              item.key,
              <FormChip
                key={item.key}
                form={item.form}
                disabled={disabled}
                onClick={() => pick(item)}
              />,
            )
          : chip(
              item.key,
              <Suggestion
                key={item.key}
                suggestion={item.label}
                data-testid={`ask-chip-question-${item.index}`}
                title={item.label.length > MAX_ASK_LABEL_LENGTH ? item.label : undefined}
                disabled={disabled}
                onClick={() => pick(item)}
              >
                {truncateAskLabel(item.label)}
              </Suggestion>,
            ),
      )}
      {hiddenCount > 0 &&
        chip(
          "more",
          <Suggestion
            key="more"
            suggestion={`+${hiddenCount}`}
            data-testid="ask-rail-more"
            aria-expanded={overflowOpen}
            aria-haspopup="listbox"
            aria-label={`Show all ${items.length} asks`}
            disabled={disabled}
            onClick={() => setOverflowOpen((v) => !v)}
          >
            +{hiddenCount}
          </Suggestion>,
        )}
    </>
  )

  return (
    <div className={cn("relative", className)} data-testid="ask-rail">
      {animateChips ? (
        <motion.div
          variants={{ show: stagger.chips, hidden: {} }}
          initial="hidden"
          animate="show"
          className="flex flex-wrap items-center gap-1.5"
        >
          {chips}
        </motion.div>
      ) : (
        <Suggestions>{chips}</Suggestions>
      )}

      {overflowOpen && (
        <div
          ref={overflowRef}
          data-testid="ask-rail-overflow"
          role="listbox"
          aria-label="All asks"
          // Grows UPWARD, out of the rail, for the same reason the form sheet
          // does: this rail sits directly above the composer, and a panel that
          // opened downward would cover the input it is offering to fill.
          className="absolute bottom-full left-0 z-30 mb-2 max-h-64 w-full max-w-sm overflow-y-auto rounded-lg border bg-popover p-1 shadow-md"
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.stopPropagation()
              setOverflowOpen(false)
            }
          }}
        >
          {items.map((item) => (
            <button
              key={item.key}
              type="button"
              role="option"
              aria-selected={false}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent"
              onClick={() => pick(item)}
            >
              {item.kind === "form" && (
                <ClipboardList className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
              )}
              <span className="truncate">{item.label}</span>
              {item.kind === "form" && <span aria-hidden="true">…</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function FormChip({
  form,
  disabled,
  onClick,
}: {
  form: AskForm
  disabled?: boolean
  onClick: () => void
}) {
  const label = truncateAskLabel(form.label)
  return (
    <Suggestion
      suggestion={form.label}
      data-testid={`ask-chip-form-${form.id}`}
      // The three non-visual halves of "this opens, it does not send".
      aria-haspopup="dialog"
      aria-label={`${form.label} — opens a form`}
      title={form.label.length > MAX_ASK_LABEL_LENGTH ? form.label : undefined}
      disabled={disabled}
      onClick={onClick}
      className="gap-1.5"
    >
      <ClipboardList className="h-3.5 w-3.5" aria-hidden="true" data-testid="ask-chip-glyph" />
      <span>{label}</span>
      {/* The visual half. aria-hidden because the accessible name already
          says "opens a form" in words — a screen reader announcing a bare
          ellipsis says nothing useful. */}
      <span aria-hidden="true">…</span>
    </Suggestion>
  )
}
