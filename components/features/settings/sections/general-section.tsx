"use client"

import { useState, type FormEvent } from "react"
import { Check, X, ChevronsUpDown, Languages, Loader2, AlertTriangle } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"
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
import { LANGUAGES } from "@/lib/languages"

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

  return (
    <div className="space-y-5">
      {/* ── Workspace Identity ── */}
      <Card className="border-white/[0.06]">
        <CardContent className="p-0">
          <div className="px-5 sm:px-6 py-4">
            <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-4">
              Identity
            </div>
            <form onSubmit={handleSave}>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mb-4">
                <div className="space-y-1.5">
                  <label className="text-[11px] text-muted-foreground/50 uppercase tracking-wider">Name</label>
                  <Input
                    value={formName}
                    onChange={(e) => setFormName(e.target.value)}
                    placeholder="My Company"
                    className="h-9 bg-white/[0.03] border-white/[0.08] text-[13px]"
                  />
                </div>
                <div className="space-y-1.5">
                  <label className="text-[11px] text-muted-foreground/50 uppercase tracking-wider">Slug</label>
                  <Input
                    value={formSlug}
                    onChange={(e) => setFormSlug(e.target.value)}
                    placeholder="my-company"
                    className="h-9 bg-white/[0.03] border-white/[0.08] text-[13px] font-mono"
                  />
                </div>
              </div>

              <div className="flex items-center gap-3">
                <button
                  type="submit"
                  disabled={saveStatus === "saving"}
                  className="inline-flex items-center gap-1.5 h-[28px] px-3 rounded-[4px] text-[11.5px] font-medium bg-blue-500/15 border border-blue-500/35 text-blue-400 hover:bg-blue-500/25 transition-colors disabled:opacity-50"
                >
                  {saveStatus === "saving" ? <Loader2 className="h-3 w-3 animate-spin" /> : saveStatus === "success" ? <Check className="h-3 w-3" /> : null}
                  {saveStatus === "saving" ? "Saving..." : saveStatus === "success" ? "Saved" : "Save Changes"}
                </button>
                {saveStatus === "error" && saveError && (
                  <span className="text-[11px] text-red-400">{saveError}</span>
                )}
              </div>
            </form>
          </div>

          <Separator className="bg-white/[0.06]" />

          {/* Language */}
          <div className="px-5 sm:px-6 py-4">
            <div className="flex items-center gap-2 mb-1">
              <Languages className="h-3.5 w-3.5 text-muted-foreground/40" />
              <span className="text-[11px] text-muted-foreground/50 uppercase tracking-wider font-semibold">Agent Language</span>
            </div>
            <p className="text-[11px] text-muted-foreground/30 mb-3">Agents will respond in this language.</p>
            <Popover open={langOpen} onOpenChange={setLangOpen}>
              <PopoverTrigger asChild>
                <button
                  className="inline-flex items-center justify-between w-64 h-9 px-3 rounded-md bg-white/[0.03] border border-white/[0.08] text-[13px] text-foreground hover:border-white/[0.15] transition-colors disabled:opacity-50"
                  disabled={langSaving}
                >
                  {formLanguage ? (() => {
                    const lang = LANGUAGES.find((l) => l.name === formLanguage)
                    return lang ? `${lang.flag} ${lang.name}` : formLanguage
                  })() : <span className="text-muted-foreground/40">Select language...</span>}
                  <ChevronsUpDown className="h-3.5 w-3.5 text-muted-foreground/40 ml-2" />
                </button>
              </PopoverTrigger>
              <PopoverContent className="w-64 p-0" align="start">
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
        </CardContent>
      </Card>

      {/* ── Usage Overview ── */}
      <Card className="border-white/[0.06]">
        <CardContent className="p-5 sm:p-6">
          <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-4">
            Usage
          </div>
          <div className="grid grid-cols-3 gap-4">
            {[
              { label: "Agents", value: agentCount, color: "bg-blue-500", bar: "bg-blue-500/30" },
              { label: "Crews", value: crewCount, color: "bg-emerald-500", bar: "bg-emerald-500/30" },
              { label: "Members", value: memberCount, color: "bg-cyan-500", bar: "bg-cyan-500/30" },
            ].map(({ label, value, color, bar }) => (
              <div key={label} className="relative overflow-hidden bg-white/[0.02] border border-white/[0.06] rounded-lg p-4">
                <div className="flex items-center gap-1.5 mb-2">
                  <div className={`w-1.5 h-1.5 rounded-full ${color}`} />
                  <span className="text-[10px] text-muted-foreground/40 uppercase tracking-wider font-medium">{label}</span>
                </div>
                <div className="text-[22px] font-mono font-semibold text-foreground tabular-nums leading-none">
                  <AnimatedNumber value={value} />
                </div>
                {/* Decorative bar */}
                <div className="absolute bottom-0 left-0 right-0 h-[3px]">
                  <div className={`h-full ${bar} rounded-full`} style={{ width: `${Math.min(100, Math.max(10, value * 8))}%` }} />
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>

      {/* ── Danger Zone ── */}
      {role === "OWNER" && (
        <Card className="border-red-500/15">
          <CardContent className="p-5 sm:p-6">
            <div className="flex items-center gap-2 mb-1">
              <AlertTriangle className="h-3.5 w-3.5 text-red-400/60" />
              <span className="text-[10px] font-semibold text-red-400/50 uppercase tracking-wider">Danger Zone</span>
            </div>
            <p className="text-[11px] text-muted-foreground/30 mb-4">
              Irreversible actions. All crews, agents, credentials will be permanently deleted.
            </p>
            {deleteError && <p className="text-[11px] text-red-400 mb-3">{deleteError}</p>}
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <button className="inline-flex items-center gap-1.5 h-[28px] px-3 rounded-[4px] text-[11.5px] font-medium bg-red-500/10 border border-red-500/25 text-red-400/70 hover:bg-red-500/20 hover:text-red-400 transition-colors">
                  Delete Workspace
                </button>
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
          </CardContent>
        </Card>
      )}
    </div>
  )
}
