import { Key, Plus, Download, Upload } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"

export default function CredentialsPage() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Credentials" description="Manage API keys and secrets for your agents">
        <Button variant="outline" size="sm">
          <Download className="mr-2 h-4 w-4" />
          Export JSON
        </Button>
        <Button variant="outline" size="sm">
          <Upload className="mr-2 h-4 w-4" />
          Import JSON
        </Button>
        <Button>
          <Plus className="mr-2 h-4 w-4" />
          Add Credential
        </Button>
      </PageHeader>

      <EmptyState
        icon={Key}
        title="No credentials yet"
        description="Add API keys and secrets that your agents will use. All credentials are encrypted with AES-256-GCM."
      >
        <Button className="mt-4">
          <Plus className="mr-2 h-4 w-4" />
          Add First Credential
        </Button>
      </EmptyState>
    </div>
  )
}
