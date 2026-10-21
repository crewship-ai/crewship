"use client"

import { SquareTerminal } from "lucide-react"

import {
  CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { PairingBlock, pairCommand, usePairing } from "@/components/layout/cli-pairing"

// Kept here for existing imports; the shared pairing kit owns it.
export { pairCommand }

/**
 * Connect CLI — signs a terminal in as you with a one-time device code
 * (POST /api/v1/auth/pair/start → `crewship login --pair` → poll).
 *
 * Onboarding was the only place this flow had a screen, so after setup the
 * way to sign a second laptop in was to mint a CLI token by hand and paste
 * the secret into a config file. The code is single-use and expires in ten
 * minutes; the token the CLI receives shows up under Sessions & CLI tokens,
 * where it can be revoked like any other.
 *
 * Harbor 2 (v67 `u_sh_cli`, G15): a medium form with the terminal tile, the
 * shared pairing block (pending with its countdown, paired, expired, failed;
 * the same component the first-run setup uses), and a footer hint with Done.
 * There is nothing to cancel: Done closes in every state.
 */
export function ConnectCliDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const pairing = usePairing(open)
  const close = () => onOpenChange(false)

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md">
      <CreateSurfaceHeader
        icon={SquareTerminal}
        title="Connect the CLI"
        description="Run this on the machine where the crewship CLI is installed. The code works once and expires in 10 minutes."
        onClose={close}
      />
      <CreateSurfaceBody>
        <PairingBlock
          status={pairing.status}
          command={pairing.command}
          remainingSec={pairing.remainingSec}
          copied={pairing.copied}
          error={pairing.error}
          onCopy={pairing.copy}
          onNewCode={() => void pairing.newCode()}
          onRetry={() => void pairing.retry()}
        />
      </CreateSurfaceBody>
      <CreateSurfaceFooter hint="Connected tokens appear in Sessions & CLI tokens." primaryLabel="Done" onPrimary={close} />
    </CreateSurface>
  )
}
