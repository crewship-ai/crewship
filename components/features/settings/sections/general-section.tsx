"use client"

import { useState, type FormEvent } from "react"
import { Check, X, ChevronsUpDown } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
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
import { SettingsCard, SettingsRow, SettingsDangerCard } from "../shared"

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
    <div className="space-y-5">
      {/* ── Identity ── */}
      <SettingsCard title="Identity" description="Your workspace name, slug, and default agent language">
        <form onSubmit={handleSave}>
          <SettingsRow label="Workspace name">
            <Input
              value={formName}
              onChange={(e) => setFormName(e.target.value)}
              placeholder="My Company"
              className="h-7 text-xs w-48"
            />
          </SettingsRow>
          <SettingsRow label="Slug" description="Used in URLs and CLI commands">
            <Input
              value={formSlug}
              onChange={(e) => setFormSlug(e.target.value)}
              placeholder="my-company"
              className="h-7 text-xs w-48 font-mono"
            />
          </SettingsRow>
          {(isDirty || saveStatus !== "idle") && (
            <div className="flex items-center justify-end gap-3 px-4 py-2 border-b border-border/40">
              {saveStatus === "error" && saveError && (
                <span className="text-[11px] text-destructive mr-auto">{saveError}</span>
              )}
              <Button
                type="submit"
                size="sm"
                className="h-7 px-2.5 text-xs"
                disabled={saveStatus === "saving"}
              >
                {saveStatus === "saving" ? <Spinner className="mr-1.5 h-3 w-3" /> : saveStatus === "success" ? <Check className="mr-1.5 h-3 w-3" /> : null}
                {saveStatus === "saving" ? "Saving…" : saveStatus === "success" ? "Saved" : "Save changes"}
              </Button>
            </div>
          )}
        </form>
        <SettingsRow label="Agent language" description="Agents will respond in this language" border={false}>
          <Popover open={langOpen} onOpenChange={setLangOpen}>
            <PopoverTrigger asChild>
              <button
                className="inline-flex items-center justify-between w-48 h-7 px-2.5 rounded-md bg-background border border-border text-xs text-foreground hover:border-ring transition-colors disabled:opacity-50"
                disabled={langSaving}
              >
                {selectedLang ? (
                  <span className="truncate">{selectedLang.flag} {selectedLang.name}</span>
                ) : (
                  <span className="text-muted-foreground">Select language…</span>
                )}
                <ChevronsUpDown className="h-3 w-3 text-muted-foreground ml-2 shrink-0" />
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-64 p-0" align="end">
              <Command filter={(value, search) => {
                const lang = LANGUAGES.find((l) => l.name === value)
                if (!lang) return 0
                const s = search.toLowerCase()
                return (lang.name.toLowerCase().includes(s) || lang.native.toLowerCase().includes(s) || lang.code.toLowerCase().includes(s)) ? 1 : 0
              }}>
                <CommandInput placeholder="Search language…" />
                <CommandList>
                  <CommandEmpty>No language found.</CommandEmpty>
                  <CommandGroup>
                    {formLanguage && (
                      <CommandItem value="__clear__" onSelect={() => handleLanguageChange(null)}>
                        <X className="h-3 w-3 text-muted-foreground" />
                        <span className="text-muted-foreground text-xs">Clear</span>
                      </CommandItem>
                    )}
                    {LANGUAGES.map((lang) => (
                      <CommandItem key={lang.code} value={lang.name} onSelect={() => handleLanguageChange(lang.name)} className="text-xs">
                        <span className="mr-2">{lang.flag}</span>
                        <span>{lang.name}</span>
                        <span className="ml-auto text-[10px] text-muted-foreground">{lang.native}</span>
                        {formLanguage === lang.name && <Check className="ml-1 h-3 w-3 text-primary" />}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>
        </SettingsRow>
      </SettingsCard>

      {/* ── Usage ── */}
      <SettingsCard title="Usage" description="Resource counts for this workspace">
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <span className="h-1.5 w-1.5 rounded-full bg-blue-400" />
              Agents
            </span>
          }
        >
          <span className="text-xs font-mono font-semibold text-foreground tabular-nums">
            <AnimatedNumber value={agentCount} />
          </span>
        </SettingsRow>
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-400" />
              Crews
            </span>
          }
        >
          <span className="text-xs font-mono font-semibold text-foreground tabular-nums">
            <AnimatedNumber value={crewCount} />
          </span>
        </SettingsRow>
        <SettingsRow
          label={
            <span className="inline-flex items-center gap-2">
              <span className="h-1.5 w-1.5 rounded-full bg-violet-400" />
              Members
            </span> as unknown as string
          }
          border={false}
        >
          <span className="text-xs font-mono font-semibold text-foreground tabular-nums">
            <AnimatedNumber value={memberCount} />
          </span>
        </SettingsRow>
      </SettingsCard>

      {/* ── Danger Zone ── */}
      {role === "OWNER" && (
        <SettingsDangerCard
          title="Danger zone"
          description="Irreversible actions that affect the whole workspace"
        >
          {deleteError && (
            <div className="px-4 py-2 border-b border-destructive/20">
              <span className="text-[11px] text-destructive">{deleteError}</span>
            </div>
          )}
          <div className="flex items-center justify-between gap-4 px-4 py-2.5">
            <div className="min-w-0 shrink-0">
              <div className="text-xs text-foreground">Delete workspace</div>
              <div className="text-[11px] text-muted-foreground/80 mt-0.5">
                Permanently delete all crews, agents, and data
              </div>
            </div>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="destructive" size="sm" className="h-7 px-2.5 text-xs">
                  Delete workspace
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle className="text-sm">Delete workspace</AlertDialogTitle>
                  <AlertDialogDescription className="text-xs">
                    This will permanently delete all crews, agents, credentials, and data. This cannot be undone.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={handleDelete}
                    className="h-7 text-xs bg-destructive text-destructive-foreground hover:bg-destructive/90"
                    disabled={isDeleting}
                  >
                    {isDeleting && <Spinner className="h-3 w-3 mr-1.5" />}
                    {isDeleting ? "Deleting…" : "Delete workspace"}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        </SettingsDangerCard>
      )}
    </div>
  )
}
