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
        // Code
        "[&_code]:bg-white/[0.06] [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-xs [&_code]:font-mono [&_code]:text-blue-300",
        "[&_pre]:bg-white/[0.04] [&_pre]:border [&_pre]:border-white/[0.06] [&_pre]:rounded-lg [&_pre]:p-3 [&_pre]:mb-3 [&_pre]:overflow-x-auto",
        "[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-xs",
        // Tables
        "[&_table]:w-full [&_table]:text-xs [&_table]:mb-3",
        "[&_th]:text-left [&_th]:text-muted-foreground [&_th]:font-medium [&_th]:py-1.5 [&_th]:px-2 [&_th]:border-b [&_th]:border-white/[0.08]",
        "[&_td]:py-1.5 [&_td]:px-2 [&_td]:border-b [&_td]:border-white/[0.04] [&_td]:text-foreground/70",
        // Links
        "[&_a]:text-blue-400 [&_a]:underline [&_a]:underline-offset-2 hover:[&_a]:text-blue-300",
        // Blockquotes
        "[&_blockquote]:border-l-2 [&_blockquote]:border-blue-500/30 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_blockquote]:italic",
        // HR
        "[&_hr]:border-white/[0.06] [&_hr]:my-3",
        // Compact mode for right panel
        compact && "[&_p]:text-xs [&_h1]:text-sm [&_h2]:text-sm [&_h3]:text-xs [&_ul]:text-xs [&_ol]:text-xs",
        className,
      )}
      plugins={plugins}
    >
      {children}
    </Streamdown>
  )
})
