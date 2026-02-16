import { Save, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { Badge } from "@/components/ui/badge"

export default async function SettingsPage({ params }: { params: Promise<{ agentId: string }> }) {
  await params

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-3xl">
      {/* General */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">General</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="name">Agent Name</Label>
            <Input id="name" defaultValue="Claude — SEO Writer" />
          </div>
          <div className="space-y-2">
            <Label htmlFor="slug">Slug</Label>
            <Input id="slug" defaultValue="claude-seo-writer" className="font-mono text-sm" />
          </div>
          <div className="space-y-2">
            <Label htmlFor="description">Description</Label>
            <Textarea id="description" defaultValue="Specialized in creating SEO-optimized blog posts, meta descriptions, and content briefs." rows={3} />
          </div>
        </CardContent>
      </Card>

      {/* Runtime */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Runtime</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="cli">CLI Adapter</Label>
              <Input id="cli" defaultValue="Claude Code" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="model">Model</Label>
              <Input id="model" defaultValue="claude-sonnet-4" className="font-mono text-sm" />
            </div>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="timeout">Timeout (minutes)</Label>
              <Input id="timeout" type="number" defaultValue={30} />
            </div>
            <div className="space-y-2">
              <Label>Role</Label>
              <div className="flex items-center gap-2 h-9">
                <Badge variant="outline" className="text-amber-600 border-amber-300">LEADER</Badge>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Team Assignment */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Team Assignment</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex items-center gap-3">
            <span className="h-3 w-3 rounded-full bg-orange-500" />
            <div>
              <p className="text-sm font-medium">Marketing</p>
              <p className="text-xs text-muted-foreground">3 agents · 2 active</p>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Actions */}
      <div className="flex flex-wrap items-center gap-3 pt-2">
        <Button className="gap-2">
          <Save className="h-4 w-4" /> Save Changes
        </Button>
        <Button variant="outline" className="gap-2 text-destructive border-destructive/30 hover:bg-destructive/10">
          <Trash2 className="h-4 w-4" /> Delete Agent
        </Button>
      </div>
    </div>
  )
}
