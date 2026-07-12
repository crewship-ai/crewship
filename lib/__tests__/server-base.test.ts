import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  applyAuthInit,
  getAuthMode,
  getBearerToken,
  getServerBase,
  resolveWsBase,
  serverFetch,
  withServerBase,
} from "../server-base"

// vitest.setup.ts replaces localStorage with a vi.fn mock (getItem → null),
// so the storage fallback is exercised by stubbing getItem's return value.
function stubStoredBase(value: string) {
  vi.mocked(window.localStorage.getItem).mockImplementation((key: string) =>
    key === "crewship.serverBase" ? value : null,
  )
}

function clearShellGlobals() {
  delete (window as Window).__CREWSHIP_SERVER_BASE__
  delete (window as Window).__CREWSHIP_TOKEN__
  vi.mocked(window.localStorage.getItem).mockImplementation(() => null)
}

beforeEach(clearShellGlobals)
afterEach(() => {
  clearShellGlobals()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe("getServerBase", () => {
  it("defaults to same-origin (empty string)", () => {
    expect(getServerBase()).toBe("")
  })

  it("reads the shell-injected global and normalizes to origin", () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com/some/path/"
    expect(getServerBase()).toBe("https://crewship.example.com")
  })

  it("falls back to localStorage", () => {
    stubStoredBase("http://192.168.1.201:8082")
    expect(getServerBase()).toBe("http://192.168.1.201:8082")
  })

  it("injected global wins over localStorage", () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://a.example.com"
    stubStoredBase("https://b.example.com")
    expect(getServerBase()).toBe("https://a.example.com")
  })

  it("ignores garbage and non-http(s) schemes fail-safe", () => {
    stubStoredBase("not a url")
    expect(getServerBase()).toBe("")
    stubStoredBase("ftp://evil.example.com")
    expect(getServerBase()).toBe("")
  })
})

describe("auth mode + bearer token", () => {
  it("cookie mode by default", () => {
    expect(getAuthMode()).toBe("cookie")
    expect(getBearerToken()).toBeNull()
  })

  it("bearer mode when the shell injects a token string", () => {
    window.__CREWSHIP_TOKEN__ = "crewship_cli_abc"
    expect(getAuthMode()).toBe("bearer")
    expect(getBearerToken()).toBe("crewship_cli_abc")
  })

  it("supports a getter-backed token (keychain seam)", () => {
    window.__CREWSHIP_TOKEN__ = () => "crewship_cli_from_keychain"
    expect(getBearerToken()).toBe("crewship_cli_from_keychain")
  })

  it("a throwing getter degrades to cookie mode", () => {
    window.__CREWSHIP_TOKEN__ = () => {
      throw new Error("keychain locked")
    }
    expect(getBearerToken()).toBeNull()
    expect(getAuthMode()).toBe("cookie")
  })
})

describe("withServerBase", () => {
  it("passes relative paths through untouched same-origin", () => {
    expect(withServerBase("/api/v1/agents")).toBe("/api/v1/agents")
  })

  it("prefixes the configured base", () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    expect(withServerBase("/api/v1/agents")).toBe("https://crewship.example.com/api/v1/agents")
  })

  it("leaves absolute and protocol-relative URLs alone", () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    expect(withServerBase("https://other.example.com/x")).toBe("https://other.example.com/x")
    expect(withServerBase("//cdn.example.com/x")).toBe("//cdn.example.com/x")
  })
})

describe("applyAuthInit", () => {
  it("is a no-op same-origin in cookie mode (regression bar)", () => {
    const init = { method: "POST" }
    expect(applyAuthInit(init)).toBe(init)
    expect(applyAuthInit(undefined)).toBeUndefined()
  })

  it("includes credentials for a remote base in cookie mode", () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    expect(applyAuthInit({})).toMatchObject({ credentials: "include" })
  })

  it("bearer mode sets Authorization and omits credentials", () => {
    window.__CREWSHIP_TOKEN__ = "crewship_cli_abc"
    const out = applyAuthInit({ method: "POST" })
    expect(out).toMatchObject({ credentials: "omit", method: "POST" })
    expect(new Headers(out?.headers).get("Authorization")).toBe("Bearer crewship_cli_abc")
  })

  it("bearer mode does not clobber a caller-set Authorization header", () => {
    window.__CREWSHIP_TOKEN__ = "crewship_cli_abc"
    const out = applyAuthInit({ headers: { Authorization: "Bearer explicit" } })
    expect(new Headers(out?.headers).get("Authorization")).toBe("Bearer explicit")
  })
})

describe("serverFetch", () => {
  it("same-origin: identical to bare fetch (url + init untouched)", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}"))
    vi.stubGlobal("fetch", fetchMock)
    const init = { method: "POST" }
    await serverFetch("/api/v1/auth/signup", init)
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/signup", init)
  })

  it("remote bearer: base-prefixed URL + Authorization + omit", async () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    window.__CREWSHIP_TOKEN__ = "crewship_cli_abc"
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}"))
    vi.stubGlobal("fetch", fetchMock)
    await serverFetch("/api/v1/system/runtime")
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("https://crewship.example.com/api/v1/system/runtime")
    expect(init).toMatchObject({ credentials: "omit" })
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer crewship_cli_abc")
  })
})

describe("resolveWsBase", () => {
  it("derives from window.location same-origin (historical behavior)", () => {
    // happy-dom provides a window.location for the same-origin default
    expect(resolveWsBase()).toBe(`ws://${window.location.host}`)
  })

  it("maps a configured https base to wss", () => {
    window.__CREWSHIP_SERVER_BASE__ = "https://crewship.example.com"
    expect(resolveWsBase()).toBe("wss://crewship.example.com")
  })

  it("maps a configured http base to ws and keeps the port", () => {
    window.__CREWSHIP_SERVER_BASE__ = "http://192.168.1.201:8082"
    expect(resolveWsBase()).toBe("ws://192.168.1.201:8082")
  })
})
