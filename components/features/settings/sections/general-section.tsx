"use client"

import { useState, type FormEvent } from "react"
import { Check, X, ChevronsUpDown, Loader2 } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { Button } from "@/components/ui/button"
import { LANGUAGES } from "@/lib/languages"
import { cn } from "@/lib/utils"

function Row({ label, description, children, border = true }: {
  label: React.ReactNode
  description?: string
  children: React.ReactNode
  border?: boolean
}) {
  return (
    <div className={cn("flex items-center justify-between gap-4 px-5 py-3.5 min-h-[48px]", border && "border-b border-border/40 last:border-b-0")}>
      <div className="shrink-0">
        <div className="text-body text-foreground">{label}</div>
        {description && <div className="text-label text-muted-foreground mt-0.5">{description}</div>}
      </div>
      <div className="flex items-center gap-2 min-w-0 justify-end">{children}</div>
    </div>
  )
}

interface GeneralSectionProps {
  workspaceId: string
  orgName: string
  orgSlug: string
  preferredLanguage: string | null
  agentCount: number
  crewCount: number
  memberCount: number
  role: string | null
  onUpdated: (org: { name: string; slug: string; preferred_language: string | null }) => void
  onDelete: () => void
}

export function GeneralSection({
  workspaceId, orgName, orgSlug, preferredLanguage,
  agentCount, crewCount, memberCount, role, onUpdated, onDelete,
}: GeneralSectionProps) {
  const [formName, setFormName] = useState(orgName)
  const [formSlug, setFormSlug] = useState(orgSlug)
  const [formLanguage, setFormLanguage] = useState(preferredLanguage)
  const [langOpen, setLangOpen] = useState(false)
  const [langSaving, setLangSaving] = useState(false)
  const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "success" | "error">("idle")
  const [saveError, setSaveError] = useState<string | null>(null)
  const [isDeleting, setIsDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const isDirty = formName !== orgName || formSlug !== orgSlug

  async function handleSave(e: FormEvent) {
    e.preventDefault()
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
        setSaveStatus("error")
        setSaveError(typeof body?.error === "string" ? body.error : "Failed to save")
        return
      }
      const updated = await res.json()
      setFormName(updated.name)
      setFormSlug(updated.slug)
      onUpdated(updated)
      setSaveStatus("success")
      setTimeout(() => setSaveStatus("idle"), 3000)
    } catch {
      setSaveStatus("error")
      setSaveError("Failed to save changes")
    }
  }

  async function handleLanguageChange(code: string | null) {
    setFormLanguage(code)
    setLangOpen(false)
    setLangSaving(true)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ preferred_language: code ?? "" }),
      })
      if (res.ok) {
        const updated = await res.json()
        setFormLanguage(updated.preferred_language)
        onUpdated(updated)
      } else {
        setFormLanguage(preferredLanguage)
      }
    } catch {
      setFormLanguage(preferredLanguage)
    } finally {
      setLangSaving(false)
    }
  }

  async function handleDelete() {
    if (isDeleting) return
    setIsDeleting(true)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, { method: "DELETE" })
      if (res.ok) {
        onDelete()
      } else {
        const body = await res.json().catch(() => null)
        setDeleteError(typeof body?.error === "string" ? body.error : "Failed to delete workspace")
      }
    } catch {
      setDeleteError("Failed to delete workspace")
    } finally {
      setIsDeleting(false)
    }
  }

  const selectedLang = formLanguage ? LANGUAGES.find((l) => l.name === formLanguage) : null

  return (
    <div className="space-y-6">
      {/* ── Identity ── */}
      <div>
        <h3 className="text-heading font-medium text-foreground mb-3">Identity</h3>
        <Card>
          <CardContent className="p-0">
            <form onSubmit={handleSave}>
              <Row label="Workspace name">
                <Input
                  value={formName}
                  onChange={(e) => setFormName(e.target.value)}
                  placeholder="My Company"
                  className="h-8 text-body w-48"
                />
              </Row>
              <Row label="Slug">
                <Input
                  value={formSlug}
                  onChange={(e) => setFormSlug(e.target.value)}
                  placeholder="my-company"
                  className="h-8 text-body w-48 font-mono"
                />
              </Row>
              {(isDirty || saveStatus !== "idle") && (
                <div className="flex items-center justify-end gap-3 px-5 py-3 border-b border-border/40">
                  {saveStatus === "error" && saveError && (
                    <span className="text-label text-destructive mr-auto">{saveError}</span>
                  )}
                  <Button
                    type="submit"
                    size="sm"
                    disabled={saveStatus === "saving"}
                  >
                    {saveStatus === "saving" ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" /> : saveStatus === "success" ? <Check className="mr-1.5 h-3 w-3" /> : null}
                    {saveStatus === "saving" ? "Saving..." : saveStatus === "success" ? "Saved" : "Save Changes"}
                  </Button>
                </div>
              )}
            </form>
            <Row label="Agent language" description="Agents will respond in this language" border={false}>
              <Popover open={langOpen} onOpenChange={setLangOpen}>
                <PopoverTrigger asChild>
                  <button
                    className="inline-flex items-center justify-between w-48 h-8 px-3 rounded-md bg-background border border-border text-body text-foreground hover:border-ring transition-colors disabled:opacity-50"
                    disabled={langSaving}
                  >
                    {selectedLang ? (
                      <span className="truncate">{selectedLang.flag} {selectedLang.name}</span>
                    ) : (
                      <span className="text-muted-foreground">Select language...</span>
                    )}
                    <ChevronsUpDown className="h-3.5 w-3.5 text-muted-foreground ml-2 shrink-0" />
                  </button>
                </PopoverTrigger>
                <PopoverContent className="w-64 p-0" align="end">
                  <Command filter={(value, search) => {
                    const lang = LANGUAGES.find((l) => l.name === value)
                    if (!lang) return 0
                    const s = search.toLowerCase()
                    return (lang.name.toLowerCase().includes(s) || lang.native.toLowerCase().includes(s) || lang.code.toLowerCase().includes(s)) ? 1 : 0
                  }}>
                    <CommandInput placeholder="Search language..." />
                    <CommandList>
                      <CommandEmpty>No language found.</CommandEmpty>
                      <CommandGroup>
                        {formLanguage && (
                          <CommandItem value="__clear__" onSelect={() => handleLanguageChange(null)}>
                            <X className="h-4 w-4 text-muted-foreground" />
                            <span className="text-muted-foreground">Clear</span>
                          </CommandItem>
                        )}
                        {LANGUAGES.map((lang) => (
                          <CommandItem key={lang.code} value={lang.name} onSelect={() => handleLanguageChange(lang.name)}>
                            <span className="mr-2">{lang.flag}</span>
                            <span>{lang.name}</span>
                            <span className="ml-auto text-label text-muted-foreground">{lang.native}</span>
                            {formLanguage === lang.name && <Check className="ml-1 h-3.5 w-3.5 text-primary" />}
                          </CommandItem>
                        ))}
                      </CommandGroup>
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>
            </Row>
          </CardContent>
        </Card>
      </div>

      {/* ── Usage ── */}
      <div>
        <h3 className="text-heading font-medium text-foreground mb-3">Usage</h3>
        <Card>
          <CardContent className="p-0">
            <Row label={
              <span className="flex items-center gap-2">
                <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                Agents
              </span>
            }>
              <span className="text-body font-mono font-semibold text-foreground tabular-nums">
                <AnimatedNumber value={agentCount} />
              </span>
            </Row>
            <Row label={
              <span className="flex items-center gap-2">
                <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                Crews
              </span>
            }>
              <span className="text-body font-mono font-semibold text-foreground tabular-nums">
                <AnimatedNumber value={crewCount} />
              </span>
            </Row>
            <Row label={
              <span className="flex items-center gap-2">
                <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                Members
              </span>
            } border={false}>
              <span className="text-body font-mono font-semibold text-foreground tabular-nums">
                <AnimatedNumber value={memberCount} />
              </span>
            </Row>
          </CardContent>
        </Card>
      </div>

      {/* ── Danger Zone ── */}
      {role === "OWNER" && (
        <div>
          <h3 className="text-heading font-medium text-foreground mb-3">Danger Zone</h3>
          <Card className="border-destructive/30">
            <CardContent className="p-0">
              {deleteError && (
                <div className="px-5 py-2 border-b border-destructive/20">
                  <span className="text-label text-destructive">{deleteError}</span>
                </div>
              )}
              <Row label="Delete workspace" description="Permanently delete all crews, agents, and data" border={false}>
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button variant="destructive" size="sm">
                      Delete Workspace
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>Delete Workspace</AlertDialogTitle>
                      <AlertDialogDescription>
                        This will permanently delete all crews, agents, credentials, and data. This cannot be undone.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>Cancel</AlertDialogCancel>
                      <AlertDialogAction
                        onClick={handleDelete}
                        className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                        disabled={isDeleting}
                      >
                        {isDeleting && <Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" />}
                        {isDeleting ? "Deleting..." : "Delete Workspace"}
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </Row>
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  )
}
