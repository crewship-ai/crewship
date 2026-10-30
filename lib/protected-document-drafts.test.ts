// @vitest-environment node
import { beforeEach, expect, it, vi } from "vitest"
const m = vi.hoisted(() => ({ open: vi.fn(), purge: vi.fn(), cipher: vi.fn() }))
vi.mock("./document-queue", async () => ({ ...await vi.importActual("./document-queue"), openDocumentDrafts: m.open, purgeDocumentDrafts: m.purge }))
vi.mock("./local-draft-seal", async () => ({ ...await vi.importActual("./local-draft-seal"), createLocalDraftCipher: m.cipher }))
import { LocalDraftReadRevoked } from "./local-draft-seal"
import { openProtectedDocumentDrafts } from "./protected-document-drafts"
beforeEach(() => vi.clearAllMocks())
it.each(["admission", "recovery"])("holds exclusive custody through a %s read-revocation purge", async phase => {
  let release!: () => void
  const purging = new Promise<void>(resolve => { release = resolve })
  const raw = { exclusive: true, close: vi.fn(), load: vi.fn(async () => [{ id: "old", schema: 1, epoch: "native-v1", snapshot: '{"format":"crewship-local-draft-v1"}', attempted: true, update: "" }]), put: vi.fn(), remove: vi.fn() }
  const cipher = { close: vi.fn(), seal: vi.fn(), open: vi.fn().mockRejectedValue(new LocalDraftReadRevoked("removed")) }
  m.open.mockResolvedValue(raw); m.purge.mockReturnValue(purging)
  if (phase === "admission") m.cipher.mockRejectedValue(new LocalDraftReadRevoked("removed")); else m.cipher.mockResolvedValue(cipher)
  const pending = (async () => { const store = await openProtectedDocumentDrafts("alice", "w", "p", { requireExclusive: true }); return store.load() })()
  void pending.catch(() => {})
  try {
    await vi.waitFor(() => expect(m.purge).toHaveBeenCalledOnce())
    expect(m.open).toHaveBeenCalledOnce()
    expect(raw.close).not.toHaveBeenCalled()
  } finally { release(); await pending.catch(() => {}) }
  expect(raw.close).toHaveBeenCalledOnce()
})
