import { Key, Plus, Download, Upload } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"

export default function CredentialsPage() {
  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm">
            <Download className="mr-2 h-4 w-4" />
            Export JSON
          </Button>
          <Button variant="outline" size="sm">
            <Upload className="mr-2 h-4 w-4" />
            Import JSON
          </Button>
        </div>
        <Button>
          <Plus className="mr-2 h-4 w-4" />
          Add Credential
        </Button>
      </div>

      <Card>
        <CardContent className="flex flex-col items-center justify-center py-16 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-muted mb-4">
            <Key className="h-6 w-6 text-muted-foreground" />
          </div>
          <h3 className="text-sm font-semibold">No credentials yet</h3>
          <p className="mt-1 text-sm text-muted-foreground max-w-sm">
            Add API keys and secrets that your agents will use. All credentials are encrypted with AES-256-GCM.
          </p>
          <Button className="mt-4">
            <Plus className="mr-2 h-4 w-4" />
            Add First Credential
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
