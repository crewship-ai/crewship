import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"
import { ConnectOAuthDialog } from "../connect-oauth-dialog"

// Credentials → Connect via OAuth, migrated onto the CreateSurface shell.
//
// The surface used to be a bare `sm:max-w-md` DialogContent carrying the
// shared dialog DEFAULTS — 448px, `p-6`, an 18px DialogTitle — which is why
// it read as a different design system rather than a different width. These
// tests pin two things at once: the shell it now mounts (size `sm` = 480px,
// no `p-6`, the shell's own header/close), and the request its primary action
// has always issued, which the migration must not touch.
//
// The body is OAuthForm, lifted across intact. It lives in
// components/features/mcp/components/oauth-form.tsx and is shared with the
// MCP server config's credential picker, so it is NOT edited here — which is
// why the Authorize button is still inside the scrollport rather than in a
// CreateSurfaceFooter.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: (...args: unknown[]) => toastError(...args),
    info: vi.fn(),
  },
}))

const PROVIDERS = {
  google: {
    auth_url: "https://accounts.google.com/o/oauth2/v2/auth",
    token_url: "https://oauth2.googleapis.com/token",
    default_scopes: "openid email",
  },
}

function ok(body: unknown) {
  return { ok: true, status: 200, json: async () => body }
}

beforeEach(() => {
  apiFetch.mockReset()
  toastError.mockReset()
  // Default: providers land, credential creation succeeds, loopback starts.
  apiFetch.mockImplementation(async (url: string) => {
    if (url.startsWith("/api/v1/oauth/providers")) return ok(PROVIDERS)
    if (url.startsWith("/api/v1/credentials")) return ok({ id: "cred-1", status: "PENDING" })
    if (url.startsWith("/api/v1/oauth/loopback")) {
      return ok({ auth_url: "https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fcb" })
    }
    return ok({})
  })
  // Deterministic: a blocked popup ends the flow right after the requests
  // this test cares about, with no timers left running.
  vi.stubGlobal("open", vi.fn(() => null))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function renderDialog(props: Partial<React.ComponentProps<typeof ConnectOAuthDialog>> = {}) {
  const onOpenChange = vi.fn()
  const onSuccess = vi.fn()
  render(
    <ConnectOAuthDialog
      workspaceId="ws1"
      open
      onOpenChange={onOpenChange}
      onSuccess={onSuccess}
      {...props}
    />,
  )
  return { onOpenChange, onSuccess }
}

const shell = () => document.querySelector('[data-slot="dialog-content"]') as HTMLElement | null

describe("ConnectOAuthDialog — the shell", () => {
  it("mounts CreateSurface at size sm rather than the default DialogContent", async () => {
    renderDialog()
    await waitFor(() => expect(shell()).not.toBeNull())

    const content = shell()!
    // Size sm is 480px, fixed for the surface's whole life.
    expect(content.className).toContain("sm:max-w-[480px]")
    // The shell owns padding; the default dialog's p-6 must be gone.
    expect(content.className).not.toContain("p-6")
    // The old 448px width, gone.
    expect(content.className).not.toContain("sm:max-w-md")
  })

  it("keeps the title and description the surface has today", async () => {
    renderDialog()
    await waitFor(() => expect(shell()).not.toBeNull())

    expect(screen.getByText("Connect via OAuth")).toBeInTheDocument()
    expect(
      screen.getByText(/Authorize a provider in a popup and store the resulting tokens/i),
    ).toBeInTheDocument()
  })

  it("closes from the shell's own close control", async () => {
    const { onOpenChange } = renderDialog()
    await waitFor(() => expect(shell()).not.toBeNull())

    fireEvent.click(screen.getByRole("button", { name: "Close" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("guards Esc once something has been typed", async () => {
    const { onOpenChange } = renderDialog()
    await waitFor(() => expect(shell()).not.toBeNull())

    fireEvent.click(screen.getByRole("button", { name: "Google" }))
    fireEvent.change(screen.getByLabelText("Client ID"), { target: { value: "abc" } })

    fireEvent.keyDown(shell()!, { key: "Escape" })

    await waitFor(() => expect(screen.getByRole("alertdialog")).toBeInTheDocument())
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
    expect(screen.getByRole("heading", { name: /Discard this connection\?/ })).toBeInTheDocument()
  })
})

describe("ConnectOAuthDialog — the request it has always issued", () => {
  it("creates an OAUTH2 credential with the typed client details", async () => {
    const { onSuccess } = renderDialog()
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith("/api/v1/oauth/providers?workspace_id=ws1"),
    )

    fireEvent.click(screen.getByRole("button", { name: "Google" }))
    fireEvent.change(screen.getByLabelText("Client ID"), { target: { value: "cid" } })
    fireEvent.change(screen.getByLabelText("Client Secret"), { target: { value: "csecret" } })
    fireEvent.click(screen.getByRole("button", { name: /authorize/i }))

    await waitFor(() => {
      const call = apiFetch.mock.calls.find(
        (c) => typeof c[0] === "string" && c[0].startsWith("/api/v1/credentials?"),
      )
      expect(call).toBeDefined()
    })

    const call = apiFetch.mock.calls.find(
      (c) => typeof c[0] === "string" && c[0].startsWith("/api/v1/credentials?"),
    )!
    expect(call[0]).toBe("/api/v1/credentials?workspace_id=ws1")
    expect(call[1].method).toBe("POST")
    const body = JSON.parse(call[1].body as string)
    expect(body).toMatchObject({
      type: "OAUTH2",
      value: "",
      scope: "WORKSPACE",
      oauth_client_id: "cid",
      oauth_client_secret: "csecret",
      oauth_auth_url: "https://accounts.google.com/o/oauth2/v2/auth",
      oauth_token_url: "https://oauth2.googleapis.com/token",
      oauth_scopes: "openid email",
    })
    expect(body.name).toMatch(/^google-oauth-/)

    // The intermediate PENDING row is announced so the page refreshes.
    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
  })

  it("still starts the loopback flow and reports a blocked popup", async () => {
    renderDialog()
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())

    fireEvent.click(screen.getByRole("button", { name: "Google" }))
    fireEvent.change(screen.getByLabelText("Client ID"), { target: { value: "cid" } })
    fireEvent.change(screen.getByLabelText("Client Secret"), { target: { value: "csecret" } })
    fireEvent.click(screen.getByRole("button", { name: /authorize/i }))

    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/oauth/loopback?workspace_id=ws1",
        expect.objectContaining({ method: "POST" }),
      ),
    )
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/Popup blocked/)))
  })

  it("cancels through the form's own Cancel", async () => {
    const { onOpenChange } = renderDialog()
    await waitFor(() => expect(shell()).not.toBeNull())

    fireEvent.click(screen.getByRole("button", { name: "Google" }))
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})
