"use client"

import { useIssuePeople } from "@/hooks/use-issue-people"
import { SHARE_LEVEL_LABEL, SHARE_LEVELS, type ShareLevel, type ShareSubjectType } from "./sharing-api"

/** The library's select idiom (artifacts-library Display row). */
export const SELECT = "h-[var(--control-sm)] rounded-md border border-border bg-card px-2 text-xs text-foreground coarse:min-h-[44px]"

export const EVERYONE_LABEL = "Everyone in this workspace"

export interface Subject { subjectType: ShareSubjectType; subjectId: string; label: string }

const subjectValue = (s: Pick<Subject, "subjectType" | "subjectId">) => `${s.subjectType}:${s.subjectId}`

/** Workspace only: a person or everyone. No guests, no links. */
export function useSubjects(workspaceId: string, enabled: boolean): { subjects: Subject[]; error: boolean } {
  const { people, error } = useIssuePeople(workspaceId, enabled)
  return {
    subjects: [{ subjectType: "workspace", subjectId: "", label: EVERYONE_LABEL }, ...people.map(p => ({ subjectType: "user" as const, subjectId: p.id, label: p.name }))],
    error,
  }
}

export function SubjectSelect({ subjects, value, onChange, exclude = [], label = "Who" }: {
  subjects: Subject[]; value: Subject | null; onChange: (subject: Subject | null) => void; exclude?: string[]; label?: string
}) {
  return <select aria-label={label} className={SELECT} value={value ? subjectValue(value) : ""}
    onChange={event => onChange(subjects.find(s => subjectValue(s) === event.target.value) ?? null)}>
    <option value="">Choose who…</option>
    {subjects.filter(s => !exclude.includes(subjectValue(s))).map(s => <option key={subjectValue(s)} value={subjectValue(s)}>{s.label}</option>)}
  </select>
}

export function LevelSelect({ value, onChange, label = "Level", levels = SHARE_LEVELS }: { value: ShareLevel; onChange: (level: ShareLevel) => void; label?: string; levels?: readonly ShareLevel[] }) {
  return <select aria-label={label} className={SELECT} value={value} onChange={event => onChange(event.target.value as ShareLevel)}>
    {levels.map(level => <option key={level} value={level}>{SHARE_LEVEL_LABEL[level]}</option>)}
  </select>
}
