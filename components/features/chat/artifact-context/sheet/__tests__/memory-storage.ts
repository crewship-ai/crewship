import { afterEach, beforeEach, vi } from "vitest"

/**
 * vitest.setup.ts replaces localStorage with shared inert vi.fn stubs. Other
 * files set return values on them (some once-only), and a batch run may carry
 * those over. Each test here starts from a reset stub backed by its own map,
 * and gives the stubs back reset, whatever order the files run in.
 */
export function installMemoryStorage(): Map<string, string> {
  const store = new Map<string, string>()
  const stubs = () => [localStorage.getItem, localStorage.setItem, localStorage.removeItem, localStorage.clear].map(fn => vi.mocked(fn))
  beforeEach(() => {
    store.clear()
    for (const stub of stubs()) stub.mockReset()
    vi.mocked(localStorage.getItem).mockImplementation((k: string) => store.get(k) ?? null)
    vi.mocked(localStorage.setItem).mockImplementation((k: string, v: string) => { store.set(k, String(v)) })
    vi.mocked(localStorage.removeItem).mockImplementation((k: string) => { store.delete(k) })
    vi.mocked(localStorage.clear).mockImplementation(() => { store.clear() })
  })
  afterEach(() => { for (const stub of stubs()) stub.mockReset(); store.clear() })
  return store
}
