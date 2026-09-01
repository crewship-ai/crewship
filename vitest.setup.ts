import { expect, afterEach, vi } from 'vitest'
import { cleanup } from '@testing-library/react'
import * as matchers from '@testing-library/jest-dom/matchers'

// Extend Vitest's expect with jest-dom matchers
expect.extend(matchers)

// Mock localStorage
const localStorageMock = {
  getItem: vi.fn(() => null),
  setItem: vi.fn(),
  removeItem: vi.fn(),
  clear: vi.fn(),
}
Object.defineProperty(global, 'localStorage', { value: localStorageMock })

// Mock Next.js navigation
vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    prefetch: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    refresh: vi.fn(),
  }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => '/',
}))

// No test may open a socket.
//
// The release-1.0 audit flagged that the suite "attempted connections to
// localhost:3000 during test setup" and asked for it to be classified as an
// intentional fixture dependency or an isolation defect. It was a defect, and
// it stayed invisible for months because it is silent: the connection is
// refused, nothing awaits it, the suite still reports 4308/4308 passing, and
// the only trace is an ECONNREFUSED stack in the noise.
//
// Silent is the problem, not the socket. On a machine where something IS
// listening on :3000, an unmocked call starts succeeding and a test begins
// asserting against whatever answered.
//
// So the default `fetch` records the attempt and rejects by name. A test that
// wants network behaviour stubs `fetch` itself, which overrides this; a test
// that did not mean to make one gets a failure that names the URL instead of
// an ECONNREFUSED nobody attributes.
const unmockedFetches: string[] = []
vi.stubGlobal(
  'fetch',
  vi.fn((input: RequestInfo | URL) => {
    const url =
      typeof input === 'string'
        ? input
        : input instanceof URL
          ? input.href
          : (input as Request).url
    unmockedFetches.push(url)
    return Promise.reject(
      new Error(
        `unmocked network call to ${url} — mock fetch in this test, or stub the ` +
          `module that calls it. See vitest.setup.ts.`,
      ),
    )
  }),
)

// Cleanup after each test
afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  const leaked = unmockedFetches.splice(0)
  if (leaked.length > 0) {
    const unique = [...new Set(leaked)]
    throw new Error(
      `${leaked.length} unmocked network call(s) escaped this test:\n` +
        unique.map((u) => `  - ${u}`).join('\n') +
        `\nA unit test must not depend on a listener. Mock fetch, or stub the module.`,
    )
  }
})

// happy-dom rejects `Animation.finished` on cancel, and creates it eagerly.
//
// Per WAAPI, `cancel()` rejects the animation's finished promise with an
// AbortError — happy-dom 20.12.0 does exactly that. A browser gets away with
// it because the spec builds `finished` lazily, on first access of the getter:
// an animation nobody awaited has no promise to reject, so nothing is ever
// unhandled. happy-dom instead assigns `finished` as an instance field in the
// constructor (and again in `play()`), so every animation carries a live,
// unwatched promise.
//
// motion drives its exit transitions through WAAPI and cancels them on
// unmount — which every `cleanup()` triggers. Its own `cancel()` wraps the
// call in try/catch, but the rejection lands asynchronously on `finished`, so
// the catch never sees it. The result was 1493 unhandled rejections across 11
// test files with 7012 tests passing and zero assertions failing.
//
// Attaching a no-op handler restores the browser's observable behaviour: the
// promise is marked handled, so an animation nobody awaited stays silent. A
// test that does await `finished` is unaffected — `.catch()` derives a new
// promise and leaves the original's rejection visible to every other consumer.
if (typeof globalThis.Animation === 'function') {
  const cancel = globalThis.Animation.prototype.cancel
  globalThis.Animation.prototype.cancel = function (this: Animation) {
    this.finished?.catch(() => {})
    return cancel.call(this)
  }
}
