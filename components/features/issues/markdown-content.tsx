"use client"

import { memo } from "react"
import { Streamdown } from "streamdown"
import { code } from "@streamdown/code"
import { cn } from "@/lib/utils"

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const plugins = { code } as any

interface MarkdownContentProps {
  children: string
  className?: string
  compact?: boolean
}

export const MarkdownContent = memo(function MarkdownContent({ children, className, compact }: MarkdownContentProps) {
  if (!children) return null

  return (
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
        // Code — emerald inline, dark bg for blocks
        "[&_code]:bg-emerald-500/10 [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-xs [&_code]:font-mono [&_code]:text-emerald-300",
        "[&_pre]:bg-[#1a1b26] [&_pre]:border [&_pre]:border-white/[0.08] [&_pre]:rounded-lg [&_pre]:p-3 [&_pre]:mb-3 [&_pre]:overflow-x-auto",
        "[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-xs [&_pre_code]:text-foreground/80",
        // Tables — more contrast
        "[&_table]:w-full [&_table]:text-xs [&_table]:mb-3",
        "[&_th]:text-left [&_th]:text-foreground/90 [&_th]:font-semibold [&_th]:py-1.5 [&_th]:px-2 [&_th]:border-b [&_th]:border-white/[0.1] [&_th]:bg-white/[0.02]",
        "[&_td]:py-1.5 [&_td]:px-2 [&_td]:border-b [&_td]:border-white/[0.04] [&_td]:text-foreground/70",
        // Links
        "[&_a]:text-blue-400 [&_a]:underline [&_a]:underline-offset-2 hover:[&_a]:text-blue-300",
        // Blockquotes — amber accent
        "[&_blockquote]:border-l-2 [&_blockquote]:border-amber-500/40 [&_blockquote]:pl-3 [&_blockquote]:text-foreground/60 [&_blockquote]:italic [&_blockquote]:my-2",
        // HR
        "[&_hr]:border-white/[0.06] [&_hr]:my-3",
        // Compact mode for right panel
        compact && "[&_p]:text-xs [&_h1]:text-sm [&_h2]:text-sm [&_h3]:text-xs [&_ul]:text-xs [&_ol]:text-xs [&_table]:text-[10px] [&_th]:py-1 [&_td]:py-1",
        className,
      )}
      plugins={plugins}
    >
      {children}
    </Streamdown>
  )
})
