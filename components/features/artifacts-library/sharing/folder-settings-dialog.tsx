"use client"

import { useEffect, useState } from "react"
import { FolderCog } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import {
  CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { LevelSelect, SubjectSelect, useSubjects, type Subject } from "./share-controls"
import {
  entryKey, useFolderSettings, useSharingMutation,
  type AccessEntry, type DefaultAudience, type FolderSettings, type ShareLevel,
} from "./sharing-api"

export interface FolderSettingsDialogProps {
  workspaceId: string
  folder: { slug: string; name: string }
  open: boolean
  onOpenChange: (open: boolean) => void
}

const AUDIENCE: { value: DefaultAudience; label: string; hint: string }[] = [
  { value: "private", label: "Private to the creator", hint: "Others see a new artifact once it is shared." },
  { value: "folder", label: "Shared like the folder", hint: "A new artifact opens to everyone this folder shares with." },
]

/** Who a folder shares with and how new artifacts in it start (change 23). */
export function FolderSettingsDialog({ workspaceId, folder, open, onOpenChange }: FolderSettingsDialogProps) {
  const settings = useFolderSettings(workspaceId, open ? folder.slug : null)
  // The form starts from a settled read, never from a cached one being refetched, and keeps its edits after that.
  const [seed, setSeed] = useState<FolderSettings | null>(null)
  const settled = open && settings.data && !settings.isFetching ? settings.data : null
  useEffect(() => { if (!open) setSeed(null); else if (settled) setSeed(current => current ?? settled) }, [open, settled])
  const close = () => onOpenChange(false)
  // The kit's form surface (dialogs-through-kit guard). The form owns the body and the footer once the read has settled.
  return <CreateSurface open={open} onOpenChange={onOpenChange} size="md">
    <CreateSurfaceHeader icon={FolderCog} title="Folder settings" description={folder.name} onClose={close} />
    {seed && !settings.error
      ? <SettingsForm key={folder.slug} workspaceId={workspaceId} folder={folder} initial={seed} onClose={close} />
      : <>
        <CreateSurfaceBody className="text-xs">
          {settings.error ? <p role="alert" className="text-destructive">The settings could not be read: {settings.error.message}</p>
            : <p role="status" className="flex items-center gap-2 text-muted-foreground"><Spinner className="size-3.5" />Reading settings…</p>}
        </CreateSurfaceBody>
        <CreateSurfaceFooter hint={null} onCancel={close} />
      </>}
  </CreateSurface>
}

function SettingsForm({ workspaceId, folder, initial, onClose }: { workspaceId: string; folder: { slug: string }; initial: FolderSettings; onClose: () => void }) {
  const [audience, setAudience] = useState<DefaultAudience>(initial.defaultAudience)
  const [entries, setEntries] = useState<AccessEntry[]>(initial.entries)
  const [subject, setSubject] = useState<Subject | null>(null)
  const [level, setLevel] = useState<ShareLevel>("read")
  const { subjects, error: peopleError } = useSubjects(workspaceId, true)
  const save = useSharingMutation(workspaceId, (api, input: Pick<FolderSettings, "defaultAudience" | "entries">) => api.saveFolderSettings(folder.slug, input))

  const add = () => {
    if (!subject) return
    setEntries(list => [...list.filter(e => entryKey(e) !== entryKey(subject)), { ...subject, level, via: "folder" }])
    setSubject(null)
    setLevel("read")
  }
  const setEntryLevel = (key: string, next: ShareLevel) => setEntries(list => list.map(e => entryKey(e) === key ? { ...e, level: next } : e))
  const remove = (key: string) => setEntries(list => list.filter(e => entryKey(e) !== key))

  const summary = `${entries.length} ${entries.length === 1 ? "entry" : "entries"} · new artifacts ${audience === "private" ? "private" : "shared like the folder"}`

  const submit = () => { if (!save.isPending) save.mutate({ defaultAudience: audience, entries }, { onSuccess: onClose }) }

  return <>
  <CreateSurfaceBody className="flex flex-col gap-4 text-xs">
    <fieldset className="flex flex-col gap-1.5">
      <legend className="mb-1.5 font-medium">New artifacts in this folder</legend>
      {AUDIENCE.map(option => <label key={option.value} className="flex items-start gap-2">
        <input type="radio" name="default-audience" value={option.value} checked={audience === option.value} onChange={() => setAudience(option.value)} className="mt-0.5" />
        <span><span className="block">{option.label}</span><span className="block text-muted-foreground">{option.hint}</span></span>
      </label>)}
    </fieldset>

    <section aria-label="Folder sharing" className="flex flex-col gap-2">
      <h3 className="font-medium">Shared with</h3>
      {entries.length === 0 ? <p className="text-muted-foreground">Only the people who add artifacts here.</p>
        : <ul aria-label="Folder access" className="flex flex-col divide-y divide-border/40 rounded-md border border-border/50">
          {entries.map(entry => <li key={entryKey(entry)} className="flex items-center gap-2 px-2.5 py-1.5">
            <span className="min-w-0 flex-1 truncate">{entry.label}</span>
            <LevelSelect label={`Level for ${entry.label}`} value={entry.level} onChange={next => setEntryLevel(entryKey(entry), next)} />
            <Button type="button" variant="ghost" size="xs" aria-label={`Remove ${entry.label}`} onClick={() => remove(entryKey(entry))}>Remove</Button>
          </li>)}
        </ul>}
      <div className="flex flex-wrap items-center gap-2">
        <SubjectSelect label="Add person or everyone" subjects={subjects} value={subject} onChange={setSubject} exclude={entries.map(entryKey)} />
        <LevelSelect label="Level to add" value={level} onChange={setLevel} />
        <Button type="button" variant="outline" size="xs" disabled={!subject} onClick={add}>Add</Button>
      </div>
      {peopleError && <p role="status" className="text-muted-foreground">People could not be read; Everyone is still available.</p>}
    </section>

    <p role="status" data-slot="folder-settings-summary" className="text-muted-foreground">{summary}</p>
    {save.error && <p role="alert" className="text-destructive">Not saved: {save.error.message}</p>}
  </CreateSurfaceBody>
  <CreateSurfaceFooter hint={null} onCancel={onClose} primaryLabel={save.isPending ? "Saving…" : "Save"} onPrimary={submit} busy={save.isPending} />
  </>
}
