"use client"

import { useState } from "react"
import { Check, X, ChevronsUpDown, AlertTriangle, Bot, Building, Gauge, User, Users } from "lucide-react"
import { toast } from "@/lib/toast"
import { Input } from "@/components/ui/input"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { Button } from "@/components/ui/button"
import { LANGUAGES } from "@/lib/languages"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier, isOwner } from "@/lib/permissions/tiers"
import { SettingsCard, SettingsCounts, SettingsRow, SettingsDangerCard, settingsControl, settingsPickerButton } from "@/components/features/settings/shared"
import { PagesAppearanceCard } from "./pages-appearance-card"
import { WorkspaceLogoRow } from "./workspace-logo-row"
import { cn } from "@/lib/utils"
import { PrivilegedCredentialsCard } from "@/components/features/settings/sections/privileged-credentials-card"
import { AvatarBackfillButton } from "@/components/features/crews/avatar-backfill-button"

interface GeneralSectionProps {
  workspaceId: string
  orgName: string
  orgSlug: string
  /** The workspace's uploaded logo (#3005), or null. */
  logoUrl?: string | null
  preferredLanguage: string | null
  agentCount: number
  crewCount: number
  memberCount: number
  role: string | null
  onUpdated: (org: { name: string; slug: string; preferred_language: string | null }) => void
  onLogoChange?: (logoUrl: string | null) => void
  onDelete: () => void
}

export function GeneralSection({
  workspaceId, orgName, orgSlug, logoUrl, preferredLanguage,
  agentCount, crewCount, memberCount, role, onUpdated, onLogoChange, onDelete,
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
  const [deleteOpen, setDeleteOpen] = useState(false)

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

  // ConfirmDialog has the slug typed back before it enables the button, and
  // stays open until this resolves: a refusal throws, the person keeps the
  // dialog and what they typed, and the reason is the sticky corner toast.
  async function handleDelete() {
    let res: Response
    try {
      res = await apiFetch(`/api/v1/workspaces/${workspaceId}?workspace_id=${workspaceId}`, {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        // The server re-validates confirm_slug against the workspace slug;
        // the dialog only enables once the typed text equals it.
        body: JSON.stringify({ confirm_slug: orgSlug }),
      })
    } catch (e) {
      toast.error("Couldn’t delete the workspace", { description: e instanceof Error ? e.message : "Network error." })
      throw e
    }
    if (!res.ok) {
      const body = await res.json().catch(() => null)
      // RFC 7807 problems use `detail`; legacy errors use `error`.
      const msg = body?.detail ?? body?.error
      const description = typeof msg === "string" ? msg : `Request failed (${res.status}).`
      toast.error("Couldn’t delete the workspace", { description })
      throw new Error(description)
    }
    onDelete()
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
      <SettingsCard icon={Building}
        title="Identity"
        // Who may change the card, said once on the right of its header.
        actions={<span className="text-label text-muted-foreground">Admins and owners</span>}
        description={
          canEdit
            ? undefined
            // Stated once, in the description, in the same muted type as every
            // other hint — a non-admin viewing their own workspace is normal,
            // so it must not read like a permission error.
            : "Only workspace admins can change these."
        }
      >
        <WorkspaceLogoRow workspaceId={workspaceId} name={orgName} logoUrl={logoUrl} canEdit={canEdit} onChange={(u) => onLogoChange?.(u)} />
        {!canEdit ? (
          <>
            <SettingsRow label="Workspace name" lock="Admins and owners">
              <span className="text-control text-foreground truncate">{orgName}</span>
            </SettingsRow>
            <SettingsRow label="Slug" description="Used in URLs and CLI commands" lock="Admins and owners">
              <span className="text-control font-mono text-foreground truncate">{orgSlug}</span>
            </SettingsRow>
            <SettingsRow
              label="Agent language"
              description="Agents will respond in this language"
              lock="Admins and owners"
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
                className={settingsControl}
              />
            </SettingsRow>
            <SettingsRow label="Slug" description="Used in URLs and CLI commands">
              <Input
                value={form.draft.slug}
                onChange={(e) => form.set("slug", e.target.value)}
                placeholder="my-company"
                aria-label="Slug"
                className={cn(settingsControl, "font-mono")}
              />
            </SettingsRow>
            <SettingsRow label="Agent language" description="Agents will respond in this language" border={false}>
              <Popover open={langOpen} onOpenChange={setLangOpen}>
                <PopoverTrigger asChild>
                  <button
                    className={settingsPickerButton}
                    disabled={form.status === "saving"}
                  >
                    {selectedLang ? (
                      <span className="truncate">{selectedLang.flag} {selectedLang.name}</span>
                    ) : (
                      <span className="text-muted-foreground">Select language…</span>
                    )}
                    <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
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
                            <span className="ml-auto text-micro text-muted-foreground">{lang.native}</span>
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
              count={form.dirtyCount}
              status={form.status}
              error={form.error}
              onSave={handleSave}
              onCancel={form.reset}
            />
          </>
        )}
      </SettingsCard>

      {/* ── Usage ── */}
      <SettingsCard icon={Gauge} title="Usage" description="Resource counts for this workspace">
        <SettingsCounts
          items={[
            { icon: Bot, label: "Agents", value: <AnimatedNumber value={agentCount} /> },
            { icon: Users, label: "Crews", value: <AnimatedNumber value={crewCount} /> },
            { icon: User, label: "Members", value: <AnimatedNumber value={memberCount} /> },
          ]}
        />
        {canEdit && agentCount > 0 && (
          <SettingsRow label="Agent avatars" description="For agents created before avatars existed">
            <AvatarBackfillButton workspaceId={workspaceId} />
          </SettingsRow>
        )}
      </SettingsCard>

      {/* Below identity and usage: it themes Page applications only. */}
      <PagesAppearanceCard key={workspaceId} workspaceId={workspaceId} role={role} />

      {/* ── Security ──
          Workspace-wide, fail-closed, and nothing to do with any one crew —
          it used to sit under "Crews & Containers", where it read as per-crew
          container config and an owner had no reason to look for it. It sits
          above the danger zone because it is the other switch on this page
          that changes what the whole workspace is allowed to do. */}
      <PrivilegedCredentialsCard workspaceId={workspaceId} />

      {/* ── Danger Zone ── */}
      {isOwner(role) && (
        <SettingsDangerCard icon={AlertTriangle}
          title="Danger zone"
          description="Owner only"
        >
          <SettingsRow label="Delete workspace" description="Permanently delete all crews, agents, and data" border={false}>
            <Button variant="destructive" size="sm" onClick={() => setDeleteOpen(true)}>
              Delete workspace…
            </Button>
          </SettingsRow>
          <ConfirmDialog
            open={deleteOpen}
            onOpenChange={setDeleteOpen}
            title={`Delete ${orgName}?`}
            description="The workspace and everything in it are deleted for every member."
            consequences={[
              { tone: "lost", text: "All crews, agents, credentials, routines, pages and their history" },
              { tone: "lost", text: "Every member loses access; the slug and its URLs stop working" },
              { tone: "warn", text: "This cannot be undone." },
            ]}
            typeToConfirm={orgSlug}
            confirmLabel="Delete workspace"
            destructive
            onConfirm={handleDelete}
          />
        </SettingsDangerCard>
      )}
    </div>
  )
}
