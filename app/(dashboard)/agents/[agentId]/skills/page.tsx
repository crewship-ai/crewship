import { Puzzle, Plus, Settings, ExternalLink } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"

export default async function SkillsPage({ params }: { params: Promise<{ agentId: string }> }) {
  await params

  const skills = [
    {
      name: "Web Search",
      description: "Search the web for relevant articles, data, and research. Uses Brave Search API.",
      category: "Research",
      status: "Active",
    },
    {
      name: "File Writer",
      description: "Create and modify files in the /output/ directory. Supports Markdown, CSV, JSON, and plain text.",
      category: "Output",
      status: "Active",
    },
    {
      name: "SEO Analyzer",
      description: "Analyze keyword density, meta tags, and content structure for SEO optimization scores.",
      category: "Analysis",
      status: "Active",
    },
  ]

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">3 skills assigned</p>
        <Button size="sm" className="gap-1.5">
          <Plus className="h-3.5 w-3.5" /> Assign Skill
        </Button>
      </div>

      {/* Skills list */}
      <div className="grid gap-3">
        {skills.map((skill) => (
          <Card key={skill.name} className="py-0">
            <CardContent className="p-4 sm:p-5">
              <div className="flex items-start justify-between gap-4">
                <div className="flex items-start gap-3">
                  <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10">
                    <Puzzle className="h-5 w-5 text-primary" />
                  </div>
                  <div className="space-y-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      <h3 className="text-sm font-medium">{skill.name}</h3>
                      <Badge variant="outline" className="text-xs">{skill.category}</Badge>
                      <Badge variant="secondary" className="bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400 text-xs">
                        {skill.status}
                      </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground leading-relaxed">{skill.description}</p>
                  </div>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <Button variant="ghost" size="icon" className="h-8 w-8">
                    <ExternalLink className="h-4 w-4" />
                  </Button>
                  <Button variant="ghost" size="icon" className="h-8 w-8">
                    <Settings className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
