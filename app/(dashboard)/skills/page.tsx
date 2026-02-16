import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"

const bundledSkills = [
  {
    name: "Coding Assistant",
    description: "Code review, refactoring, debugging, test writing",
    category: "CODING",
    icon: "💻",
  },
  {
    name: "Web Researcher",
    description: "Web search, data extraction, competitive analysis",
    category: "DATA",
    icon: "🔍",
  },
  {
    name: "DevOps Helper",
    description: "Infrastructure monitoring, deployment, CI/CD",
    category: "DEVOPS",
    icon: "🔧",
  },
]

export default function SkillsPage() {
  return (
    <div className="p-6 space-y-6">
      <div>
        <h1 className="text-lg font-semibold">Skills</h1>
        <p className="text-sm text-muted-foreground">Browse and manage agent skills</p>
      </div>

      <div className="flex items-center gap-2">
        <Badge variant="secondary" className="cursor-pointer">All</Badge>
        <Badge variant="outline" className="cursor-pointer">Bundled</Badge>
        <Badge variant="outline" className="cursor-pointer">Custom</Badge>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {bundledSkills.map((skill) => (
          <Card key={skill.name} className="hover:border-primary/50 transition-colors cursor-pointer">
            <CardContent className="p-5">
              <div className="flex items-start gap-3">
                <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-muted text-lg">
                  {skill.icon}
                </div>
                <div className="flex-1">
                  <div className="flex items-center gap-2">
                    <h3 className="text-sm font-semibold">{skill.name}</h3>
                    <Badge variant="secondary" className="text-[10px]">Bundled</Badge>
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">{skill.description}</p>
                  <Badge variant="outline" className="mt-2 text-[10px]">{skill.category}</Badge>
                </div>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
