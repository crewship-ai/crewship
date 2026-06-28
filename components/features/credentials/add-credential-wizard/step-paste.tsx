"use client"

import * as React from "react"
import { Eye, EyeOff, CheckCircle2, XCircle, FileUp } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import type { WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

// Types that have no /test endpoint — raw vault secrets the agent
// consumes directly. Skip the debounced auto-test for these so we
// don't fire pointless 404s every 800ms during typing.
const NON_TESTABLE_TYPES = new Set(["USERPASS", "SSH_KEY", "CERTIFICATE", "GENERIC_SECRET", "SECRET"])

export function StepPaste({ state, setState }: Props) {
  const [bulkMode, setBulkMode] = React.useState(false)
  const debounceRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)
  const abortRef = React.useRef<AbortController | null>(null)

  // Auto-test debounced 800ms after paste. Skipped for vault types
  // (no /test endpoint) and for the legacy NONE provider (no upstream
  // to test against).
  //
  // Race protection: an in-flight /test fetch must be aborted if the
  // user switches tile or auth method (changing state.type / .provider)
  // OR clears the value, otherwise a stale response can land in
  // setState() after the user has already moved past — e.g. flipping
  // from API_KEY → USERPASS while a test was pending would render a
  // ghost "Invalid" verdict on a non-testable type. AbortController +
  // an effect dep on type/provider closes both windows.
  React.useEffect(() => {
    if (NON_TESTABLE_TYPES.has(state.type) || state.provider === "NONE" || !state.value.trim()) {
      return
    }
    if (debounceRef.current) clearTimeout(debounceRef.current)
    // Cancel any in-flight test from a prior keystroke / tile change.
    if (abortRef.current) abortRef.current.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    debounceRef.current = setTimeout(async () => {
      setState({ testing: true, testResult: null })
      try {
        const res = await apiFetch("/api/v1/credentials/test", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            provider: state.provider,
            type: state.type,
            value: state.value.trim(),
          }),
          signal: ctrl.signal,
        })
        if (!res.ok) {
          setState({ testing: false, testResult: { valid: false, error: "Test request failed" } })
          return
        }
        const data = await res.json()
        setState({ testing: false, testResult: { valid: data.valid, error: data.error } })
      } catch (err) {
        // AbortError is the expected outcome when type/provider/value
        // changes mid-flight — don't surface it as a network error.
        if ((err as Error)?.name === "AbortError") return
        setState({ testing: false, testResult: { valid: false, error: "Network error" } })
      }
    }, 800)
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
      ctrl.abort()
    }
  }, [state.value, state.type, state.provider, setState])

  if (bulkMode) {
    return <BulkImport setBulkMode={setBulkMode} />
  }

  // Type-specific input UIs — see each Fields component for the
  // shape. The shared test-status bar + bulk-import shortcut sit
  // below, so each component renders only its own input(s).
  const inputs =
    state.type === "USERPASS" ? (
      <UserPassFields state={state} setState={setState} />
    ) : state.type === "SSH_KEY" ? (
      <PEMFields state={state} setState={setState} kind="ssh" />
    ) : state.type === "CERTIFICATE" ? (
      <PEMFields state={state} setState={setState} kind="cert" />
    ) : (
      <SingleValueField state={state} setState={setState} />
    )

  return (
    <div className="space-y-3">
      {inputs}

      {/* Test status + bulk-import shortcut. The status bar is hidden
          for non-testable types so we don't render an empty 24px gap
          for credentials that never report a verdict. */}
      <div className="flex items-center justify-between min-h-[24px]">
        <div className="text-xs">
          {!NON_TESTABLE_TYPES.has(state.type) && state.testing && (
            <span className="inline-flex items-center gap-1.5 text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" />
              Testing key...
            </span>
          )}
          {!NON_TESTABLE_TYPES.has(state.type) && !state.testing && state.testResult?.valid && (
            <span className="inline-flex items-center gap-1.5 text-emerald-400">
              <CheckCircle2 className="h-3.5 w-3.5" />
              Valid
            </span>
          )}
          {!NON_TESTABLE_TYPES.has(state.type) && !state.testing && state.testResult && !state.testResult.valid && (
            <span className={cn("inline-flex items-center gap-1.5 text-red-400")}>
              <XCircle className="h-3.5 w-3.5" />
              {state.testResult.error || "Invalid"}
            </span>
          )}
        </div>
        <button
          type="button"
          onClick={() => setBulkMode(true)}
          className="text-[11px] text-muted-foreground hover:text-foreground inline-flex items-center gap-1"
        >
          <FileUp className="h-3 w-3" />
          Import from .env
        </button>
      </div>
    </div>
  )
}

// SingleValueField is the original step-paste input — one masked text
// field with an eye toggle, used for API keys, tokens, OAuth blobs,
// and opaque secrets. Placeholder hints come from the provider since
// the user's most common task is pasting *this specific provider's*
// key shape.
function SingleValueField({ state, setState }: Props) {
  const [showValue, setShowValue] = React.useState(false)

  const placeholder =
    state.authMethod === "setup-token"
      ? "Paste output of `claude setup-token`..."
      : state.provider === "ANTHROPIC"
        ? "sk-ant-..."
        : state.provider === "OPENAI"
          ? "sk-proj-..."
          : state.provider === "GITHUB"
            ? "ghp_..."
            : "Paste value..."

  return (
    <>
      {state.authMethod === "setup-token" && (
        <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs space-y-1.5">
          <p className="font-medium">How to get a setup token:</p>
          <ol className="list-decimal list-inside space-y-0.5 text-foreground/80">
            <li>Open a terminal on your computer</li>
            <li>Run: <code className="rounded bg-black/40 px-1 font-mono">claude setup-token</code></li>
            <li>Copy the entire output and paste below</li>
          </ol>
        </div>
      )}

      <div className="space-y-1.5">
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
          Value
        </label>
        <div className="relative">
          <input
            autoFocus
            type={showValue ? "text" : "password"}
            value={state.value}
            onChange={(e) => setState({ value: e.target.value, testResult: null })}
            placeholder={placeholder}
            className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 pr-10 text-sm font-mono outline-none focus:border-blue-400"
          />
          <button
            type="button"
            onClick={() => setShowValue((s) => !s)}
            className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          >
            {showValue ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        </div>
      </div>
    </>
  )
}

// UserPassFields renders the Bitwarden-style two-field input pair for
// USERPASS credentials. The username is cleartext (it's an identifier),
// the password is masked by default with an eye-toggle. The injected
// env-var pair is <NAME>_USERNAME + <NAME>_PASSWORD; the hint below
// the inputs spells that out so the user knows what to expect inside
// the agent container.
function UserPassFields({ state, setState }: Props) {
  const [showPassword, setShowPassword] = React.useState(false)

  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
          Username
        </label>
        <input
          autoFocus
          type="text"
          value={state.username}
          onChange={(e) => setState({ username: e.target.value })}
          placeholder="user@gmail.com"
          autoComplete="off"
          className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm font-mono outline-none focus:border-blue-400"
        />
      </div>

      <div className="space-y-1.5">
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
          Password
        </label>
        <div className="relative">
          <input
            type={showPassword ? "text" : "password"}
            value={state.value}
            onChange={(e) => setState({ value: e.target.value })}
            placeholder="••••••••"
            autoComplete="new-password"
            className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 pr-10 text-sm font-mono outline-none focus:border-blue-400"
          />
          <button
            type="button"
            onClick={() => setShowPassword((s) => !s)}
            className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          >
            {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        </div>
      </div>

      <p className="text-[11px] text-muted-foreground leading-relaxed">
        Injected as <code className="rounded bg-black/40 px-1 font-mono">{"<NAME>"}_USERNAME</code>{" "}
        and <code className="rounded bg-black/40 px-1 font-mono">{"<NAME>"}_PASSWORD</code> env vars,
        where <code className="rounded bg-black/40 px-1 font-mono">{"<NAME>"}</code> is the binding
        name you set on the next step.
      </p>
    </div>
  )
}

// PEMFields renders a multi-line monospaced textarea for PEM-encoded
// keys / certs. `kind` switches the inline help and placeholder so the
// SSH and CERTIFICATE variants explain their own format without two
// near-duplicate components.
function PEMFields({
  state,
  setState,
  kind,
}: Props & { kind: "ssh" | "cert" }) {
  const isSSH = kind === "ssh"
  const placeholder = isSSH
    ? "-----BEGIN OPENSSH PRIVATE KEY-----\n…\n-----END OPENSSH PRIVATE KEY-----"
    : "-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----"

  // Soft client-side hint. Backend validateCredentialPayload does the
  // canonical PEM check (looksLikePEM in internal/api/credentials_types.go);
  // this just nudges users who paste obviously wrong content before they
  // hit Submit. The most common foot-gun for SSH_KEY is pasting an
  // OpenSSH public key (`ssh-rsa AAAA…` or `-----BEGIN PUBLIC KEY-----`)
  // into a field that needs the *private* half — the basic structural
  // check ("starts with BEGIN, contains END") would still pass that, so
  // we also verify the first-line label ends with the right marker.
  //
  // firstLine is .trim()'d *before* the regex replace so a CRLF-pasted
  // key (Windows openssl, Notepad) — where split('\n')[0] keeps a
  // trailing '\r' that prevents `-----$` from matching — still resolves
  // to the right marker. Mirrors the early TrimSpace in looksLikePEM.
  //
  // The warning is computed against a debounced snapshot of the
  // value, not the live keystroke, so a user typing a fresh BEGIN
  // line doesn't see the amber warning flash on every character.
  // Also suppressed entirely while the textarea has focus — show
  // only once they tab away or after they stop typing for 350ms.
  const [debouncedValue, setDebouncedValue] = React.useState(state.value)
  const [focused, setFocused] = React.useState(false)
  React.useEffect(() => {
    const t = setTimeout(() => setDebouncedValue(state.value), 350)
    return () => clearTimeout(t)
  }, [state.value])

  const trimmed = debouncedValue.trim()
  const expectedMarker = isSSH ? "PRIVATE KEY" : "CERTIFICATE"
  const firstLine = (trimmed.split("\n", 1)[0] ?? "").trim()
  const looksWrongShape =
    !focused &&
    trimmed.length > 0 &&
    !(
      trimmed.startsWith("-----BEGIN ") &&
      trimmed.includes("-----END ") &&
      firstLine.replace(/^-----BEGIN /, "").replace(/-----$/, "").trim().endsWith(expectedMarker)
    )

  // onPaste normalises CRLF → LF and strips leading whitespace some
  // terminals / Confluence-style copies inject before each line. The
  // backend tolerates CRLF (looksLikePEM TrimSpaces firstLine) but
  // the user-facing warning is easier to satisfy if the value is
  // clean, and the sidecar PEM parser is stricter than our gate.
  const handlePaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const raw = e.clipboardData.getData("text")
    if (!raw) return
    const normalised = raw
      .replace(/\r\n?/g, "\n")
      .split("\n")
      .map((line) => line.replace(/^[ \t]+(?=-----)/, "")) // strip indent before armour lines
      .join("\n")
    if (normalised === raw) return // nothing to fix, let default paste happen
    e.preventDefault()
    const target = e.currentTarget
    const start = target.selectionStart ?? state.value.length
    const end = target.selectionEnd ?? state.value.length
    setState({ value: state.value.slice(0, start) + normalised + state.value.slice(end) })
  }

  return (
    <div className="space-y-3">
      <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs space-y-1.5">
        <p className="font-medium">
          {isSSH ? "Paste the SSH private key (PEM)" : "Paste the certificate (PEM)"}
        </p>
        <p className="text-foreground/80 leading-relaxed">
          {isSSH ? (
            <>
              The agent mounts it at{" "}
              <code className="rounded bg-black/40 px-1 font-mono">~/.ssh/keys/{"<NAME>"}</code>{" "}
              with mode 0600. Use the{" "}
              <code className="rounded bg-black/40 px-1 font-mono">{"<NAME>"}_PATH</code>{" "}
              env var to locate it (e.g.{" "}
              <code className="rounded bg-black/40 px-1 font-mono">ssh -i $GITHUB_PATH …</code>).
            </>
          ) : (
            <>
              The agent mounts it at{" "}
              <code className="rounded bg-black/40 px-1 font-mono">
                /secrets/{"<agent>"}/certs/{"<NAME>"}.pem
              </code>{" "}
              with mode 0400. Locate via the auto-injected{" "}
              <code className="rounded bg-black/40 px-1 font-mono">{"<NAME>"}_PATH</code> env var.
            </>
          )}
        </p>
      </div>

      <div className="space-y-1.5">
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
          {isSSH ? "Private key (PEM)" : "Certificate (PEM)"}
        </label>
        <textarea
          autoFocus
          rows={10}
          value={state.value}
          onChange={(e) => setState({ value: e.target.value })}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          onPaste={handlePaste}
          placeholder={placeholder}
          spellCheck={false}
          autoComplete="off"
          className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-[11px] font-mono leading-snug outline-none focus:border-blue-400 resize-y"
        />
        {looksWrongShape && (
          <p className="text-[11px] text-amber-400 leading-relaxed">
            {isSSH ? (
              <>
                That doesn&rsquo;t look like a PEM-encoded{" "}
                <strong>private</strong> key — expected{" "}
                <code className="rounded bg-black/40 px-1 font-mono">-----BEGIN ... PRIVATE KEY-----</code>.{" "}
                If you pasted an{" "}
                <code className="rounded bg-black/40 px-1 font-mono">ssh-rsa</code> /{" "}
                <code className="rounded bg-black/40 px-1 font-mono">ssh-ed25519</code>{" "}
                line or a <code className="rounded bg-black/40 px-1 font-mono">PUBLIC KEY</code>{" "}
                block, that&rsquo;s the wrong half — Crewship needs the private key here.
              </>
            ) : (
              <>
                That doesn&rsquo;t look PEM-shaped — expected{" "}
                <code className="rounded bg-black/40 px-1 font-mono">-----BEGIN CERTIFICATE-----</code>{" "}
                … <code className="rounded bg-black/40 px-1 font-mono">-----END CERTIFICATE-----</code>.
              </>
            )}
          </p>
        )}
      </div>
    </div>
  )
}

function BulkImport({ setBulkMode }: { setBulkMode: (b: boolean) => void }) {
  const [text, setText] = React.useState("")
  const parsed = React.useMemo(() => {
    return text
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith("#"))
      .map((l) => {
        const eq = l.indexOf("=")
        if (eq === -1) return null
        const key = l.slice(0, eq).trim()
        let val = l.slice(eq + 1).trim()
        if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
          val = val.slice(1, -1)
        }
        return { key, val }
      })
      .filter((x): x is { key: string; val: string } => x !== null && x.key.length > 0 && x.val.length > 0)
  }, [text])

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium">Bulk import from .env</span>
        <Button variant="ghost" size="sm" onClick={() => setBulkMode(false)}>Back</Button>
      </div>
      <textarea
        rows={8}
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder="ANTHROPIC_API_KEY=sk-ant-...&#10;GH_TOKEN=ghp_..."
        className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-xs font-mono outline-none focus:border-blue-400"
      />
      <div className="rounded-md border border-amber-500/25 bg-amber-500/[0.05] px-3 py-2.5 text-xs">
        <strong>{parsed.length}</strong> credential{parsed.length === 1 ? "" : "s"} detected.
        Bulk-create flow lands in EPIC 2.5 — for now this is preview-only.
        Use the wizard for one credential at a time.
      </div>
    </div>
  )
}
