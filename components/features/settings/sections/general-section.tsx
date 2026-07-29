"use client"

import { useState } from "react"
import { Check, X, ChevronsUpDown } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Input } from "@/components/ui/input"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
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
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier, isOwner } from "@/lib/permissions/tiers"
import { SettingsCard, SettingsRow, SettingsDangerCard } from "@/components/features/settings/shared"
import { PrivilegedCredentialsCard } from "@/components/features/settings/sections/privileged-credentials-card"

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
  // Name, slug and language are all typed-in values on one card, so they share
  // one draft and one footer. Language used to PATCH the instant you picked it,
  // which made the card commit two different ways depending on which control
  // you touched.
  const form = useDirtyForm({ name: orgName, slug: orgSlug, language: preferredLanguage })
  // PATCH /api/v1/workspaces/{id} is roleManage — ADMIN and up. Below that the
  // card is information, not a form: rendering inputs (even disabled ones) only
  // invites an edit the server answers with a 403.
  const canEdit = isAdminTier(role)
  const [langOpen, setLangOpen] = useState(false)
  const [isDeleting, setIsDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [confirmSlug, setConfirmSlug] = useState("")
  // The backend requires the caller to re-type the slug (confirm_slug),
  // so the destructive action stays disabled until it matches exactly.
  const slugConfirmed = confirmSlug === orgSlug

  function handleSave() {
    void form.submit(async (draft) => {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        // One write for the whole identity triple. The endpoint reads "" as
        // "clear the language", which is what a null draft means here.
        body: JSON.stringify({
          name: draft.name,
          slug: draft.slug,
          preferred_language: draft.language ?? "",
        }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        // Thrown, not swallowed: useDirtyForm turns the message into the
        // footer's error state and keeps the draft for a retry.
        throw new Error(typeof body?.error === "string" ? body.error : "Failed to save")
      }
      // The server normalizes (slugification); it reaches the inputs via the
      // parent's props, which the clean form adopts as its new baseline.
      onUpdated(await res.json())
    })
  }

  async function handleDelete() {
    if (isDeleting || !slugConfirmed) return
    setIsDeleting(true)
    setDeleteError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        // The server re-validates confirm_slug against the workspace slug.
        body: JSON.stringify({ confirm_slug: confirmSlug }),
      })
      if (res.ok) {
        setDeleteOpen(false)
        onDelete()
      } else {
        const body = await res.json().catch(() => null)
        // RFC 7807 problems use `detail`; legacy errors use `error`.
        const msg = body?.detail ?? body?.error
        setDeleteError(typeof msg === "string" ? msg : "Failed to delete workspace")
        setDeleteOpen(false)
      }
    } catch {
      setDeleteError("Failed to delete workspace")
      setDeleteOpen(false)
    } finally {
      setIsDeleting(false)
    }
  }

  const draftLanguage = form.draft.language
  const selectedLang = draftLanguage ? LANGUAGES.find((l) => l.name === draftLanguage) : null
  // Read-only path renders the saved value, not the draft — there is no control
  // that could have moved it. Looked up only for the flag; an unrecognised
  // language still prints its own name rather than vanishing.
  const readOnlyLang = preferredLanguage ? LANGUAGES.find((l) => l.name === preferredLanguage) : null

  function pickLanguage(name: string | null) {
    form.set("language", name)
    setLangOpen(false)
  }

  return (
    <div className="space-y-5">
      {/* ── Identity ── */}
      <SettingsCard
        title="Identity"
        description={
          canEdit
            ? "Your workspace name, slug, and default agent language"
            // Stated once, in the description, in the same muted type as every
            // other hint — a non-admin viewing their own workspace is normal,
            // so it must not read like a permission error.
            : "Your workspace name, slug, and default agent language. Only workspace admins can change these."
        }
      >
        {!canEdit ? (
          <>
            <SettingsRow label="Workspace name">
              <span className="text-xs text-foreground truncate">{orgName}</span>
            </SettingsRow>
            <SettingsRow label="Slug" description="Used in URLs and CLI commands">
              <span className="text-xs font-mono text-foreground truncate">{orgSlug}</span>
            </SettingsRow>
            <SettingsRow
              label="Agent language"
              description="Agents will respond in this language"
              border={false}
            >
              {preferredLanguage ? (
                <span className="text-xs text-foreground truncate">
                  {readOnlyLang ? `${readOnlyLang.flag} ` : ""}{preferredLanguage}
                </span>
              ) : (
                <span className="text-xs text-muted-foreground">Not set</span>
              )}
            </SettingsRow>
          </>
        ) : (
          <>
            <SettingsRow label="Workspace name">
              <Input
                value={form.draft.name}
                onChange={(e) => form.set("name", e.target.value)}
                placeholder="My Company"
                aria-label="Workspace name"
                className="h-7 text-xs w-48"
              />
            </SettingsRow>
            <SettingsRow label="Slug" description="Used in URLs and CLI commands">
              <Input
                value={form.draft.slug}
                onChange={(e) => form.set("slug", e.target.value)}
                placeholder="my-company"
                aria-label="Slug"
                className="h-7 text-xs w-48 font-mono"
              />
            </SettingsRow>
            <SettingsRow label="Agent language" description="Agents will respond in this language" border={false}>
              <Popover open={langOpen} onOpenChange={setLangOpen}>
                <PopoverTrigger asChild>
                  <button
                    className="inline-flex items-center justify-between w-48 h-7 px-2.5 rounded-md bg-background border border-border text-xs text-foreground hover:border-ring transition-colors disabled:opacity-50"
                    disabled={form.status === "saving"}
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
                        {draftLanguage && (
                          <CommandItem value="__clear__" onSelect={() => pickLanguage(null)}>
                            <X className="h-3 w-3 text-muted-foreground" />
                            <span className="text-muted-foreground text-xs">Clear</span>
                          </CommandItem>
                        )}
                        {LANGUAGES.map((lang) => (
                          <CommandItem key={lang.code} value={lang.name} onSelect={() => pickLanguage(lang.name)} className="text-xs">
                            <span className="mr-2">{lang.flag}</span>
                            <span>{lang.name}</span>
                            <span className="ml-auto text-[10px] text-muted-foreground">{lang.native}</span>
                            {draftLanguage === lang.name && <Check className="ml-1 h-3 w-3 text-primary" />}
                          </CommandItem>
                        ))}
                      </CommandGroup>
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>
            </SettingsRow>
            <SaveFooter
              dirty={form.isDirty}
              status={form.status}
              error={form.error}
              onSave={handleSave}
              onCancel={form.reset}
            />
          </>
        )}
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
              <span className="h-1.5 w-1.5 rounded-full bg-success" />
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
              <span className="h-1.5 w-1.5 rounded-full bg-purple" />
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

      {/* ── Security ──
          Workspace-wide, fail-closed, and nothing to do with any one crew —
          it used to sit under "Crews & Containers", where it read as per-crew
          container config and an owner had no reason to look for it. It sits
          above the danger zone because it is the other switch on this page
          that changes what the whole workspace is allowed to do. */}
      <PrivilegedCredentialsCard workspaceId={workspaceId} />

      {/* ── Danger Zone ── */}
      {isOwner(role) && (
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
            <AlertDialog
              open={deleteOpen}
              onOpenChange={(o) => { setDeleteOpen(o); if (!o) setConfirmSlug("") }}
            >
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
                <div className="space-y-1.5 py-1">
                  <label htmlFor="confirm-slug" className="text-[11px] text-muted-foreground">
                    Type <span className="font-mono font-medium text-foreground">{orgSlug}</span> to confirm
                  </label>
                  <Input
                    id="confirm-slug"
                    value={confirmSlug}
                    onChange={(e) => setConfirmSlug(e.target.value)}
                    autoComplete="off"
                    autoCorrect="off"
                    spellCheck={false}
                    aria-label="Confirm workspace slug"
                    placeholder={orgSlug}
                    className="h-8 text-xs font-mono"
                    disabled={isDeleting}
                  />
                </div>
                <AlertDialogFooter>
                  <AlertDialogCancel className="h-7 text-xs" disabled={isDeleting}>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={(e) => { e.preventDefault(); void handleDelete() }}
                    className="h-7 text-xs bg-destructive text-destructive-foreground hover:bg-destructive/90"
                    disabled={isDeleting || !slugConfirmed}
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
