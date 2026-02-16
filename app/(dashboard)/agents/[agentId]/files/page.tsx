import { FileText, FileSpreadsheet, File, Download, Eye, ChevronRight, FolderOpen } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"

export default async function FilesPage({ params }: { params: Promise<{ agentId: string }> }) {
  await params

  const files = [
    { name: "blog-post.md", icon: FileText, size: "14.2 KB", modified: "23 min ago", type: "Markdown" },
    { name: "seo-audit.pdf", icon: File, size: "2.1 MB", modified: "1h ago", type: "PDF" },
    { name: "keywords.csv", icon: FileSpreadsheet, size: "8.4 KB", modified: "3h ago", type: "CSV" },
  ]

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Breadcrumb */}
      <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
        <FolderOpen className="h-4 w-4" />
        <span>/output/</span>
        <ChevronRight className="h-3 w-3" />
      </div>

      {/* File list */}
      <div className="border rounded-lg overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b bg-muted/50 text-xs text-muted-foreground uppercase tracking-wide">
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Name</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Type</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium">Size</th>
              <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Modified</th>
              <th className="text-right px-4 sm:px-6 py-3 font-medium">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {files.map((f) => (
              <tr key={f.name} className="hover:bg-muted/50">
                <td className="px-4 sm:px-6 py-3">
                  <div className="flex items-center gap-2">
                    <f.icon className="h-4 w-4 text-muted-foreground shrink-0" />
                    <span className="font-medium truncate">{f.name}</span>
                  </div>
                </td>
                <td className="px-4 sm:px-6 py-3 hidden sm:table-cell">
                  <Badge variant="outline" className="text-xs">{f.type}</Badge>
                </td>
                <td className="px-4 sm:px-6 py-3 text-muted-foreground font-mono text-xs">{f.size}</td>
                <td className="px-4 sm:px-6 py-3 text-xs text-muted-foreground hidden sm:table-cell">{f.modified}</td>
                <td className="px-4 sm:px-6 py-3 text-right">
                  <div className="flex items-center justify-end gap-1">
                    <Button variant="ghost" size="icon" className="h-8 w-8">
                      <Eye className="h-4 w-4" />
                    </Button>
                    <Button variant="ghost" size="icon" className="h-8 w-8">
                      <Download className="h-4 w-4" />
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Summary */}
      <p className="text-xs text-muted-foreground">3 files · 2.1 MB total</p>
    </div>
  )
}
