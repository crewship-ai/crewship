"use client"

import * as React from "react"
import { FileText } from "lucide-react"
import Link from "next/link"

import { cn } from "@/lib/utils"
import { entityHref } from "./entity-href"
import { defaultEmptyHint, panelGate, provenanceProducedAt } from "./freshness"
import {
  FailedValue,
  NeverProducedValue,
  PanelAge,
  PanelFrame,
  PanelValue,
  resolveNow,
} from "./panel-frame"
import {
  NARRATIVE_BLOCK_KINDS,
  type EntityRef,
  type NarrativeBlock,
  type NarrativePayload,
  type PanelProps,
} from "./types"

/**
 * `narrative.v1` — typed prose blocks and an optional verdict (§3).
 *
 * This is the panel an AI agent writes, so §8 is its specification rather than
 * its background. Four of the ten rules are enforced HERE rather than at the
 * API boundary, because they are properties of the renderer:
 *
 *  1. The agent fills a schema; it never emits markup. There is no markdown
 *     parser in this file and no markup to parse — a block is a `kind` from a
 *     closed enum and a string, and the string becomes a React text node.
 *  2. No images. Not sanitised — there is no image field in the payload type,
 *     no `<img>` below, and nothing that takes a source. CamoLeak exfiltrated
 *     through a TRUSTED FIRST-PARTY image proxy, so an allow-list was never
 *     the control; having no element is.
 *  3. No free-form links. A block carries `ref: {kind, id}` and THIS FILE
 *     builds the href from a route table it owns. A ref whose kind is not in
 *     the table renders as plain text — never as a link to a path assembled
 *     from producer input. Slack AI's private-channel leak was a rendered
 *     link.
 * 10. Text renders through React elements, never `innerHTML`. There is no
 *     `dangerouslySetInnerHTML` here, and the panel-registry test greps this
 *     directory's source to keep it that way.
 *
 * Actions are absent on purpose (§12 v1: "narrative.v1, text only, no
 * actions"). Rules 4-7 govern them and the PageAction vocabulary of §8b owns
 * their server-verified click token; a button drawn from this payload would be
 * an agent authoring an action, which rule 4 forbids in as many words.
 */
export function NarrativePanel({ panel, data, now, publicView = false, className }: PanelProps) {
  const clock = resolveNow(now)
  const gate = panelGate(data)
  const payload = (data.payload ?? {}) as NarrativePayload
  const blocks = Array.isArray(payload.blocks) ? payload.blocks : []
  const verdict = typeof payload.verdict === "string" ? payload.verdict.trim() : ""

  let body: React.ReactNode
  if (gate.kind === "failed") {
    body = (
      <FailedValue
        failure={data.failure}
        publicView={publicView}
        producedAt={provenanceProducedAt(data.provenance)}
        now={clock}
      />
    )
  } else if (gate.kind === "never") {
    body = <NeverProducedValue hint={data.emptyHint?.trim() || defaultEmptyHint(panel)} />
  } else {
    body = (
      <div className="flex flex-col gap-2">
        {gate.dimmed ? (
          <PanelAge producedAt={provenanceProducedAt(data.provenance)} now={clock} />
        ) : null}
        <PanelValue basis="measured" dimmed={gate.dimmed} className="flex flex-col gap-2">
          {verdict ? (
            <p data-slot="narrative-verdict" className="text-body font-semibold text-foreground">
              {verdict}
            </p>
          ) : null}
          {blocks.length === 0 && !verdict ? (
            // A measured "the agent ran and had nothing to say". Not the em
            // dash: the producer did run, so there IS a basis — the basis is
            // that there was nothing to report (§9b.4).
            <p className="text-body text-muted-foreground">
              The agent produced no narrative in its latest push.
            </p>
          ) : (
            <NarrativeBlocks blocks={blocks} />
          )}
        </PanelValue>
      </div>
    )
  }

  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={clock}
      publicView={publicView}
      className={className}
      icon={FileText}
    >
      {body}
    </PanelFrame>
  )
}

/**
 * Blocks in order, with consecutive `list` blocks gathered into one `<ul>`.
 *
 * The schema gives a block exactly one string, so a list exists only as a run
 * of list blocks — which keeps the payload flat and keeps the agent from ever
 * describing nesting, indentation or any other layout decision. The host owns
 * the look and feel; that is Adaptive Cards' "no code behind" applied here.
 */
function NarrativeBlocks({ blocks }: { blocks: NarrativeBlock[] }) {
  const groups: { kind: "paragraph" | "list"; items: NarrativeBlock[]; at: number }[] = []
  blocks.forEach((block, i) => {
    // An unrecognised kind degrades to a paragraph. A payload is machine-
    // written and, per §8, may be agent-written: it does not get to select a
    // renderer this build does not have by inventing a third kind.
    const kind = isBlockKind(block?.kind) && block.kind === "list" ? "list" : "paragraph"
    const tail = groups[groups.length - 1]
    if (kind === "list" && tail?.kind === "list") tail.items.push(block)
    else groups.push({ kind, items: [block], at: i })
  })

  return (
    <div className="flex flex-col gap-2">
      {groups.map((group) =>
        group.kind === "list" ? (
          <ul
            key={group.at}
            data-slot="narrative-list"
            className="flex list-disc flex-col gap-1 pl-4 text-body text-muted-foreground marker:text-muted-foreground-soft"
          >
            {group.items.map((item, i) => (
              <li key={i} data-slot="narrative-block" data-kind="list" className="min-w-0 break-words">
                <BlockText block={item} />
              </li>
            ))}
          </ul>
        ) : (
          <p
            key={group.at}
            data-slot="narrative-block"
            data-kind="paragraph"
            className="min-w-0 break-words text-body text-muted-foreground"
          >
            <BlockText block={group.items[0]} />
          </p>
        ),
      )}
    </div>
  )
}

/**
 * One block's text, plus its entity reference if it has one.
 *
 * `{text}` is a React child, so every character in it is a character: an angle
 * bracket is an angle bracket and a `<script>` is five words. That is rule 10,
 * and it is why rule 1's "never emits markup" needs no sanitiser on this side
 * — there is nothing here that would interpret markup if it arrived.
 */
function BlockText({ block }: { block: NarrativeBlock }) {
  const text = typeof block?.text === "string" ? block.text : ""
  return (
    <>
      {text}
      <EntityRefLink refValue={block?.ref} />
    </>
  )
}

const NARRATIVE_BLOCK_KIND_SET: ReadonlySet<string> = new Set<string>(NARRATIVE_BLOCK_KINDS)

function isBlockKind(value: unknown): value is (typeof NARRATIVE_BLOCK_KINDS)[number] {
  return typeof value === "string" && NARRATIVE_BLOCK_KIND_SET.has(value)
}

/**
 * §8 rule 3, the permitted half: the payload names an entity, `entityHref`
 * builds the URL.
 *
 * The resolver lives in `entity-href.ts` because §8b.1's `kind: "link"` action
 * needs the identical rules — closed kind set, refused relative id, encoded
 * before interpolation — and two copies of that is two chances to weaken one.
 * A ref whose kind is unknown, or whose id is empty, renders as nothing: a
 * producer that cannot name a real entity does not get a link built out of what
 * it did send.
 */
function EntityRefLink({ refValue }: { refValue?: EntityRef | null }) {
  const href = entityHref(refValue)
  if (!href) return null
  const kind = refValue!.kind as string
  const id = (refValue!.id as string).trim()

  return (
    <>
      {" "}
      <Link
        data-slot="narrative-ref"
        data-ref-kind={kind}
        data-ref-id={id}
        href={href}
        className={cn(
          "font-medium text-primary underline-offset-2 hover:underline",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
      >
        {`${kind} ${id}`}
      </Link>
    </>
  )
}
