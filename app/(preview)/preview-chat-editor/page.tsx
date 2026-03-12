"use client"

import { useState } from "react"
import {
  FolderClosed,
  FolderOpen,
  FileText,
  FileCode,
  FileJson,
  FileArchive,
  ChevronRight,
  ChevronDown,
  X,
  Save,
  Maximize2,
  Minimize2,
  Files,
  Sparkles,
  Users,
  MessageSquare,
} from "lucide-react"

/* ── fake data ──────────────────────────────────────── */

interface TreeNode {
  name: string
  isDir: boolean
  size?: string
  children?: TreeNode[]
}

const TREE: TreeNode[] = [
  {
    name: "google-ads-env",
    isDir: true,
    children: [
      { name: "bin", isDir: true, children: [{ name: "activate", isDir: false, size: "2.1 KB" }] },
      { name: "lib", isDir: true, children: [] },
      { name: "pyvenv.cfg", isDir: false, size: "0.3 KB" },
    ],
  },
  {
    name: "google-ads-python-main",
    isDir: true,
    children: [
      { name: "setup.py", isDir: false, size: "1.8 KB" },
      { name: "README.md", isDir: false, size: "4.2 KB" },
    ],
  },
  { name: "googleads_env", isDir: true, children: [] },
  { name: "test-project", isDir: true, children: [] },
  { name: "google_ads_example.py", isDir: false, size: "5.4 KB" },
  { name: "google-ads-python.zip", isDir: false, size: "18 MB" },
  { name: "google-ads.yaml", isDir: false, size: "1.5 KB" },
  { name: "NAVOD.md", isDir: false, size: "5.1 KB" },
]

const FAKE_CODE: Record<string, string> = {
  "google_ads_example.py": `#!/usr/bin/env python3
"""Google Ads API example - fetch campaign performance."""

import argparse
from google.ads.googleads.client import GoogleAdsClient
from google.ads.googleads.errors import GoogleAdsException


def main(client, customer_id):
    """Fetch and display campaign metrics."""
    ga_service = client.get_service("GoogleAdsService")

    query = """
        SELECT
          campaign.id,
          campaign.name,
          campaign.status,
          metrics.impressions,
          metrics.clicks,
          metrics.cost_micros,
          metrics.conversions
        FROM campaign
        WHERE segments.date DURING LAST_30_DAYS
        ORDER BY metrics.impressions DESC
        LIMIT 20
    """

    stream = ga_service.search_stream(
        customer_id=customer_id, query=query
    )

    print(f"{'Campaign':<40} {'Status':<12} {'Impr':>10} {'Clicks':>8} {'Cost':>12} {'Conv':>8}")
    print("-" * 92)

    for batch in stream:
        for row in batch.results:
            campaign = row.campaign
            metrics = row.metrics
            cost = metrics.cost_micros / 1_000_000

            print(
                f"{campaign.name:<40} "
                f"{campaign.status.name:<12} "
                f"{metrics.impressions:>10,} "
                f"{metrics.clicks:>8,} "
                f"\${cost:>11,.2f} "
                f"{metrics.conversions:>8,.1f}"
            )


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("-c", "--customer_id", required=True)
    args = parser.parse_args()

    credentials = GoogleAdsClient.load_from_storage("google-ads.yaml")
    main(credentials, args.customer_id)
`,
  "google-ads.yaml": `developer_token: "REDACTED_DEVELOPER_TOKEN"
client_id: "REDACTED_CLIENT_ID.apps.googleusercontent.com"
client_secret: "REDACTED_CLIENT_SECRET"
refresh_token: "REDACTED_REFRESH_TOKEN"
login_customer_id: "REDACTED_CUSTOMER_ID"
# use_proto_plus: True
`,
  "NAVOD.md": `# Google Ads API - Navod

## Instalace

\`\`\`bash
pip install google-ads
\`\`\`

## Konfigurace

1. Vytvorte soubor \`google-ads.yaml\`
2. Vyplnte credentials z Google Cloud Console
3. Spustte example:

\`\`\`bash
python google_ads_example.py -c 1234567890
\`\`\`

## Dulezite

- API verze: v17
- Rate limit: 10,000 requests/day
- Pouzijte \`search_stream\` pro velke datasety
`,
  "pyvenv.cfg": `home = /usr/bin
include-system-site-packages = false
version = 3.11.5
`,
  "setup.py": `from setuptools import setup, find_packages

setup(
    name="google-ads-example",
    version="0.1.0",
    packages=find_packages(),
    install_requires=[
        "google-ads>=23.0.0",
    ],
)
`,
  "activate": `#!/bin/bash
# Virtual environment activation script
export VIRTUAL_ENV="/output/google-ads-env"
export PATH="\$VIRTUAL_ENV/bin:\$PATH"
`,
  "README.md": `# google-ads-python

Google Ads API client library for Python.
`,
}

/* ── icon helper ─────────────────────────────────────── */

function fileIcon(name: string) {
  if (name.endsWith(".py")) return <FileCode className="h-3.5 w-3.5 text-yellow-600 shrink-0" />
  if (name.endsWith(".yaml") || name.endsWith(".yml")) return <FileText className="h-3.5 w-3.5 text-rose-500 shrink-0" />
  if (name.endsWith(".json")) return <FileJson className="h-3.5 w-3.5 text-amber-500 shrink-0" />
  if (name.endsWith(".zip")) return <FileArchive className="h-3.5 w-3.5 text-orange-500 shrink-0" />
  if (name.endsWith(".md")) return <FileText className="h-3.5 w-3.5 text-blue-500 shrink-0" />
  if (name.endsWith(".cfg") || name.endsWith(".sh") || name === "activate") return <FileCode className="h-3.5 w-3.5 text-green-600 shrink-0" />
  return <FileText className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
}

/* ── tree row ────────────────────────────────────────── */

function TreeRow({
  node,
  depth,
  selectedFile,
  onSelect,
}: {
  node: TreeNode
  depth: number
  selectedFile: string | null
  onSelect: (name: string) => void
}) {
  const [open, setOpen] = useState(false)
  const isSelected = !node.isDir && selectedFile === node.name

  return (
    <>
      <button
        onClick={() => {
          if (node.isDir) setOpen(!open)
          else onSelect(node.name)
        }}
        className={`
          w-full flex items-center gap-1.5 py-1 px-2 text-xs rounded-md transition-colors
          hover:bg-accent/60
          ${isSelected ? "bg-blue-50 text-blue-700 font-medium" : "text-foreground/80"}
        `}
        style={{ paddingLeft: `${8 + depth * 16}px` }}
      >
        {node.isDir ? (
          open ? (
            <ChevronDown className="h-3 w-3 text-muted-foreground shrink-0" />
          ) : (
            <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
          )
        ) : (
          <span className="w-3" />
        )}
        {node.isDir
          ? open
            ? <FolderOpen className="h-3.5 w-3.5 text-blue-500 shrink-0" />
            : <FolderClosed className="h-3.5 w-3.5 text-blue-500 shrink-0" />
          : fileIcon(node.name)}
        <span className="truncate flex-1 text-left">{node.name}</span>
        {!node.isDir && node.size && (
          <span className="text-[10px] text-muted-foreground ml-auto shrink-0">{node.size}</span>
        )}
      </button>
      {node.isDir && open && node.children?.map((child) => (
        <TreeRow
          key={child.name}
          node={child}
          depth={depth + 1}
          selectedFile={selectedFile}
          onSelect={onSelect}
        />
      ))}
    </>
  )
}

/* ── slide-up editor ─────────────────────────────────── */

function SlideEditor({
  fileName,
  code,
  expanded,
  onToggleExpand,
  onClose,
}: {
  fileName: string
  code: string
  expanded: boolean
  onToggleExpand: () => void
  onClose: () => void
}) {
  const [dirty, setDirty] = useState(false)
  const [content, setContent] = useState(code)

  const lineCount = content.split("\n").length

  return (
    <div
      className={`
        border-t bg-[#1e1e1e] flex flex-col transition-all duration-300 ease-in-out
        ${expanded ? "h-[70%]" : "h-[40%]"}
      `}
    >
      {/* header bar */}
      <div className="flex items-center justify-between px-3 py-1.5 bg-[#252526] border-b border-[#3c3c3c] shrink-0">
        <div className="flex items-center gap-2">
          {fileIcon(fileName)}
          <span className="text-xs text-[#cccccc] font-medium">{fileName}</span>
          {dirty && <span className="w-1.5 h-1.5 rounded-full bg-amber-400" />}
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={() => { setDirty(false) }}
            className={`
              flex items-center gap-1 px-2 py-0.5 rounded text-[10px] font-medium transition-colors
              ${dirty
                ? "bg-blue-600 text-white hover:bg-blue-700"
                : "bg-[#3c3c3c] text-[#666] cursor-default"
              }
            `}
            disabled={!dirty}
          >
            <Save className="h-3 w-3" />
            Save
          </button>
          <button onClick={onToggleExpand} className="p-1 rounded hover:bg-[#3c3c3c] text-[#888]">
            {expanded ? <Minimize2 className="h-3 w-3" /> : <Maximize2 className="h-3 w-3" />}
          </button>
          <button onClick={onClose} className="p-1 rounded hover:bg-[#3c3c3c] text-[#888]">
            <X className="h-3 w-3" />
          </button>
        </div>
      </div>

      {/* editor area */}
      <div className="flex-1 overflow-auto font-mono text-xs leading-5">
        <div className="flex min-h-full">
          {/* line numbers */}
          <div className="shrink-0 py-2 pl-3 pr-2 text-right text-[#5a5a5a] select-none border-r border-[#3c3c3c]" style={{ minWidth: "40px" }}>
            {Array.from({ length: lineCount }, (_, i) => (
              <div key={i}>{i + 1}</div>
            ))}
          </div>
          {/* code */}
          <textarea
            value={content}
            onChange={(e) => {
              setContent(e.target.value)
              setDirty(true)
            }}
            spellCheck={false}
            className="flex-1 bg-transparent text-[#d4d4d4] resize-none outline-none py-2 px-3 leading-5"
            style={{ tabSize: 4 }}
          />
        </div>
      </div>

      {/* status bar */}
      <div className="flex items-center justify-between px-3 py-0.5 bg-[#007acc] text-[10px] text-white shrink-0">
        <div className="flex items-center gap-3">
          <span>{lineCount} lines</span>
          <span>UTF-8</span>
        </div>
        <div className="flex items-center gap-3">
          <span>Ctrl+S to save</span>
          {dirty && <span className="font-medium">Modified</span>}
        </div>
      </div>
    </div>
  )
}

/* ── main page ───────────────────────────────────────── */

export default function PreviewChatEditorPage() {
  const [selectedFile, setSelectedFile] = useState<string | null>(null)
  const [editorExpanded, setEditorExpanded] = useState(false)

  const fileCount = TREE.filter((n) => !n.isDir).length

  const handleFileSelect = (name: string) => {
    if (FAKE_CODE[name]) {
      setSelectedFile(name)
      setEditorExpanded(false)
    }
  }

  return (
    <div className="flex items-center justify-center min-h-screen bg-gray-100 p-8">
      {/* phone-like frame for the right panel */}
      <div className="w-[420px] h-[700px] bg-card rounded-2xl shadow-2xl border overflow-hidden flex flex-col">
        {/* tab bar */}
        <div className="flex items-center border-b bg-card shrink-0">
          {[
            { icon: Files, label: "Files", active: true },
            { icon: Sparkles, label: "Triggers", active: false },
            { icon: Users, label: "Team", active: false },
            { icon: MessageSquare, label: "Context", active: false },
          ].map((tab) => (
            <button
              key={tab.label}
              className={`
                flex-1 flex items-center justify-center gap-1.5 py-2.5 text-xs font-medium
                border-b-2 transition-colors
                ${tab.active
                  ? "border-blue-500 text-blue-600"
                  : "border-transparent text-muted-foreground hover:text-foreground"
                }
              `}
            >
              <tab.icon className="h-3.5 w-3.5" />
              {tab.label}
            </button>
          ))}
        </div>

        {/* tree area -- grows to fill when no editor, shrinks when editor is open */}
        <div className="flex-1 min-h-0 overflow-y-auto px-1 py-1">
          {TREE.map((node) => (
            <TreeRow
              key={node.name}
              node={node}
              depth={0}
              selectedFile={selectedFile}
              onSelect={handleFileSelect}
            />
          ))}
        </div>

        {/* footer (only when no editor) */}
        {!selectedFile && (
          <div className="px-3 py-1.5 border-t text-[11px] text-muted-foreground shrink-0 bg-card">
            {fileCount} files
          </div>
        )}

        {/* slide-up editor */}
        {selectedFile && FAKE_CODE[selectedFile] && (
          <SlideEditor
            fileName={selectedFile}
            code={FAKE_CODE[selectedFile]}
            expanded={editorExpanded}
            onToggleExpand={() => setEditorExpanded(!editorExpanded)}
            onClose={() => setSelectedFile(null)}
          />
        )}
      </div>

      {/* legend */}
      <div className="ml-8 max-w-xs space-y-3 text-sm text-muted-foreground">
        <h3 className="text-lg font-semibold text-foreground">Chat Right Panel + Editor</h3>
        <p>Click any file in the tree to open a <strong>slide-up editor</strong> from the bottom.</p>
        <ul className="space-y-1.5 text-xs">
          <li className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-blue-500" />
            Default: editor takes <strong>40%</strong> of panel height
          </li>
          <li className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-green-500" />
            Expand button: grows to <strong>70%</strong>
          </li>
          <li className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-amber-500" />
            Amber dot = unsaved changes
          </li>
          <li className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-[#007acc]" />
            VS Code-style status bar at bottom
          </li>
        </ul>
        <p className="text-xs">Tree remains scrollable above the editor. Close with X to return to full tree view.</p>
      </div>
    </div>
  )
}
