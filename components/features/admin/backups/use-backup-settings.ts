"use client"

import { destinationsFixture, incidentsFixture, recipientsFixture, settingsFixture, vaultKeysFixture } from "./__fixtures__/backups"
import type {
  BackupIncident, BackupRecipient, BackupSettings, DestinationCreated, DestinationTest, NewOffsiteDestination, OffsiteDestination, VaultKeysResponse,
} from "./backups-model"
import { INSTANCE_BACKUPS, listOf, send, useResource, type SendResult } from "./use-backups-data"

/** GET /admin/instance/backups/settings — limits, destinations, alerts. */
export function useBackupSettings() {
  return useResource<BackupSettings>(`${INSTANCE_BACKUPS}/settings`, settingsFixture)
}

export function saveBackupSettings(patch: Partial<BackupSettings>): Promise<SendResult<BackupSettings>> {
  return send(`${INSTANCE_BACKUPS}/settings`, "PUT", patch)
}

/**
 * PUT /admin/instance/backups/settings/recovery-kit {enabled} — whether new
 * instance bundles carry the vault keys, audited as its own action. (PUT
 * …/settings accepts recovery_kit_enabled too; the console uses this route so
 * the kit's audit entry says what changed.)
 */
export function setRecoveryKit(enabled: boolean): Promise<SendResult<{ enabled: boolean }>> {
  return send(`${INSTANCE_BACKUPS}/settings/recovery-kit`, "PUT", { enabled })
}

/** GET /admin/instance/backups/recipients — AGE public keys. */
export function useRecipients() {
  return useResource<BackupRecipient[]>(`${INSTANCE_BACKUPS}/recipients`, () => recipientsFixture(new Date()), listOf)
}

export function addRecipient(body: { name: string; public_key: string; holder: string }): Promise<SendResult<BackupRecipient>> {
  return send(`${INSTANCE_BACKUPS}/recipients`, "POST", body)
}

export function removeRecipient(id: string): Promise<SendResult<unknown>> {
  return send(`${INSTANCE_BACKUPS}/recipients/${encodeURIComponent(id)}`, "DELETE")
}

/** GET /admin/instance/backups/vault-keys */
export function useVaultKeys() {
  return useResource<VaultKeysResponse>(`${INSTANCE_BACKUPS}/vault-keys`, vaultKeysFixture)
}

/** GET /admin/instance/backups/incidents */
export function useIncidents() {
  return useResource<BackupIncident[]>(`${INSTANCE_BACKUPS}/incidents`, () => incidentsFixture(new Date()), listOf)
}

/** GET /admin/instance/backups/destinations — off-site stores, their verified copies. */
export function useDestinations() {
  return useResource<OffsiteDestination[]>(`${INSTANCE_BACKUPS}/destinations`, destinationsFixture, listOf)
}

/**
 * POST /admin/instance/backups/destinations — the server tests the connection
 * first and stores nothing when that fails (422, the error says which step).
 */
export function addDestination(body: NewOffsiteDestination): Promise<SendResult<DestinationCreated>> {
  return send(`${INSTANCE_BACKUPS}/destinations`, "POST", body)
}

export function testDestination(id: string): Promise<SendResult<DestinationTest>> {
  return send(`${INSTANCE_BACKUPS}/destinations/${encodeURIComponent(id)}/test`, "POST", {})
}

/** DELETE …/destinations/{id} — refused (409) while a plan copies there. */
export function removeDestination(id: string): Promise<SendResult<unknown>> {
  return send(`${INSTANCE_BACKUPS}/destinations/${encodeURIComponent(id)}`, "DELETE")
}

/** The recovery sheet (text/markdown), downloaded by the browser. */
export const RECOVERY_SHEET_HREF = `${INSTANCE_BACKUPS}/recovery-sheet`
