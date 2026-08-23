"use client"

/**
 * Issues — New issue, New project.
 *
 * Both are real field sets, not sketches. Every control below exists in the
 * component named in `audit.ts`, and the count in the door's `fields` array is
 * what the migration is not allowed to drop. The state is local and nothing is
 * written; this is the surface, not a second way to file a bug.
 */

import * as React from "react"
import {
  CalendarDays,
  Check,
  Gauge,
  GitMerge,
  FolderKanban,
  Milestone,
  Palette,
  Paperclip,
  Signal,
  Tag,
  User,
} from "lucide-react"

import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  CREW_ICON_CATEGORIES,
  GRADIENT_PALETTES,
  getCrewIconDef,
  searchCrewIcons,
} from "@/lib/entities"
import {
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceDescriptionInput,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfacePicker,
  CreateSurfacePill,
  CreateSurfacePills,
  CreateSurfaceSection,
  CreateSurfaceTitleInput,
} from "@/components/layout/create-surface"

/* ══ Issues → New issue ═════════════════════════════════════════════════ */

export function NewIssueContent({ onClose }: { onClose: () => void }) {
  const [title, setTitle] = React.useState("")
  const [body, setBody] = React.useState("")
  const [priority, setPriority] = React.useState<"none" | "urgent" | "high" | "medium" | "low">("none")
  const [assignee, setAssignee] = React.useState<string | null>(null)
  const [project, setProject] = React.useState<string | null>(null)
  const [routine, setRoutine] = React.useState<string | null>(null)
  const [labels, setLabels] = React.useState(0)
  const [more, setMore] = React.useState(false)
  // The four the API accepts at create and the modal never offered. They are
  // pills, not fields: optional, one click away, zero decisions away — which
  // is the property that made this surface the reference in the first place.
  const [due, setDue] = React.useState<string | null>(null)
  const [estimate, setEstimate] = React.useState<number | null>(null)
  const [milestone, setMilestone] = React.useState<string | null>(null)
  const [parent, setParent] = React.useState<string | null>(null)

  return (
    <>
      <CreateSurfaceHeader concept="issues" context="Platform" title="New issue" onClose={onClose} />

      {/* The one input treatment in the product that does not look like a
          form. It is why this surface is the reference. */}
      <CreateSurfaceBody className="space-y-1">
        <CreateSurfaceTitleInput
          autoFocus
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Issue title"
        />
        <CreateSurfaceDescriptionInput
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder="Add description…"
        />
      </CreateSurfaceBody>

      <CreateSurfacePills>
        <CreateSurfacePill concept="issues" readOnly>
          Backlog
        </CreateSurfacePill>
        <CreateSurfacePill
          icon={Signal}
          accent="amber"
          set={priority !== "none"}
          onClick={() => setPriority((p) => (p === "none" ? "urgent" : "none"))}
        >
          {priority === "none" ? "Priority" : "Urgent"}
        </CreateSurfacePill>
        <CreateSurfacePill
          icon={User}
          accent="purple"
          set={assignee !== null}
          onClick={() => setAssignee((a) => (a ? null : "keeper"))}
        >
          {assignee ?? "Assignee"}
        </CreateSurfacePill>
        <CreateSurfacePill
          icon={FolderKanban}
          accent="blue"
          set={project !== null}
          onClick={() => setProject((p) => (p ? null : "Release 1.0"))}
        >
          {project ?? "Project"}
        </CreateSurfacePill>
        <CreateSurfacePill
          concept="routines"
          set={routine !== null}
          onClick={() => setRoutine((r) => (r ? null : "nightly-sweep"))}
        >
          {routine ?? "Routine"}
        </CreateSurfacePill>
        <CreateSurfacePill icon={Tag} accent="green" set={labels > 0} onClick={() => setLabels((l) => (l + 1) % 3)}>
          {labels > 0 ? `${labels} label${labels > 1 ? "s" : ""}` : "Labels"}
        </CreateSurfacePill>

        {/* ── Everything below is accepted by POST /issues today and had no
            control anywhere in the modal. See the parity ledger. ── */}
        <CreateSurfacePill
          icon={CalendarDays}
          accent="amber"
          set={due !== null}
          onClick={() => setDue((d) => (d ? null : "Fri 29 Aug"))}
        >
          {due ?? "Due"}
        </CreateSurfacePill>
        <CreateSurfacePill
          icon={Gauge}
          accent="teal"
          set={estimate !== null}
          onClick={() => setEstimate((e) => (e === null ? 3 : e === 3 ? 5 : null))}
        >
          {estimate === null ? "Estimate" : `${estimate} pts`}
        </CreateSurfacePill>
        <CreateSurfacePill
          icon={Milestone}
          accent="purple"
          set={milestone !== null}
          onClick={() => setMilestone((m) => (m ? null : "Release 1.0"))}
        >
          {milestone ?? "Milestone"}
        </CreateSurfacePill>
        <CreateSurfacePill
          icon={GitMerge}
          accent="blue"
          set={parent !== null}
          onClick={() => setParent((p) => (p ? null : "PLA-412"))}
        >
          {parent ? `Sub-issue of ${parent}` : "Parent"}
        </CreateSurfacePill>
      </CreateSurfacePills>

      <CreateSurfaceFooter
        hint={
          <>
            <kbd className="font-mono">⌘↵</kbd> to create · <kbd className="font-mono">Esc</kbd> to cancel
          </>
        }
        aside={
          <label className="mr-1 flex cursor-pointer items-center gap-2 whitespace-nowrap text-xs text-muted-foreground max-sm:hidden">
            <Switch size="sm" checked={more} onCheckedChange={setMore} />
            Create more
          </label>
        }
        onCancel={onClose}
        primaryLabel="Create issue"
        primaryIcon={Paperclip}
        primaryDisabled={!title.trim()}
        onPrimary={onClose}
      />
    </>
  )
}

/* ══ Issues → New project ═══════════════════════════════════════════════ */

export function NewProjectContent({ onClose }: { onClose: () => void }) {
  // Same picker component as the crew's, with the category chips the project
  // one already had. Two surfaces, one control — which is the whole point.
  const [panel, setPanel] = React.useState<null | "icon">(null)
  const [name, setName] = React.useState("")
  const [summary, setSummary] = React.useState("")
  const [description, setDescription] = React.useState("")
  const [icon, setIcon] = React.useState("rocket")
  const [iconSearch, setIconSearch] = React.useState("")
  const [iconCategory, setIconCategory] = React.useState<string | null>(null)
  const [color, setColor] = React.useState("blue")
  const [status, setStatus] = React.useState<"backlog" | "planned" | "started" | "paused">("backlog")
  const [priority, setPriority] = React.useState<"none" | "high" | "medium" | "low">("none")
  const [lead, setLead] = React.useState<string | null>(null)
  const [milestones, setMilestones] = React.useState<string[]>([])

  // The catalogue's own search, so the results here are the results everywhere.
  const iconNames = searchCrewIcons(iconCategory ?? iconSearch)
  const iconOptions = iconNames.map((n) => {
    const def = getCrewIconDef(n)
    return { id: n, label: def.label, render: <def.icon className="h-4 w-4 text-foreground/70" /> }
  })

  return (
    <>
      <CreateSurfaceHeader
        icon={FolderKanban}
        accent="blue"
        context="Platform"
        title={panel === "icon" ? "Icon — new project" : "New project"}
        description={
          panel === "icon" ? "Pick a colour, then an icon. Browse by category, or search." : undefined
        }
        onBack={panel ? () => setPanel(null) : undefined}
        onClose={onClose}
      />

      <CreateSurfaceBody className="space-y-5">
        {panel === "icon" && (
          <CreateSurfacePicker
            preview={<CrewIcon icon={icon} color={color} size="xl" />}
            previewHint={`${getCrewIconDef(icon).label} · ${color}`}
            palette={{
              value: color,
              onChange: setColor,
              options: GRADIENT_PALETTES.map((g) => ({ id: g.id, dot: g.dot })),
            }}
            categories={{
              value: iconCategory,
              options: CREW_ICON_CATEGORIES,
              onChange: (c) => {
                setIconCategory(c)
                setIconSearch("")
              },
            }}
            search={{
              value: iconSearch,
              onChange: (v) => {
                setIconSearch(v)
                setIconCategory(null)
              },
              placeholder: "Search icons…",
            }}
            options={iconOptions}
            value={icon}
            onChange={setIcon}
          />
        )}

        {!panel && (
          <>
        {/* Identity — the icon and colour ARE the project in every list it
            appears in, so they are not buried behind an "appearance" tab. */}
        <CreateSurfaceSection title="Identity" icon={Palette} accent="blue">
          <div className="flex items-start gap-3">
            <button
              type="button"
              aria-label="Change project icon"
              onClick={() => setPanel("icon")}
              className="shrink-0 rounded-xl transition-opacity hover:opacity-80"
            >
              <CrewIcon icon={icon} color={color} size="lg" />
            </button>

            <div className="flex min-w-0 flex-1 flex-col gap-2">
              <CreateSurfaceTitleInput
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Project name"
              />
              <Input
                value={summary}
                onChange={(e) => setSummary(e.target.value)}
                placeholder="Add a short summary…"
                aria-label="Summary"
                className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
              />
            </div>
          </div>
        </CreateSurfaceSection>

        <CreateSurfaceSection title="Brief" hint="shown on the project page">
          <CreateSurfaceDescriptionInput
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Write a description, a project brief, or collect ideas…"
            rows={4}
            className="rounded-lg border border-hairline bg-foreground/[0.02] p-2.5 text-xs"
          />
        </CreateSurfaceSection>

        <CreateSurfaceSection title="Planning" icon={CalendarDays} accent="amber">
          <CreateSurfaceGrid>
            <CreateSurfaceField label="Status">
              <CreateSurfaceChoice
                ariaLabel="Project status"
                value={status}
                onChange={setStatus}
                options={[
                  { value: "backlog", label: "Backlog" },
                  { value: "planned", label: "Planned" },
                  { value: "started", label: "Started" },
                  { value: "paused", label: "Paused" },
                ]}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Priority">
              <CreateSurfaceChoice
                ariaLabel="Project priority"
                value={priority}
                onChange={setPriority}
                options={[
                  { value: "none", label: "None" },
                  { value: "low", label: "Low" },
                  { value: "medium", label: "Med" },
                  { value: "high", label: "High" },
                ]}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Start date" htmlFor="proj-start">
              <Input id="proj-start" type="date" className="h-8 text-xs max-sm:h-12 max-sm:text-sm" />
            </CreateSurfaceField>
            <CreateSurfaceField label="Target date" htmlFor="proj-target">
              <Input id="proj-target" type="date" className="h-8 text-xs max-sm:h-12 max-sm:text-sm" />
            </CreateSurfaceField>
          </CreateSurfaceGrid>
        </CreateSurfaceSection>

        <CreateSurfaceSection title="Milestones" icon={Milestone} accent="purple" hint="optional">
          {milestones.map((m, i) => (
            <div key={i} className="flex items-center gap-2">
              <Check className="h-3.5 w-3.5 shrink-0 text-success" />
              <Input
                defaultValue={m}
                aria-label={`Milestone ${i + 1}`}
                className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
              />
            </div>
          ))}
          <button
            type="button"
            onClick={() => setMilestones((ms) => [...ms, `Milestone ${ms.length + 1}`])}
            className="self-start rounded-md border border-dashed border-border px-2.5 py-1.5 text-xs text-muted-foreground transition-colors hover:border-primary/40 hover:text-foreground max-sm:w-full max-sm:py-3"
          >
            + Add milestone
          </button>
        </CreateSurfaceSection>
          </>
        )}
      </CreateSurfaceBody>

      {!panel && <CreateSurfacePills>
        <CreateSurfacePill
          icon={User}
          accent="purple"
          set={lead !== null}
          onClick={() => setLead((l) => (l ? null : "keeper"))}
        >
          {lead ?? "Lead"}
        </CreateSurfacePill>
        <CreateSurfacePill icon={Tag} accent="green">
          Labels
        </CreateSurfacePill>
      </CreateSurfacePills>}

      <CreateSurfaceFooter
        onCancel={panel ? () => setPanel(null) : onClose}
        cancelLabel={panel ? "Back" : "Cancel"}
        primaryLabel={panel ? "Use this icon" : "Create project"}
        primaryDisabled={panel ? false : !name.trim()}
        onPrimary={panel ? () => setPanel(null) : onClose}
      />
    </>
  )
}
