"use client"

import { useState } from "react"
import { Check, Loader2, Pencil, Plus, Trash2, X } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { IssueLabel } from "@/lib/types/mission"

interface LabelsDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  labels: IssueLabel[]
  workspaceId: string
  onLabelsChanged: () => void
}

// Inline styles are used for color swatches and label dots because label colors
// are stored as hex values in the database. Arbitrary hex colors from user-defined
// labels cannot be reliably mapped to Tailwind utility classes.
const PRESET_COLORS = [
  { name: "Red", value: "#EF4444" },
  { name: "Orange", value: "#F97316" },
  { name: "Yellow", value: "#EAB308" },
  { name: "Green", value: "#22C55E" },
  { name: "Blue", value: "#3B82F6" },
  { name: "Purple", value: "#A855F7" },
  { name: "Pink", value: "#EC4899" },
  { name: "Gray", value: "#6B7280" },
]

export function LabelsDialog({
  open,
  onOpenChange,
  labels,
  workspaceId,
  onLabelsChanged,
}: LabelsDialogProps) {
  const [newName, setNewName] = useState("")
  const [newColor, setNewColor] = useState(PRESET_COLORS[4].value)
  const [newGroup, setNewGroup] = useState("")
  const [creating, setCreating] = useState(false)

  const [editingId, setEditingId] = useState<string | null>(null)
  const [editName, setEditName] = useState("")
  const [editColor, setEditColor] = useState("")
  const [saving, setSaving] = useState(false)

  const [deletingId, setDeletingId] = useState<string | null>(null)
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null)

  async function handleCreate() {
    if (creating) return
    if (!newName.trim()) {
      toast.error("Label name is required")
      return
    }
    setCreating(true)
    try {
      const res = await fetch(
        `/api/v1/labels?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: newName.trim(),
            color: newColor,
            label_group: newGroup.trim() || undefined,
          }),
        },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to create label")
        return
      }
      toast.success("Label created")
      setNewName("")
      setNewGroup("")
      onLabelsChanged()
    } catch {
      toast.error("Failed to create label")
    } finally {
      setCreating(false)
    }
  }

  async function handleUpdate(id: string) {
    if (saving) return
    if (!editName.trim()) {
      toast.error("Label name is required")
      return
    }
    setSaving(true)
    try {
      const res = await fetch(
        `/api/v1/labels/${id}?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: editName.trim(),
            color: editColor,
          }),
        },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to update label")
        return
      }
      toast.success("Label updated")
      setEditingId(null)
      onLabelsChanged()
    } catch {
      toast.error("Failed to update label")
    } finally {
      setSaving(false)
    }
  }

  async function handleDelete(id: string) {
    if (deletingId === id) return
    setDeletingId(id)
    try {
      const res = await fetch(
        `/api/v1/labels/${id}?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "DELETE",
        },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? "Failed to delete label")
        return
      }
      toast.success("Label deleted")
      setConfirmDeleteId(null)
      onLabelsChanged()
    } catch {
      toast.error("Failed to delete label")
    } finally {
      setDeletingId(null)
    }
  }

  function startEdit(label: IssueLabel) {
    setEditingId(label.id)
    setEditName(label.name)
    setEditColor(label.color)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle>Manage Labels</DialogTitle>
        </DialogHeader>

        {/* Create form */}
        <div className="space-y-3 border-b border-border pb-4">
          <div className="flex items-center gap-2">
            <Input
              placeholder="Label name"
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              className="h-8 text-sm flex-1"
              onKeyDown={(e) => {
                if (e.key === "Enter") handleCreate()
              }}
            />
            <Input
              placeholder="Group (optional)"
              value={newGroup}
              onChange={(e) => setNewGroup(e.target.value)}
              className="h-8 text-sm w-[120px]"
            />
          </div>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-1">
              {PRESET_COLORS.map((color) => (
                <button
                  key={color.value}
                  type="button"
                  className={cn(
                    "h-6 w-6 rounded-full border-2 transition-all",
                    newColor === color.value
                      ? "border-foreground scale-110"
                      : "border-transparent hover:border-muted-foreground/40",
                  )}
                  style={{ backgroundColor: color.value }}
                  onClick={() => setNewColor(color.value)}
                  title={color.name}
                  aria-label={`Select ${color.name} color`}
                />
              ))}
            </div>
            <Button
              size="sm"
              className="h-7 text-xs gap-1"
              onClick={handleCreate}
              disabled={creating || !newName.trim()}
            >
              {creating ? (
                <Loader2 className="h-3 w-3 animate-spin" />
              ) : (
                <Plus className="h-3 w-3" />
              )}
              Add
            </Button>
          </div>
        </div>

        {/* Labels list */}
        <div className="max-h-[320px] overflow-y-auto space-y-1">
          {labels.length === 0 && (
            <p className="text-sm text-muted-foreground py-4 text-center">
              No labels yet. Create one above.
            </p>
          )}
          {labels.map((label) => (
            <div
              key={label.id}
              className="flex items-center gap-2 rounded-md px-2 py-1.5 hover:bg-accent/50 group"
            >
              {editingId === label.id ? (
                <>
                  <div className="flex items-center gap-1 flex-1">
                    <div className="flex items-center gap-1 shrink-0">
                      {PRESET_COLORS.map((color) => (
                        <button
                          key={color.value}
                          type="button"
                          className={cn(
                            "h-4 w-4 rounded-full border transition-all",
                            editColor === color.value
                              ? "border-foreground"
                              : "border-transparent",
                          )}
                          style={{ backgroundColor: color.value }}
                          onClick={() => setEditColor(color.value)}
                          title={color.name}
                          aria-label={`Select ${color.name} color`}
                        />
                      ))}
                    </div>
                    <Input
                      value={editName}
                      onChange={(e) => setEditName(e.target.value)}
                      className="h-7 text-xs flex-1 ml-1"
                      autoFocus
                      onKeyDown={(e) => {
                        if (e.key === "Enter") handleUpdate(label.id)
                        if (e.key === "Escape") setEditingId(null)
                      }}
                    />
                  </div>
                  <Button
                    size="icon"
                    variant="ghost"
                    className="h-6 w-6 shrink-0"
                    onClick={() => handleUpdate(label.id)}
                    disabled={saving}
                    aria-label="Save changes"
                  >
                    {saving ? (
                      <Loader2 className="h-3 w-3 animate-spin" />
                    ) : (
                      <Check className="h-3 w-3" />
                    )}
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    className="h-6 w-6 shrink-0"
                    onClick={() => setEditingId(null)}
                    aria-label="Cancel editing"
                  >
                    <X className="h-3 w-3" />
                  </Button>
                </>
              ) : (
                <>
                  <span
                    className="h-3 w-3 rounded-full shrink-0"
                    style={{ backgroundColor: label.color }}
                  />
                  <span
                    className="text-sm flex-1 cursor-pointer hover:underline"
                    role="button"
                    tabIndex={0}
                    onClick={() => startEdit(label)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault()
                        startEdit(label)
                      }
                    }}
                  >
                    {label.name}
                  </span>
                  {label.label_group && (
                    <span className="text-[10px] text-muted-foreground bg-muted px-1.5 py-0.5 rounded">
                      {label.label_group}
                    </span>
                  )}
                  <Button
                    size="icon"
                    variant="ghost"
                    className="h-6 w-6 shrink-0 opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
                    onClick={() => startEdit(label)}
                    aria-label={`Edit ${label.name}`}
                  >
                    <Pencil className="h-3 w-3" />
                  </Button>
                  {confirmDeleteId === label.id ? (
                    <div className="flex items-center gap-1">
                      <Button
                        size="sm"
                        variant="destructive"
                        className="h-6 text-[10px] px-2"
                        onClick={() => handleDelete(label.id)}
                        disabled={deletingId === label.id}
                        aria-label={`Confirm delete ${label.name}`}
                      >
                        {deletingId === label.id ? (
                          <Loader2 className="h-3 w-3 animate-spin" />
                        ) : (
                          "Delete"
                        )}
                      </Button>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="h-6 w-6"
                        onClick={() => setConfirmDeleteId(null)}
                        aria-label="Cancel delete"
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </div>
                  ) : (
                    <Button
                      size="icon"
                      variant="ghost"
                      className="h-6 w-6 shrink-0 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 text-muted-foreground hover:text-destructive"
                      onClick={() => setConfirmDeleteId(label.id)}
                      aria-label={`Delete ${label.name}`}
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  )}
                </>
              )}
            </div>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
