"use client"

import { use, useState, useEffect } from "react"
import { ShieldCheck, AlertCircle, Inbox } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useOrg } from "@/hooks/use-org"

interface CredentialData {
  id: string
  name: string
  description: string | null
  scope: string
}

interface AgentCredential {
  id: string
  agent_id: string
  credential_id: string
  env_var_name: string
  priority: number
  credential: CredentialData
}

const SCOPE_STYLES: Record<string, string> = {
  ORGANIZATION: "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-400",
  TEAM: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400",
}

export default function CredentialsPage({ params }: { params: Promise<{ agentId: string }> }) {
  const { agentId } = use(params)
  const { orgId, loading: orgLoading } = useOrg()
  const [credentials, setCredentials] = useState<AgentCredential[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!orgId) return

    let cancelled = false

    async function fetchCredentials() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}/credentials?org_id=${orgId}`)
        if (!res.ok) {
          if (!cancelled) setError("Failed to load credentials")
          return
        }
        const data: AgentCredential[] = await res.json()
        if (!cancelled) setCredentials(data)
      } catch {
        if (!cancelled) setError("Network error. Please try again.")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchCredentials()
    return () => { cancelled = true }
  }, [agentId, orgId])

  if (orgLoading || loading) {
    return <CredentialsSkeleton />
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Header */}
      <div className="flex items-center gap-2">
        <ShieldCheck className="h-4 w-4 text-muted-foreground" />
        <p className="text-sm text-muted-foreground">
          {credentials.length} credential{credentials.length !== 1 ? "s" : ""} assigned · AES-256-GCM encrypted
        </p>
      </div>

      {credentials.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
          <p className="text-sm font-medium text-muted-foreground">No credentials assigned</p>
          <p className="text-xs text-muted-foreground mt-1">Assign credentials so the agent can access external services.</p>
        </div>
      ) : (
        <div className="border rounded-lg overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b bg-muted/50 text-xs text-muted-foreground uppercase tracking-wide">
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Name</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Env Variable</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium">Priority</th>
                <th className="text-left px-4 sm:px-6 py-3 font-medium hidden sm:table-cell">Scope</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {credentials.map((c) => (
                <tr key={c.id} className="hover:bg-muted/50">
                  <td className="px-4 sm:px-6 py-3">
                    <div>
                      <span className="font-medium">{c.credential.name}</span>
                      {c.credential.description && (
                        <p className="text-xs text-muted-foreground mt-0.5 truncate max-w-[200px]">{c.credential.description}</p>
                      )}
                    </div>
                  </td>
                  <td className="px-4 sm:px-6 py-3 font-mono text-xs">{c.env_var_name}</td>
                  <td className="px-4 sm:px-6 py-3 text-center font-mono text-xs">{c.priority}</td>
                  <td className="px-4 sm:px-6 py-3 hidden sm:table-cell">
                    <Badge variant="secondary" className={`text-xs ${SCOPE_STYLES[c.credential.scope] ?? ""}`}>
                      {c.credential.scope}
                    </Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Info */}
      <p className="text-xs text-muted-foreground">
        Credentials are injected at container start. Priority-based failover rotates keys automatically on rate limit errors.
      </p>
    </div>
  )
}

function CredentialsSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <Skeleton className="h-5 w-64" />
      <div className="border rounded-lg">
        <div className="border-b bg-muted/50 px-4 sm:px-6 py-3">
          <Skeleton className="h-4 w-full max-w-md" />
        </div>
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="px-4 sm:px-6 py-3 border-b last:border-b-0">
            <Skeleton className="h-5 w-full" />
          </div>
        ))}
      </div>
    </div>
  )
}
