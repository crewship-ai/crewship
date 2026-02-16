"use client"

import { useEffect, useState, type FormEvent } from "react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { PageHeader } from "@/components/layout/page-header"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useOrg } from "@/hooks/use-org"

interface Org {
  id: string
  name: string
  slug: string
  _count: { teams: number; agents: number; members: number }
}

interface MemberUser {
  id: string
  email: string
  full_name: string | null
  avatar_url: string | null
}

interface Member {
  id: string
  role: string
  created_at: string
  user: MemberUser
}

export default function SettingsPage() {
  const { orgId, loading: orgLoading } = useOrg()
  const [, setOrg] = useState<Org | null>(null)
  const [members, setMembers] = useState<Member[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "success" | "error">("idle")
  const [saveError, setSaveError] = useState<string | null>(null)

  const [formName, setFormName] = useState("")
  const [formSlug, setFormSlug] = useState("")

  useEffect(() => {
    if (!orgId) return

    let cancelled = false

    async function fetchData() {
      setLoading(true)
      setError(null)
      try {
        const [orgRes, membersRes] = await Promise.all([
          fetch(`/api/v1/orgs/${orgId}?org_id=${orgId}`),
          fetch(`/api/v1/orgs/${orgId}/members?org_id=${orgId}`),
        ])

        if (!orgRes.ok) {
          setError("Failed to load organization")
          return
        }

        const orgData = (await orgRes.json()) as Org
        if (!cancelled) {
          setOrg(orgData)
          setFormName(orgData.name)
          setFormSlug(orgData.slug)
        }

        if (membersRes.ok) {
          const membersData = (await membersRes.json()) as Member[]
          if (!cancelled) setMembers(membersData)
        }
      } catch {
        if (!cancelled) setError("Failed to load settings")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchData()
    return () => {
      cancelled = true
    }
  }, [orgId])

  const isLoading = orgLoading || loading

  async function handleSave(e: FormEvent) {
    e.preventDefault()
    if (!orgId) return

    setSaveStatus("saving")
    setSaveError(null)

    try {
      const res = await fetch(`/api/v1/orgs/${orgId}?org_id=${orgId}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: formName, slug: formSlug }),
      })

      if (!res.ok) {
        const body = await res.json().catch(() => null)
        const msg = typeof body?.error === "string" ? body.error : "Failed to save"
        setSaveStatus("error")
        setSaveError(msg)
        return
      }

      const updated = (await res.json()) as Org
      setOrg(updated)
      setFormName(updated.name)
      setFormSlug(updated.slug)
      setSaveStatus("success")
      setTimeout(() => setSaveStatus("idle"), 3000)
    } catch {
      setSaveStatus("error")
      setSaveError("Failed to save changes")
    }
  }

  async function handleDelete() {
    if (!orgId) return

    const confirmed = window.confirm(
      "Are you sure you want to delete this organization? This action cannot be undone."
    )
    if (!confirmed) return

    try {
      const res = await fetch(`/api/v1/orgs/${orgId}?org_id=${orgId}`, {
        method: "DELETE",
      })
      if (res.ok) {
        window.location.href = "/"
      } else {
        const body = await res.json().catch(() => null)
        const msg = typeof body?.error === "string" ? body.error : "Failed to delete"
        setSaveError(msg)
      }
    } catch {
      setSaveError("Failed to delete organization")
    }
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-2xl">
        <PageHeader title="Settings" description="Manage your organization settings" />
        <p className="text-sm text-destructive">{error}</p>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6 max-w-2xl">
      <PageHeader title="Settings" description="Manage your organization settings" />

      {isLoading ? (
        <div className="space-y-4">
          <Skeleton className="h-[200px] rounded-xl" />
          <Skeleton className="h-[100px] rounded-xl" />
        </div>
      ) : (
        <>
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Organization</CardTitle>
              <CardDescription>Update your organization details</CardDescription>
            </CardHeader>
            <CardContent>
              <form onSubmit={handleSave} className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="org-name">Organization Name</Label>
                  <Input
                    id="org-name"
                    value={formName}
                    onChange={(e) => setFormName(e.target.value)}
                    placeholder="My Company"
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="org-slug">Slug</Label>
                  <Input
                    id="org-slug"
                    value={formSlug}
                    onChange={(e) => setFormSlug(e.target.value)}
                    placeholder="my-company"
                  />
                </div>

                {saveStatus === "success" && (
                  <p className="text-sm text-emerald-600">Changes saved successfully.</p>
                )}
                {saveStatus === "error" && saveError && (
                  <p className="text-sm text-destructive">{saveError}</p>
                )}

                <Button type="submit" disabled={saveStatus === "saving"}>
                  {saveStatus === "saving" ? "Saving…" : "Save Changes"}
                </Button>
              </form>
            </CardContent>
          </Card>

          {members.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Members</CardTitle>
                <CardDescription>People in your organization</CardDescription>
              </CardHeader>
              <CardContent className="p-0">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Name</TableHead>
                      <TableHead>Email</TableHead>
                      <TableHead>Role</TableHead>
                      <TableHead>Joined</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {members.map((member) => (
                      <TableRow key={member.id}>
                        <TableCell className="text-sm font-medium">
                          {member.user.full_name ?? "—"}
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {member.user.email}
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className="text-[10px]">
                            {member.role}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {new Date(member.created_at).toLocaleDateString()}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </CardContent>
            </Card>
          )}

          <Card>
            <CardHeader>
              <CardTitle className="text-base">Danger Zone</CardTitle>
              <CardDescription>Irreversible actions for your organization</CardDescription>
            </CardHeader>
            <CardContent>
              {saveError && saveStatus !== "error" && (
                <p className="text-sm text-destructive mb-3">{saveError}</p>
              )}
              <Button variant="destructive" onClick={handleDelete}>
                Delete Organization
              </Button>
            </CardContent>
          </Card>
        </>
      )}
    </div>
  )
}
