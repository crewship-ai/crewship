import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

// The tab is a composition; the structure is what is under test, so the parts
// that fetch are stubbed and only the arrangement is real. Factories are
// inlined because vi.mock is hoisted above any local helper.
vi.mock("@/components/admin/backup-create-dialog", () => ({ BackupCreateDialog: () => <div data-testid="stub-BackupCreateDialog" /> }))
vi.mock("@/components/admin/backup-inspect-panel", () => ({ BackupInspectPanel: () => <div data-testid="stub-BackupInspectPanel" /> }))
vi.mock("@/components/admin/backup-list", () => ({ BackupList: () => <div data-testid="stub-BackupList" /> }))
vi.mock("@/components/admin/backup-metrics-row", () => ({ BackupMetricsRow: () => <div data-testid="stub-BackupMetricsRow" /> }))
vi.mock("@/components/admin/backup-restore-dialog", () => ({ BackupRestoreDialog: () => <div data-testid="stub-BackupRestoreDialog" /> }))
vi.mock("@/components/admin/backup-retention-card", () => ({ BackupRetentionCard: () => <div data-testid="stub-BackupRetentionCard" /> }))
vi.mock("@/components/admin/backup-self-test-card", () => ({ BackupSelfTestCard: () => <div data-testid="stub-BackupSelfTestCard" /> }))
vi.mock("@/components/admin/backup-status-banner", () => ({ BackupStatusBanner: () => <div data-testid="stub-BackupStatusBanner" /> }))
vi.mock("@/stores/backup-store", () => ({ useBackupStore: (sel: (s: unknown) => unknown) => sel({ openCreate: () => {} }) }))

import { BackupsTab } from "@/app/(dashboard)/admin/tabs/backups-tab"

// =============================================================================
// The screen has to answer "what is a backup" before it offers to make one.
//
// It used to go straight from a Create button to a list of files. Nothing said
// what a bundle covers, whether credentials are in it, or whether it can be
// read on another machine — which are the questions you ask before you rely on
// one. Those answers are load-bearing copy, so they get a test: a refactor
// that quietly drops the band leaves the screen unable to answer them again.
// =============================================================================

describe("backups tab structure", () => {
  it("answers what a backup is, before the list", () => {
    render(<BackupsTab workspaceId="ws1" />)
    const bands = screen.getAllByRole("heading").map((h) => h.textContent)
    expect(bands).toEqual(["What a backup is", "Bundles", "Keeping it honest"])
  })

  it("says credential plaintext is not in the bundle", () => {
    render(<BackupsTab workspaceId="ws1" />)
    expect(screen.getByText(/Credential plaintext/)).toBeInTheDocument()
    expect(screen.getByText(/cannot leak/)).toBeInTheDocument()
  })

  it("says a bundle is encrypted and checkable without restoring", () => {
    render(<BackupsTab workspaceId="ws1" />)
    expect(screen.getByText(/age-v1/)).toBeInTheDocument()
    expect(screen.getByText(/crewship backup verify/)).toBeInTheDocument()
  })

  it("keeps the counters below the list, not above it", () => {
    const { container } = render(<BackupsTab workspaceId="ws1" />)
    const order = [...container.querySelectorAll("[data-testid^='stub-']")]
      .map((e) => e.getAttribute("data-testid"))
    expect(order.indexOf("stub-BackupList")).toBeLessThan(order.indexOf("stub-BackupMetricsRow"))
  })

  it("uses the app's sub-bar CTA, not a solid primary", () => {
    render(<BackupsTab workspaceId="ws1" />)
    // `soft` is the documented convention in components/ui/button.tsx; a solid
    // button here read as heavier than the page's own header.
    expect(screen.getByTestId("backup-create-btn").className).toMatch(/bg-primary\/15/)
  })
})
