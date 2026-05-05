"use client"

import { useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  AlertTriangle,
  CheckCircle2,
  FileCheck2,
  Loader2,
  Lock,
  XCircle,
} from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { useBackupStore } from "@/stores/backup-store"
import { useInspectBackup, useRestoreBackup } from "@/hooks/use-backups"
import { cn } from "@/lib/utils"

/**
 * Restore is the most-destructive admin operation in the product.
 * Following the Supabase pattern, this dialog requires:
 *
 *  1. The operator to read what WILL and WILL NOT be restored
 *     (manifest contents + per-instance fields that aren't in the
 *     bundle by design — users, sessions, audit logs, secrets).
 *  2. An explicit Dry-run pass before the destructive Apply (much
 *     less hand-wavy than the prior checkbox affordance — Apply is
 *     disabled until Dry-run has been completed once OR the operator
 *     has typed the workspace/crew name into the confirm field).
 *  3. Type-name confirmation in the Apply step. The expected name is
 *     pulled from the manifest contents (workspace.slug for workspace
 *     scope, first crew slug for crew scope) so the operator can't
 *     muscle-memory their way past it.
 *
 * The dialog also surfaces estimated downtime + the "what's not in
 * this bundle" disclosure that operators have asked for repeatedly:
 * a multi-container product needs to make it loud what state is
 * preserved across a restore.
 */

const ALWAYS_INCLUDED = [
  "Workspace settings & preferences",
  "All crews (configs, network policies, container limits)",
  "All agents (system prompts, MCP bindings, integrations)",
  "Skills, memory, chat history",
  "Devcontainer + mise configs",
  "Container filesystem snapshots",
] as const

const NEVER_INCLUDED = [
  "Users & workspace members (per-instance)",
  "Credentials & OAuth tokens (handled separately)",
  "Active sessions (you'll be logged out)",
  "Audit logs (preserved on the target)",
  "Running processes & in-flight requests",
] as const

export function BackupRestoreDialog({ workspaceId }: { workspaceId: string | undefined }) {
  const dialog = useBackupStore((s) => s.dialog)
  const selectedPath = useBackupStore((s) => s.selectedPath)
  const close = useBackupStore((s) => s.close)
  const restore = useRestoreBackup(workspaceId)
  const open = dialog === "restore" && Boolean(selectedPath)

  // Pre-fetch manifest so the dialog renders with real bundle metadata
  // (scope, target crew names) the operator can verify before typing
  // the confirmation. Falls back to the file path display if manifest
  // can't be read (encryption + no passphrase, ancient format, etc.).
  const inspect = useInspectBackup(workspaceId, open ? selectedPath : null)

  const [passphrase, setPassphrase] = useState("")
  const [asWorkspace, setAsWorkspace] = useState("")
  const [asCrew, setAsCrew] = useState("")
  const [dryRunPassed, setDryRunPassed] = useState(false)
  const [confirmText, setConfirmText] = useState("")

  // Reset session-only state when the dialog closes for any reason.
  // Wiping passphrase + dryRunPassed is the load-bearing part — the
  // confirmation gate must reset between dialog opens.
  function resetAndClose() {
    setPassphrase("")
    setAsWorkspace("")
    setAsCrew("")
    setDryRunPassed(false)
    setConfirmText("")
    close()
  }

  // Derive the expected confirmation name from manifest. Workspace
  // scope: workspace.slug (or as_workspace if the operator is renaming).
  // Crew scope: first crew's slug. If the manifest can't be read, fall
  // back to the file's basename so the operator still has SOMETHING
  // distinct to type.
  const expectedConfirm = useMemo(() => {
    if (asWorkspace.trim()) return asWorkspace.trim()
    if (asCrew.trim()) return asCrew.trim()
    if (!inspect.data) return selectedPath?.split("/").pop() ?? ""
    if (inspect.data.scope === "workspace") {
      return inspect.data.contents.workspace?.slug ?? ""
    }
    return inspect.data.contents.crews[0]?.slug ?? ""
  }, [inspect.data, selectedPath, asWorkspace, asCrew])

  // Reset dry-run consent if the operator changes the rename targets
  // — the prior dry-run validated against a different target name, so
  // it should not authorize the new (different) Apply.
  useEffect(() => {
    setDryRunPassed(false)
    setConfirmText("")
  }, [asWorkspace, asCrew])

  async function runDry() {
    if (!selectedPath) return
    try {
      await restore.mutateAsync({
        path: selectedPath,
        passphrase: passphrase || undefined,
        as_workspace: asWorkspace.trim() || undefined,
        as_crew: asCrew.trim() || undefined,
        dry_run: true,
      })
      setDryRunPassed(true)
      toast.success("Dry-run passed — checksum + compat OK", {
        description: "Apply is now enabled — type the confirmation name below",
      })
    } catch (err) {
      setDryRunPassed(false)
      toast.error("Dry-run failed", {
        description: err instanceof Error ? err.message : "Unknown error",
      })
    }
  }

  async function runApply() {
    if (!selectedPath) return
    if (!dryRunPassed) {
      toast.error("Run Dry-run first")
      return
    }
    if (confirmText.trim() !== expectedConfirm) {
      toast.error(`Type "${expectedConfirm}" exactly to confirm`)
      return
    }
    try {
      await restore.mutateAsync({
        path: selectedPath,
        passphrase: passphrase || undefined,
        as_workspace: asWorkspace.trim() || undefined,
        as_crew: asCrew.trim() || undefined,
        dry_run: false,
      })
      toast.success("Restore completed")
      resetAndClose()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to restore backup")
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && resetAndClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="text-destructive">Restore from backup</DialogTitle>
          <DialogDescription>
            This will overwrite the current state. Read the manifest before applying.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {/* Manifest summary — pulls real values from /inspect */}
          <div className="rounded-md border border-border/60 bg-muted/30 p-3 text-xs space-y-1.5 font-mono">
            {inspect.isLoading ? (
              <Skeleton className="h-16" />
            ) : inspect.isError ? (
              <div className="text-destructive flex items-start gap-2">
                <XCircle className="h-3.5 w-3.5 mt-0.5 shrink-0" />
                <div>
                  <div>Could not read manifest.</div>
                  <div className="opacity-70 text-[11px]">
                    {selectedPath} — bundle may be encrypted (provide passphrase below)
                    or in an unsupported format.
                  </div>
                </div>
              </div>
            ) : inspect.data ? (
              <>
                <div>
                  <span className="text-muted-foreground">Bundle:</span>{" "}
                  {selectedPath?.split("/").pop()}
                </div>
                <div>
                  <span className="text-muted-foreground">Scope:</span>{" "}
                  {inspect.data.scope}
                  {inspect.data.scope === "workspace" && inspect.data.contents.workspace
                    ? ` · ${inspect.data.contents.workspace.slug}`
                    : ""}
                </div>
                <div>
                  <span className="text-muted-foreground">Created:</span>{" "}
                  {new Date(inspect.data.created_at).toLocaleString()}
                </div>
                <div>
                  <span className="text-muted-foreground">Crews:</span>{" "}
                  {inspect.data.contents.crews.length}
                  {inspect.data.contents.crews.length > 0
                    ? ` (${inspect.data.contents.crews
                        .slice(0, 4)
                        .map((c) => c.slug)
                        .join(", ")}${inspect.data.contents.crews.length > 4 ? ", …" : ""})`
                    : ""}
                </div>
                <div>
                  <span className="text-muted-foreground">Encryption:</span>{" "}
                  {inspect.data.encryption.enabled ? (
                    <span className="text-emerald-500 inline-flex items-center gap-1">
                      <Lock className="h-3 w-3" />
                      {inspect.data.encryption.algorithm ?? "encrypted"}
                    </span>
                  ) : (
                    <span className="text-amber-500">unencrypted</span>
                  )}
                </div>
                <div>
                  <span className="text-muted-foreground">Format version:</span>{" "}
                  v{inspect.data.format_version}
                </div>
              </>
            ) : null}
          </div>

          {/* What's included / what's not */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div className="rounded-md border border-emerald-500/30 bg-emerald-500/5 p-3">
              <div className="flex items-center gap-1.5 text-[10px] font-bold uppercase tracking-wider text-emerald-500 mb-1.5">
                <CheckCircle2 className="h-3 w-3" /> Included
              </div>
              <ul className="text-xs text-foreground/80 space-y-0.5 list-disc list-inside">
                {ALWAYS_INCLUDED.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
            </div>
            <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3">
              <div className="flex items-center gap-1.5 text-[10px] font-bold uppercase tracking-wider text-destructive mb-1.5">
                <XCircle className="h-3 w-3" /> NOT included
              </div>
              <ul className="text-xs text-foreground/70 space-y-0.5 list-disc list-inside">
                {NEVER_INCLUDED.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
            </div>
          </div>

          {/* Inputs */}
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="restore-passphrase" className="text-xs">
                Passphrase {inspect.data?.encryption.enabled ? "" : "(empty for unencrypted)"}
              </Label>
              <Input
                id="restore-passphrase"
                type="password"
                autoComplete="current-password"
                value={passphrase}
                onChange={(e) => setPassphrase(e.target.value)}
              />
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="restore-as-workspace" className="text-xs">
                  Rename workspace (optional)
                </Label>
                <Input
                  id="restore-as-workspace"
                  value={asWorkspace}
                  onChange={(e) => setAsWorkspace(e.target.value)}
                  placeholder={inspect.data?.contents.workspace?.slug ?? "new-slug"}
                />
                <p className="text-[11px] text-muted-foreground">
                  Regenerates IDs to avoid collision with existing workspace.
                </p>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="restore-as-crew" className="text-xs">
                  Rename crew (crew scope only)
                </Label>
                <Input
                  id="restore-as-crew"
                  value={asCrew}
                  onChange={(e) => setAsCrew(e.target.value)}
                  placeholder={inspect.data?.contents.crews[0]?.slug ?? "new-crew-slug"}
                  disabled={inspect.data?.scope !== "crew"}
                />
              </div>
            </div>
          </div>

          {/* Apply gate — confirm by typing the expected name */}
          <AnimatePresence>
            {dryRunPassed && (
              <motion.div
                initial={{ opacity: 0, height: 0 }}
                animate={{ opacity: 1, height: "auto" }}
                exit={{ opacity: 0, height: 0 }}
                transition={{ duration: 0.18, ease: "easeOut" }}
                className="overflow-hidden"
              >
                <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-3 space-y-2">
                  <div className="flex items-start gap-2 text-xs">
                    <AlertTriangle className="h-3.5 w-3.5 text-amber-500 mt-0.5 shrink-0" />
                    <div>
                      <strong className="text-foreground">Irreversible.</strong>{" "}
                      Containers will restart. In-flight runs will be terminated. Active sessions invalidated.
                    </div>
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="restore-confirm" className="text-xs">
                      Type{" "}
                      <code className="font-mono bg-muted px-1 rounded text-foreground">
                        {expectedConfirm}
                      </code>{" "}
                      to confirm
                    </Label>
                    <Input
                      id="restore-confirm"
                      value={confirmText}
                      onChange={(e) => setConfirmText(e.target.value)}
                      autoComplete="off"
                      placeholder={expectedConfirm}
                      className={cn(
                        "font-mono",
                        confirmText && confirmText !== expectedConfirm && "border-destructive",
                      )}
                    />
                  </div>
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        </div>

        <DialogFooter className="gap-2">
          <Button type="button" variant="ghost" onClick={resetAndClose}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={restore.isPending}
            onClick={runDry}
            title="Validate checksum and compatibility — no writes"
          >
            {restore.isPending && restore.variables?.dry_run ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" />
            ) : (
              <FileCheck2 className="h-3.5 w-3.5 mr-1" />
            )}
            Dry-run
          </Button>
          <Button
            type="button"
            variant="destructive"
            disabled={!dryRunPassed || confirmText.trim() !== expectedConfirm || restore.isPending}
            onClick={runApply}
          >
            {restore.isPending && !restore.variables?.dry_run ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" />
            ) : null}
            Apply restore
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
