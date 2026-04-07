"use client"

import { useState, type FormEvent } from "react"
import { Check, X, ChevronsUpDown, Languages, Loader2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import { LANGUAGES } from "@/lib/languages"

interface GeneralSectionProps {
  workspaceId: string
  orgName: string
  orgSlug: string
  preferredLanguage: string | null
  onUpdated: (org: { name: string; slug: string; preferred_language: string | null }) => void
}

export function GeneralSection({
  workspaceId,
  orgName,
  orgSlug,
  preferredLanguage,
  onUpdated,
}: GeneralSectionProps) {
  const [formName, setFormName] = useState(orgName)
  const [formSlug, setFormSlug] = useState(orgSlug)
  const [formLanguage, setFormLanguage] = useState(preferredLanguage)
  const [langOpen, setLangOpen] = useState(false)
  const [langSaving, setLangSaving] = useState(false)
  const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "success" | "error">("idle")
  const [saveError, setSaveError] = useState<string | null>(null)

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

  return (
    <div className="space-y-4">
      {/* Identity card */}
      <div className="bg-card border border-white/[0.06] rounded-lg p-6">
        <h4 className="text-[11px] font-semibold text-muted-foreground/50 uppercase tracking-wider mb-4">
          Workspace Identity
        </h4>
        <form onSubmit={handleSave} className="space-y-4">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <label className="text-[11px] text-muted-foreground/60 uppercase tracking-wider">
                Name
              </label>
              <Input
                value={formName}
                onChange={(e) => setFormName(e.target.value)}
                placeholder="My Company"
                className="h-9 bg-white/[0.03] border-white/[0.08] text-[13px]"
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-[11px] text-muted-foreground/60 uppercase tracking-wider">
                Slug
              </label>
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
              {saveStatus === "saving" ? (
                <Loader2 className="h-3 w-3 animate-spin" />
              ) : saveStatus === "success" ? (
                <Check className="h-3 w-3" />
              ) : null}
              {saveStatus === "saving" ? "Saving..." : saveStatus === "success" ? "Saved" : "Save Changes"}
            </button>
            {saveStatus === "error" && saveError && (
              <span className="text-[11px] text-red-400">{saveError}</span>
            )}
          </div>
        </form>
      </div>

      {/* Language card */}
      <div className="bg-card border border-white/[0.06] rounded-lg p-6">
        <div className="flex items-center gap-2 mb-1">
          <Languages className="h-4 w-4 text-muted-foreground/60" />
          <h4 className="text-[11px] font-semibold text-muted-foreground/50 uppercase tracking-wider">
            Agent Language
          </h4>
        </div>
        <p className="text-[12px] text-muted-foreground/50 mb-4">
          Agents will respond in the selected language.
        </p>
        <Popover open={langOpen} onOpenChange={setLangOpen}>
          <PopoverTrigger asChild>
            <Button
              variant="outline"
              role="combobox"
              aria-expanded={langOpen}
              className="w-64 justify-between font-normal h-9 bg-white/[0.03] border-white/[0.08] text-[13px]"
              disabled={langSaving}
            >
              {formLanguage ? (
                (() => {
                  const lang = LANGUAGES.find((l) => l.name === formLanguage)
                  return lang ? `${lang.flag} ${lang.name}` : formLanguage
                })()
              ) : (
                <span className="text-muted-foreground/50">Select language...</span>
              )}
              <ChevronsUpDown className="ml-2 h-3.5 w-3.5 shrink-0 opacity-50" />
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-64 p-0" align="start">
            <Command
              filter={(value, search) => {
                const lang = LANGUAGES.find((l) => l.name === value)
                if (!lang) return 0
                const s = search.toLowerCase()
                if (
                  lang.name.toLowerCase().includes(s) ||
                  lang.native.toLowerCase().includes(s) ||
                  lang.code.toLowerCase().includes(s)
                )
                  return 1
                return 0
              }}
            >
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
                      {formLanguage === lang.name && (
                        <Check className="ml-1 h-3.5 w-3.5 text-primary" />
                      )}
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
