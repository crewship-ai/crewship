'use client'

import { useState } from 'react'
import { Check, Copy, Pencil } from 'lucide-react'
import { cn } from '@/lib/utils'
import hljs from 'highlight.js/lib/core'
import yaml from 'highlight.js/lib/languages/yaml'
import json from 'highlight.js/lib/languages/json'

// Register languages
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('json', json)

interface CodeBlockProps {
  code: string
  language?: 'yaml' | 'json'
  className?: string
  showCopy?: boolean
  onEdit?: () => void
}

export function CodeBlock({ 
  code, 
  language = 'yaml', 
  className,
  showCopy = true,
  onEdit,
}: CodeBlockProps) {
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    await navigator.clipboard.writeText(code)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const highlightedCode = code 
    ? hljs.highlight(code, { language }).value
    : `<span class="hljs-comment"># No content</span>`

  return (
    <div className={cn("rounded-lg border border-border flex flex-col", className)}>
      {/* Header - fixed at top, never scrolls */}
      <div className="flex items-center justify-between px-4 py-2 bg-muted/50 border-b border-border shrink-0">
        <span className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          {language}
        </span>
        <div className="flex items-center gap-3">
          {onEdit && (
            <button
              type="button"
              onClick={onEdit}
              className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              <Pencil className="h-3.5 w-3.5" />
              <span>Edit</span>
            </button>
          )}
          {showCopy && (
            <button
              type="button"
              onClick={handleCopy}
              className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              {copied ? (
                <>
                  <Check className="h-3.5 w-3.5 text-primary" />
                  <span>Copied!</span>
                </>
              ) : (
                <>
                  <Copy className="h-3.5 w-3.5" />
                  <span>Copy</span>
                </>
              )}
            </button>
          )}
        </div>
      </div>
      {/* Code - this is what scrolls */}
      <pre className="text-xs p-4 overflow-y-auto font-mono bg-card/80 m-0 whitespace-pre-wrap break-words flex-1">
        <code 
          className={cn("language-" + language, "code-block-content")}
          dangerouslySetInnerHTML={{ __html: highlightedCode }}
        />
      </pre>
      {/* Custom syntax highlighting - VSCode-like palette */}
      <style jsx>{`
        .code-block-content {
          color: hsl(var(--foreground));
        }
        /* Keys/attributes - cyan/teal */
        .code-block-content :global(.hljs-attr),
        .code-block-content :global(.hljs-attribute) {
          color: #9cdcfe;
        }
        /* String values - orange/coral */
        .code-block-content :global(.hljs-string) {
          color: #ce9178;
        }
        /* Literal values (true, false, null) - blue */
        .code-block-content :global(.hljs-literal) {
          color: #569cd6;
        }
        /* Numbers - light green */
        .code-block-content :global(.hljs-number) {
          color: #b5cea8;
        }
        /* Comments - gray italic */
        .code-block-content :global(.hljs-comment) {
          color: #6a9955;
          font-style: italic;
        }
        /* Keywords - purple/magenta */
        .code-block-content :global(.hljs-keyword),
        .code-block-content :global(.hljs-built_in) {
          color: #c586c0;
        }
        /* Punctuation - subtle gray */
        .code-block-content :global(.hljs-punctuation) {
          color: #808080;
        }
        /* Type markers like 'account', 'campaign' - yellow */
        .code-block-content :global(.hljs-type) {
          color: #dcdcaa;
        }
      `}</style>
    </div>
  )
}
