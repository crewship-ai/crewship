"use client"

import { Database, Plus, ShieldCheck } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { BackupCreateDialog } from "@/components/admin/backup-create-dialog"
import { BackupInspectPanel } from "@/components/admin/backup-inspect-panel"
import { BackupList } from "@/components/admin/backup-list"
import { BackupMetricsRow } from "@/components/admin/backup-metrics-row"
import { BackupRestoreDialog } from "@/components/admin/backup-restore-dialog"
import { BackupRetentionCard } from "@/components/admin/backup-retention-card"
import { BackupSelfTestCard } from "@/components/admin/backup-self-test-card"
import { BackupStatusBanner } from "@/components/admin/backup-status-banner"
import { useBackupStore } from "@/stores/backup-store"

// =============================================================================
// Backups, in the same three-band shape as every other detail surface:
//
//   What a backup is       the one question the screen never answered
//   Bundles                what exists, and making another
//   Keeping it honest      retention, and proof that a restore works
//
// The facts in the first band are measured, not described. On dev3, 2026-07-29:
//   crewship backup create --scope=workspace  → 400.6 MiB, AGE, format v2, 12s
//   crewship backup verify <bundle>           → VALID
//   crewship backup inspect <bundle>          → manifest read without the key
//   crewship backup self-test --crew ops      → ok:true, 48ms full round-trip
//
// The old layout opened with a green "Idle — no backup in progress" banner and
// a warning telling every reader to set CREWSHIP_INSTANCE_OWNER_EMAIL. Two
// alerts above the content, neither of them a problem: the first says nothing
// is wrong, the second is an instruction to an operator who is probably not
// the person reading. An alert should mean something is actually wrong.
// =============================================================================

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-1 items-baseline gap-2 border-b border-hairline px-4 py-2 last:border-b-0 md:grid-cols-[170px_minmax(0,1fr)]">
      <span className="type-row font-medium text-foreground">{label}</span>
      <span className="type-row leading-snug text-muted-foreground">{children}</span>
    </div>
  )
}

function Band({ title, note, action, children }: {
  title: string
  note: string
  action?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section>
      <div className="mb-2 flex items-baseline gap-2">
        <h2 className="type-section text-foreground/70">{title}</h2>
        <span className="type-meta text-muted-foreground-soft">{note}</span>
        {action && <span className="ml-auto">{action}</span>}
      </div>
      {children}
    </section>
  )
}

export function BackupsTab({ workspaceId }: { workspaceId: string | undefined }) {
  const openCreate = useBackupStore((s) => s.openCreate)

  return (
    <div className="@container space-y-5">
      {/* The screen used to go straight from a Create button to a list of
          files, without ever saying what a backup covers or whether it can be
          read on another machine — the two things you want to know before you
          rely on one. */}
      <Band title="What a backup is" note="before you rely on one">
        <div className="grid gap-3.5 @4xl:grid-cols-2">
          <DetailCard bare icon={Database} title="What goes in">
            <Fact label="Scope">A whole workspace, or a single crew.</Fact>
            <Fact label="Database">
              Around thirty tables — crews, agents, chats, journal, memberships,
              inbox, notification wiring, cost ledger, feature flags.
            </Fact>
            <Fact label="Files">
              Workspace files and chat attachments, plus each crew container&rsquo;s
              working directory.
            </Fact>
            <Fact label="Not included">
              Credential plaintext. Secrets stay sealed by the vault, so a bundle
              cannot leak one.
            </Fact>
          </DetailCard>

          <DetailCard
            bare icon={ShieldCheck} title="How it is stored"
            footer="The manifest is plaintext on purpose: you can see what a bundle is, and which schema it came from, before deciding to decrypt it."
          >
            <Fact label="Encryption">
              AGE (age-v1), scrypt-derived from your passphrase. A recipient key
              works too, for backups nobody should hold a passphrase for.
            </Fact>
            <Fact label="Integrity">
              SHA-256 over the payload, checkable with{" "}
              <code className="type-meta font-mono">crewship backup verify</code>{" "}
              without restoring anything.
            </Fact>
            <Fact label="Portability">
              The manifest records every schema migration in the source, so a
              bundle can be restored onto another instance.
            </Fact>
            <Fact label="Location">
              On the server, under{" "}
              <code className="type-meta font-mono">~/.crewship/backups</code>.
            </Fact>
          </DetailCard>
        </div>
      </Band>

      <Band
        title="Bundles"
        note="what exists on this instance"
        action={
          <Button variant="soft" size="sm" onClick={openCreate} data-testid="backup-create-btn">
            <Plus />
            Create backup
          </Button>
        }
      >
        <div className="space-y-3.5">
          {/* Stays above the list: a stuck lock is the one genuinely wrong
              thing on this screen, and it decides whether the list is current. */}
          <BackupStatusBanner workspaceId={workspaceId} />
          <BackupList workspaceId={workspaceId} />
          {/* Counters below the list, not above it. They are context for what
              you are looking at, not the headline. */}
          <BackupMetricsRow workspaceId={workspaceId} />
        </div>
      </Band>

      <Band title="Keeping it honest" note="what gets deleted, and whether a restore actually works">
        <div className="grid gap-3.5 @4xl:grid-cols-2">
          <BackupRetentionCard workspaceId={workspaceId} />
          <BackupSelfTestCard workspaceId={workspaceId} />
        </div>
      </Band>

      {/* Dialogs / panels mounted last so they overlay everything else. */}
      <BackupCreateDialog workspaceId={workspaceId} />
      <BackupRestoreDialog workspaceId={workspaceId} />
      <BackupInspectPanel workspaceId={workspaceId} />
    </div>
  )
}
