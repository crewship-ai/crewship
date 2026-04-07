"use client"

import { useEffect, useState, type FormEvent } from "react"
import {
  User, Palette, Bell, Shield, Building, Users, CreditCard,
  AlertTriangle, Check, X, Key, ChevronsUpDown, Languages, Container
} from "lucide-react"
import { useSession } from "@/hooks/use-auth"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { InviteMemberDialog } from "@/components/features/members/invite-member-dialog"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { cn } from "@/lib/utils"
import { LANGUAGES } from "@/lib/languages"
import { CrewInfrastructure } from "@/components/features/settings/crew-infrastructure"

type Scope = "user" | "org"

interface TabDef {
  type?: "section"
  key?: string
  label: string
  icon?: React.ElementType
  badge?: string
}

const userTabs: TabDef[] = [
  { type: "section", label: "ACCOUNT" },
  { key: "profile", label: "Profile", icon: User },
  { key: "chats", label: "Chats", icon: Shield, badge: "Phase 2" },
  { type: "section", label: "PREFERENCES" },
  { key: "appearance", label: "Appearance", icon: Palette, badge: "Phase 2" },
  { key: "notifications", label: "Notifications", icon: Bell, badge: "Phase 2" },
  { type: "section", label: "DEVELOPER" },
  { key: "tokens", label: "API Tokens", icon: Key, badge: "Phase 2" },
]

const orgTabs: TabDef[] = [
  { type: "section", label: "GENERAL" },
  { key: "general", label: "General", icon: Building },
  { key: "members", label: "Members", icon: Users },
  { key: "roles", label: "Roles & Permissions", icon: Shield },
  { type: "section", label: "INFRASTRUCTURE" },
  { key: "crews", label: "Crews & Containers", icon: Container },
  { type: "section", label: "BILLING" },
  { key: "billing", label: "Billing & Usage", icon: CreditCard },
  { type: "section", label: "ADVANCED" },
  { key: "danger", label: "Danger Zone", icon: AlertTriangle, badge: "OWNER" },
]

interface Org {
  id: string
  name: string
  slug: string
  preferred_language: string | null
  _count: { crews: number; agents: number; members: number }
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

const roleCls: Record<string, string> = {
  OWNER: "bg-amber-50 text-amber-700",
  ADMIN: "bg-blue-50 text-blue-700",
  MANAGER: "bg-teal-50 text-teal-700",
  MEMBER: "bg-muted text-muted-foreground",
  VIEWER: "bg-muted text-muted-foreground",
}

const permMatrix = [
  { role: "Owner", perms: [true, true, true, "All", true, true] },
  { role: "Admin", perms: [true, true, true, "All", true, true] },
  { role: "Manager", perms: [false, true, "Crew", "Crew", false, false] },
  { role: "Member", perms: [false, false, false, "Own", false, false] },
  { role: "Viewer", perms: [false, false, false, false, false, false] },
]
const permHeaders = ["See all crews", "Create agents", "Manage creds", "Audit access", "Manage members", "Billing"]

export default function SettingsPage() {
  const { data: session } = useSession()
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const [scope, setScope] = useState<Scope>("user")
  const [tab, setTab] = useState("profile")

  const [org, setOrg] = useState<Org | null>(null)
  const [members, setMembers] = useState<Member[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [formName, setFormName] = useState("")
  const [formSlug, setFormSlug] = useState("")
  const [formLanguage, setFormLanguage] = useState<string | null>(null)
  const [langOpen, setLangOpen] = useState(false)
  const [langSaving, setLangSaving] = useState(false)
  const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "success" | "error">("idle")
  const [saveError, setSaveError] = useState<string | null>(null)
  const [refreshKey, setRefreshKey] = useState(0)
  const [isDeleting, setIsDeleting] = useState(false)

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false

    async function fetchData() {
      setLoading(true)
      setError(null)
      try {
        const [orgRes, membersRes] = await Promise.all([
          fetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`),
          fetch(`/api/v1/workspaces/${workspaceId}/members?workspace_id=${workspaceId}`),
        ])

        if (!orgRes.ok) {
          setError("Failed to load workspace")
          return
        }

        const orgData = (await orgRes.json()) as Org
        if (!cancelled) {
          setOrg(orgData)
          setFormName(orgData.name)
          setFormSlug(orgData.slug)
          setFormLanguage(orgData.preferred_language)
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
    return () => { cancelled = true }
  }, [workspaceId, refreshKey])

  function switchScope(s: Scope) {
    setScope(s)
    setTab(s === "user" ? "profile" : "general")
  }

  async function handleSaveOrg(e: FormEvent) {
    e.preventDefault()
    if (!workspaceId) return

    setSaveStatus("saving")
    setSaveError(null)

    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, {
        method: "PATCH",
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

  async function handleDeleteOrg() {
    if (!workspaceId || isDeleting) return

    setIsDeleting(true)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (res.ok) {
        window.location.href = "/"
      } else {
        const body = await res.json().catch(() => null)
        setSaveError(typeof body?.error === "string" ? body.error : "Failed to delete workspace")
      }
    } catch {
      setSaveError("Failed to delete workspace")
    } finally {
      setIsDeleting(false)
    }
  }

  async function handleLanguageChange(code: string | null) {
    setFormLanguage(code)
    setLangOpen(false)
    if (!workspaceId) return

    setLangSaving(true)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ preferred_language: code ?? "" }),
      })
      if (res.ok) {
        const updated = (await res.json()) as Org
        setOrg(updated)
        setFormLanguage(updated.preferred_language)
      } else {
        // Revert on server error
        setFormLanguage(org?.preferred_language ?? null)
      }
    } catch {
      // Revert on network error
      setFormLanguage(org?.preferred_language ?? null)
    } finally {
      setLangSaving(false)
    }
  }

  const isLoading = wsLoading || loading
  const tabs = scope === "user" ? userTabs : orgTabs

  function renderContent() {
    if (isLoading) {
      return <div className="space-y-4"><Skeleton className="h-[200px] rounded-xl" /></div>
    }

    // User tabs
    if (tab === "profile") {
      return (
        <div className="space-y-5">
          <div className="flex items-center gap-4 pb-4 border-b">
            <div className="w-14 h-14 rounded-full bg-primary flex items-center justify-center text-primary-foreground text-lg font-semibold">
              {(session?.user?.name ?? "U").split(" ").map((n) => n[0]).join("").slice(0, 2).toUpperCase()}
            </div>
            <div>
              <div className="text-sm font-medium">{session?.user?.name ?? "User"}</div>
              <div className="text-xs text-muted-foreground">{session?.user?.email ?? ""}</div>
            </div>
          </div>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>Full Name</Label>
              <Input value={session?.user?.name ?? ""} readOnly className="bg-muted" />
            </div>
            <div className="space-y-2">
              <Label>Email</Label>
              <Input value={session?.user?.email ?? ""} readOnly className="bg-muted" />
            </div>
            {role && (
              <div className="space-y-2">
                <Label>Workspace Role</Label>
                <Badge variant="outline">{role}</Badge>
              </div>
            )}
          </div>
        </div>
      )
    }

    if (tab === "general") {
      return (
        <div className="space-y-5">
          <div className="pb-4 border-b">
            <h3 className="text-sm font-medium">Workspace Settings</h3>
            <p className="text-xs text-muted-foreground">Update your workspace details.</p>
          </div>
          <form onSubmit={handleSaveOrg} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="org-name">Workspace Name</Label>
              <Input id="org-name" value={formName} onChange={(e) => setFormName(e.target.value)} placeholder="My Company" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="org-slug">Slug</Label>
              <Input id="org-slug" value={formSlug} onChange={(e) => setFormSlug(e.target.value)} placeholder="my-company" />
            </div>

            {saveStatus === "success" && <p className="text-sm text-emerald-600">Changes saved successfully.</p>}
            {saveStatus === "error" && saveError && <p className="text-sm text-destructive">{saveError}</p>}

            <Button type="submit" disabled={saveStatus === "saving"}>
              {saveStatus === "saving" ? "Saving..." : "Save Changes"}
            </Button>
          </form>

          <div className="pt-4 border-t">
            <h3 className="text-sm font-medium flex items-center gap-2">
              <Languages className="h-4 w-4" />
              Agent Language
            </h3>
            <p className="text-xs text-muted-foreground mt-1 mb-3">
              Set a preferred language for all agents in this workspace. Agents will respond in the selected language.
            </p>
            <Popover open={langOpen} onOpenChange={setLangOpen}>
              <PopoverTrigger asChild>
                <Button
                  variant="outline"
                  role="combobox"
                  aria-expanded={langOpen}
                  className="w-64 justify-between font-normal"
                  disabled={langSaving}
                >
                  {formLanguage ? (
                    (() => {
                      const lang = LANGUAGES.find((l) => l.name === formLanguage)
                      return lang ? `${lang.flag} ${lang.name}` : formLanguage
                    })()
                  ) : (
                    <span className="text-muted-foreground">Select language...</span>
                  )}
                  <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
                </Button>
              </PopoverTrigger>
              <PopoverContent className="w-64 p-0" align="start">
                <Command filter={(value, search) => {
                  const lang = LANGUAGES.find((l) => l.name === value)
                  if (!lang) return 0
                  const s = search.toLowerCase()
                  if (lang.name.toLowerCase().includes(s) || lang.native.toLowerCase().includes(s) || lang.code.toLowerCase().includes(s)) return 1
                  return 0
                }}>
                  <CommandInput placeholder="Search language..." />
                  <CommandList>
                    <CommandEmpty>No language found.</CommandEmpty>
                    <CommandGroup>
                      {formLanguage && (
                        <CommandItem value="__clear__" onSelect={() => handleLanguageChange(null)}>
                          <X className="h-4 w-4 text-muted-foreground" />
                          <span className="text-muted-foreground">Clear selection</span>
                        </CommandItem>
                      )}
                      {LANGUAGES.map((lang) => (
                        <CommandItem
                          key={lang.code}
                          value={lang.name}
                          onSelect={() => handleLanguageChange(lang.name)}
                        >
                          <span className="mr-2">{lang.flag}</span>
                          <span>{lang.name}</span>
                          <span className="ml-auto text-xs text-muted-foreground">{lang.native}</span>
                          {formLanguage === lang.name && <Check className="ml-1 h-3.5 w-3.5 text-primary" />}
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
          </div>
        </div>
      )
    }

    if (tab === "members") {
      return (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h3 className="text-sm font-medium">Members</h3>
              <p className="text-xs text-muted-foreground">{members.length} member{members.length !== 1 ? "s" : ""}</p>
            </div>
            {abilities.can("create", "Member") && workspaceId && (
              <InviteMemberDialog workspaceId={workspaceId} onInvited={() => setRefreshKey((k) => k + 1)} />
            )}
          </div>
          {members.length > 0 && (
            <Card>
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
                        <TableCell className="text-sm font-medium">{member.user.full_name ?? "—"}</TableCell>
                        <TableCell className="text-sm text-muted-foreground">{member.user.email}</TableCell>
                        <TableCell>
                          <Badge variant="secondary" className={cn("text-micro", roleCls[member.role] ?? "")}>
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
        </div>
      )
    }

    if (tab === "roles") {
      return (
        <div className="space-y-4">
          <div className="pb-3 border-b">
            <h3 className="text-sm font-medium">Permission Matrix</h3>
            <p className="text-xs text-muted-foreground">Reference of what each role can do. Roles are assigned per member.</p>
          </div>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">Role</TableHead>
                  {permHeaders.map((h) => (
                    <TableHead key={h} className="text-center text-micro">{h}</TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {permMatrix.map((row) => (
                  <TableRow key={row.role}>
                    <TableCell className={cn("text-xs font-medium", row.role === "Owner" ? "text-amber-700" : row.role === "Admin" ? "text-blue-700" : "")}>
                      {row.role}
                    </TableCell>
                    {row.perms.map((v, i) => (
                      <TableCell key={i} className="text-center">
                        {v === true ? (
                          <Check className="h-3.5 w-3.5 text-emerald-600 mx-auto" />
                        ) : v === false ? (
                          <X className="h-3.5 w-3.5 text-muted-foreground/30 mx-auto" />
                        ) : (
                          <span className="text-micro text-muted-foreground">{v}</span>
                        )}
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </div>
      )
    }

    if (tab === "billing") {
      return (
        <div className="space-y-5">
          <Card>
            <CardContent className="p-4">
              <div className="mb-3">
                <h3 className="text-sm font-medium">Workspace Usage</h3>
                <p className="text-xs text-muted-foreground">{org?.name ?? "Workspace"}</p>
              </div>
              <div className="space-y-2 text-xs">
                <div className="flex justify-between"><span className="text-muted-foreground">Agents</span><span className="font-medium">{org?._count.agents ?? 0}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">Crews</span><span className="font-medium">{org?._count.crews ?? 0}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">Members</span><span className="font-medium">{org?._count.members ?? 0}</span></div>
              </div>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="p-6 text-center">
              <CreditCard className="h-8 w-8 text-muted-foreground/40 mx-auto mb-2" />
              <p className="text-sm text-muted-foreground">Billing and plan management will be available in a future release.</p>
            </CardContent>
          </Card>
        </div>
      )
    }

    if (tab === "crews") {
      return (
        <CrewInfrastructure
          workspaceId={workspaceId!}
          canEdit={abilities.can("update", "Crew")}
        />
      )
    }

    if (tab === "danger") {
      if (role !== "OWNER") {
        return (
          <Card>
            <CardContent className="p-6 text-center">
              <p className="text-sm text-muted-foreground">Only workspace owners can access this section.</p>
            </CardContent>
          </Card>
        )
      }
      return (
        <Card className="border-destructive/30">
          <CardHeader>
            <CardTitle className="text-base text-destructive flex items-center gap-2">
              <AlertTriangle className="h-4 w-4" />
              Danger Zone
            </CardTitle>
            <CardDescription>These actions are irreversible. Proceed with extreme caution.</CardDescription>
          </CardHeader>
          <CardContent>
            {saveError && <p className="text-sm text-destructive mb-3">{saveError}</p>}
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="destructive">Delete Workspace</Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Delete Workspace</AlertDialogTitle>
                  <AlertDialogDescription>
                    Are you sure you want to delete this workspace? All crews, agents, credentials, and data will be permanently removed. This action cannot be undone.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={handleDeleteOrg}
                    variant="destructive"
                    disabled={isDeleting}
                  >
                    {isDeleting ? "Deleting..." : "Delete Workspace"}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </CardContent>
        </Card>
      )
    }

    // Phase 2 placeholder
    return (
      <Card>
        <CardContent className="p-6 text-center">
          <Badge variant="outline" className="mb-2">Phase 2</Badge>
          <p className="text-sm text-muted-foreground">This feature is coming in a future release.</p>
        </CardContent>
      </Card>
    )
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6 max-w-2xl">
        <p className="text-sm text-destructive">{error}</p>
      </div>
    )
  }

  return (
    <div className="flex h-full">
      {/* Left nav */}
      <div className="w-56 border-r bg-background flex flex-col flex-shrink-0 overflow-y-auto">
        {/* Scope switcher */}
        <div className="p-3 border-b">
          <div className="flex gap-1 p-0.5 bg-muted rounded-lg" role="tablist" aria-label="Settings scope">
            <button
              role="tab"
              aria-selected={scope === "user"}
              className={cn(
                "flex-1 text-xs py-1.5 rounded-md font-medium transition-colors",
                scope === "user" ? "bg-background shadow-sm" : "text-muted-foreground hover:text-foreground"
              )}
              onClick={() => switchScope("user")}
            >
              User
            </button>
            <button
              role="tab"
              aria-selected={scope === "org"}
              className={cn(
                "flex-1 text-xs py-1.5 rounded-md font-medium transition-colors",
                scope === "org" ? "bg-background shadow-sm" : "text-muted-foreground hover:text-foreground"
              )}
              onClick={() => switchScope("org")}
            >
              Workspace
            </button>
          </div>
          {scope === "org" && org && (
            <div className="flex items-center gap-2 mt-2 px-1">
              <div className="w-5 h-5 rounded bg-primary flex items-center justify-center text-primary-foreground text-[8px] font-bold">
                {org.name[0]?.toUpperCase()}
              </div>
              <span className="text-micro text-muted-foreground">{org.name}</span>
            </div>
          )}
        </div>

        {/* Tab list */}
        <nav className="flex-1 p-2 space-y-0.5">
          {tabs.map((t, i) => {
            if (t.type === "section") {
              return (
                <div key={i} className="text-micro font-semibold text-muted-foreground uppercase tracking-wider px-3 pt-3 pb-1">
                  {t.label}
                </div>
              )
            }
            const Icon = t.icon!
            const isActive = t.key === tab
            return (
              <button
                key={t.key}
                className={cn(
                  "flex items-center gap-2.5 w-full px-3 py-2 rounded-md text-xs transition-colors",
                  isActive ? "bg-primary/10 text-primary font-medium" : "text-muted-foreground hover:bg-muted"
                )}
                onClick={() => setTab(t.key!)}
              >
                <Icon className="h-4 w-4 flex-shrink-0" />
                <span className="truncate">{t.label}</span>
                {t.badge === "Phase 2" && (
                  <span className="ml-auto text-[8px] bg-muted text-muted-foreground px-1.5 py-0.5 rounded font-medium">P2</span>
                )}
                {t.badge === "OWNER" && (
                  <span className="ml-auto text-[8px] bg-amber-50 text-amber-700 px-1.5 py-0.5 rounded font-medium">Owner</span>
                )}
              </button>
            )
          })}
        </nav>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        <div className="max-w-2xl mx-auto px-8 py-6">
          {renderContent()}
        </div>
      </div>
    </div>
  )
}
