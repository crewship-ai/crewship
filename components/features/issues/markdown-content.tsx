"use client"

import { memo } from "react"
import { Streamdown } from "streamdown"
import { code } from "@streamdown/code"
import { cn } from "@/lib/utils"
import { parseMentionUrl, type MentionDirectory } from "@/lib/mentions"
import { MentionDirectoryProvider, ResolvedMention } from "./mention-chip"

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const plugins = { code } as any

/* ------------------------------------------------------------------ *
 *  @mentions                                                          *
 * ------------------------------------------------------------------ */

/**
 * The element name a mention becomes on its way through the renderer.
 *
 * Random, once per module load, and it never reaches the DOM — the component
 * below replaces it with a `<span>`. That is deliberate, and it is the third
 * leg of the forgery defence:
 *
 *   Streamdown renders a *sanitised* subset of raw HTML, and `allowedTags`
 *   widens that allow-list. Widen it with a fixed name (`<crewship-mention>`)
 *   and a comment body containing that literal tag renders a chip — the one
 *   thing the brief says must never happen. Widen it with a name nobody can
 *   observe or predict and a typed `<crewship-mention …>` is an unknown tag
 *   again: stripped to its text, like every other tag we do not allow.
 *
 * Nothing depends on the value, so a different nonce per process (server
 * render vs. client) is fine — there is no hydration mismatch to have,
 * because the tag is gone before either side emits HTML.
 */
const MENTION_TAG = `crewship-mention-${mentionNonce()}`

function mentionNonce(): string {
  const bytes = new Uint8Array(8)
  const c: Crypto | undefined = globalThis.crypto
  if (typeof c?.getRandomValues === "function") {
    c.getRandomValues(bytes)
  } else {
    for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  }
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")
}

/**
 * `hProperties: { dataAgentId }` reaches the component as `data-agent-id`;
 * the sanitiser's allow-list is keyed by the hast property name, which is the
 * camel-cased one. Listing anything else silently drops the attribute.
 */
const MENTION_ALLOWED_TAGS = { [MENTION_TAG]: ["dataAgentId"] }

const MENTION_COMPONENTS = {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  [MENTION_TAG]: ((props: any) => (
    <ResolvedMention agentId={String(props["data-agent-id"] ?? "")} label={flatten(props.children)} />
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  )) as any,
}

/** The label the author wrote, as a string. Rendered as text, never markup. */
function flatten(children: unknown): string {
  if (typeof children === "string") return children.replace(/^@/, "")
  if (Array.isArray(children)) return children.map(flatten).join("")
  return ""
}

/* eslint-disable @typescript-eslint/no-explicit-any */
/**
 * Turn `[@slug](crewship:agent/<id>)` links into mention elements.
 *
 * It runs on the *parsed* document, not the raw text, which is why a mention
 * inside a code span or a fenced block is left alone: those are `inlineCode`
 * and `code` nodes, not links. A regex over the body cannot tell the
 * difference, and would make writing documentation of this syntax fire a
 * trigger.
 */
function remarkCrewshipMentions() {
  return (tree: any) => {
    walk(tree)
  }
}

function walk(node: any): void {
  const children = node?.children
  if (!Array.isArray(children)) return
  for (let i = 0; i < children.length; i++) {
    const child = children[i]
    if (child?.type === "link") {
      const agentId = parseMentionUrl(child.url)
      if (agentId) {
        children[i] = {
          type: "crewshipMention",
          data: {
            hName: MENTION_TAG,
            hProperties: { dataAgentId: agentId },
          },
          children: [{ type: "text", value: mdastText(child) }],
        }
        continue
      }
    }
    walk(child)
  }
}

function mdastText(node: any): string {
  if (typeof node?.value === "string") return node.value
  if (Array.isArray(node?.children)) return node.children.map(mdastText).join("")
  return ""
}
/* eslint-enable @typescript-eslint/no-explicit-any */

const MENTION_REMARK_PLUGINS = [remarkCrewshipMentions]

/* ------------------------------------------------------------------ */

interface MarkdownContentProps {
  children: string
  className?: string
  compact?: boolean
  /**
   * Agents this body may mention, by id. Supply it and a mention renders as a
   * chip; omit it and every mention degrades to the plain `@slug` its author
   * typed. There is no third behaviour — a mention is never rendered from the
   * body's own claims about who it addresses.
   */
  mentions?: MentionDirectory | null
}

export const MarkdownContent = memo(function MarkdownContent({ children, className, compact, mentions }: MarkdownContentProps) {
  if (!children) return null

  return (
    <MentionDirectoryProvider directory={mentions}>
      <Streamdown
        className={cn(
        "prose prose-invert max-w-none",
        "[&>*:first-child]:mt-0 [&>*:last-child]:mb-0",
        // Headings
        "[&_h1]:text-lg [&_h1]:font-semibold [&_h1]:text-foreground [&_h1]:mb-2 [&_h1]:mt-4",
        "[&_h2]:text-base [&_h2]:font-semibold [&_h2]:text-foreground [&_h2]:mb-2 [&_h2]:mt-3",
        "[&_h3]:text-sm [&_h3]:font-semibold [&_h3]:text-foreground [&_h3]:mb-1 [&_h3]:mt-2",
        // Text
        "[&_p]:text-sm [&_p]:text-foreground/80 [&_p]:leading-relaxed [&_p]:mb-2",
        "[&_strong]:text-foreground [&_strong]:font-semibold",
        "[&_em]:text-foreground/70",
        // Lists
        "[&_ul]:text-sm [&_ul]:text-foreground/80 [&_ul]:pl-4 [&_ul]:mb-2",
        "[&_ol]:text-sm [&_ol]:text-foreground/80 [&_ol]:pl-4 [&_ol]:mb-2",
        "[&_li]:mb-0.5",
        // Code — success inline, dark bg for blocks
        "[&_code]:bg-success/10 [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-xs [&_code]:font-mono [&_code]:text-success",
        "[&_pre]:bg-[#0d1117] [&_pre]:border [&_pre]:border-white/[0.08] [&_pre]:rounded-lg [&_pre]:p-3 [&_pre]:mb-3 [&_pre]:overflow-x-auto",
        "[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-xs [&_pre_code]:font-mono",
        // Tables — more contrast
        "[&_table]:w-full [&_table]:text-xs [&_table]:mb-3",
        "[&_th]:text-left [&_th]:text-foreground/90 [&_th]:font-semibold [&_th]:py-1.5 [&_th]:px-2 [&_th]:border-b [&_th]:border-white/[0.1] [&_th]:bg-white/[0.02]",
        "[&_td]:py-1.5 [&_td]:px-2 [&_td]:border-b [&_td]:border-white/[0.04] [&_td]:text-foreground/70",
        // Links
        "[&_a]:text-primary [&_a]:underline [&_a]:underline-offset-2 hover:[&_a]:text-primary/80",
        // Blockquotes — amber accent
        "[&_blockquote]:border-l-2 [&_blockquote]:border-warn/40 [&_blockquote]:pl-3 [&_blockquote]:text-foreground/60 [&_blockquote]:italic [&_blockquote]:my-2",
        // HR
        "[&_hr]:border-white/[0.06] [&_hr]:my-3",
        // Compact mode for right panel
        compact && "[&_p]:text-xs [&_h1]:text-sm [&_h2]:text-sm [&_h3]:text-xs [&_ul]:text-xs [&_ol]:text-xs [&_table]:text-[10px] [&_th]:py-1 [&_td]:py-1",
        className,
        )}
        plugins={plugins}
        remarkPlugins={MENTION_REMARK_PLUGINS}
        allowedTags={MENTION_ALLOWED_TAGS}
        components={MENTION_COMPONENTS}
      >
        {children}
      </Streamdown>
    </MentionDirectoryProvider>
  )
})
