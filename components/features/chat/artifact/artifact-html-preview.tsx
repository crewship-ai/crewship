"use client"

import { useMemo } from "react"
import { marked } from "marked"
import { useStaticArtifactDocument } from "@/hooks/use-static-artifact-document"

const markdownStyle = '<style>body{box-sizing:border-box;max-width:800px;min-height:100%;margin:0 auto;padding:40px 48px;background:#fff;color:#242932;font:15px/1.6 system-ui,sans-serif}h1,h2,h3{line-height:1.25}h1{font-size:28px}h2{font-size:20px;margin-top:30px}table{border-collapse:collapse;width:100%}th,td{border:1px solid #d9dfe6;padding:7px;text-align:left}blockquote{border-left:3px solid #4c9aff;padding-left:12px;color:#526070}code{background:#f1f3f6;padding:2px 4px}a{color:#276ac2}</style>'

/** Chat files use exactly the same inert raster profile as Pages and Board pins. */
export function ArtifactHTMLPreview({ path, text }: { path: string; text: string }) {
  const html = useMemo(() => /\.md$/i.test(path) ? markdownStyle + marked.parse(text, { async: false }) : text, [path, text])
  const document = useStaticArtifactDocument(html)
  return <iframe key={document ? "ready" : "pending"} title={`Preview ${path}`} sandbox="" referrerPolicy="no-referrer" className="h-full w-full bg-white" srcDoc={document} />
}
