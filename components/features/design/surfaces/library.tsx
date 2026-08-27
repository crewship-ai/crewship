"use client"

/**
 * Skills, Credentials and Integrations — the four remaining doors.
 *
 * Import skill and Connect via OAuth are the two surfaces that still use the
 * shared DialogContent's default `p-6` padding and its 18px title, so they are
 * the ones that look like a different design system rather than a different
 * width. Add secret is the only door in the product that already handles a
 * phone; its three steps survive unchanged. Add integration keeps its two-step
 * kind → service shape, which was the right call and is why it is `xl`.
 */

import * as React from "react"
import {
  AlertTriangle,
  Bell,
  Check,
  Clock,
  Eye,
  FileCode2,
  GitBranch,
  Globe,
  KeyRound,
  Link2,
  Lock,
  MessageSquare,
  Network,
  FileJson,
  Search,
  Server,
  Shield,
  ShieldCheck,
  Tag,
  Upload,
  Wrench,
  X,
} from "lucide-react"

import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import {
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceDisclosure,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceSecondaryAction,
  CreateSurfaceSteps,
  CreateSurfaceTile,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"

/* ══ Skills → Import ════════════════════════════════════════════════════ */

export function ImportSkillContent({ onClose }: { onClose: () => void }) {
  const [tab, setTab] = React.useState<"url" | "content" | "repo">("url")
  const [url, setUrl] = React.useState("")
  const [content, setContent] = React.useState("")
  const [repoUrl, setRepoUrl] = React.useState("")
  const [repoRef, setRepoRef] = React.useState("")
  const [repoVendor, setRepoVendor] = React.useState("")
  const [unsafeLicense, setUnsafeLicense] = React.useState(false)
  const [dryRun, setDryRun] = React.useState(true)

  const ready = tab === "url" ? url.trim() !== "" : tab === "content" ? content.trim() !== "" : repoUrl.trim() !== ""

  return (
    <>
      <CreateSurfaceHeader
        concept="skills"
        context="Skills"
        title="Import a skill"
        description="From a GitHub URL, a pasted SKILL.md, or a whole repository at once."
        onClose={onClose}
      />

      <CreateSurfaceBody className="space-y-3">
        <CreateSurfaceChoice
          ariaLabel="Import source"
          value={tab}
          onChange={setTab}
          options={[
            { value: "url", label: "URL" },
            { value: "content", label: "Paste" },
            { value: "repo", label: "Repository" },
          ]}
        />

        {tab === "url" && (
          <CreateSurfaceField
            label="Skill URL"
            htmlFor="skill-url"
            required
            hint="A link to a SKILL.md, or the directory that holds one."
          >
            <Input
              id="skill-url"
              autoFocus
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://github.com/org/repo/blob/main/skills/foo/SKILL.md"
              className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
            />
          </CreateSurfaceField>
        )}

        {tab === "content" && (
          <CreateSurfaceField label="SKILL.md" htmlFor="skill-content" required>
            <textarea
              id="skill-content"
              autoFocus
              value={content}
              onChange={(e) => setContent(e.target.value)}
              rows={10}
              placeholder={"---\nname: my-skill\ndescription: …\n---\n"}
              className="w-full resize-none rounded-lg border border-hairline bg-background p-2.5 font-mono text-[11px] text-foreground outline-none focus:border-primary"
            />
          </CreateSurfaceField>
        )}

        {tab === "repo" && (
          <>
            <CreateSurfaceField label="Repository" htmlFor="skill-repo" required>
              <Input
                id="skill-repo"
                autoFocus
                value={repoUrl}
                onChange={(e) => setRepoUrl(e.target.value)}
                placeholder="https://github.com/org/skills"
                className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>
            <CreateSurfaceGrid>
              <CreateSurfaceField label="Ref" htmlFor="skill-ref" hint="Branch, tag or SHA. Defaults to the default branch.">
                <Input
                  id="skill-ref"
                  value={repoRef}
                  onChange={(e) => setRepoRef(e.target.value)}
                  placeholder="main"
                  className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
                />
              </CreateSurfaceField>
              <CreateSurfaceField label="Vendor" htmlFor="skill-vendor" hint="Attribution recorded on every imported skill.">
                <Input
                  id="skill-vendor"
                  value={repoVendor}
                  onChange={(e) => setRepoVendor(e.target.value)}
                  placeholder="anthropic"
                  className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
                />
              </CreateSurfaceField>
            </CreateSurfaceGrid>
            <CreateSurfaceToggleRow
              icon={Eye}
              accent="teal"
              label="Dry run"
              hint="List what would be imported without writing anything. On by default for a repository."
              control={<Switch checked={dryRun} onCheckedChange={setDryRun} />}
            />
          </>
        )}

        <CreateSurfaceDisclosure
          icon={Shield}
          accent="amber"
          label="Licensing"
          summary={unsafeLicense ? "unrecognised licences allowed" : "recognised licences only"}
        >
          <CreateSurfaceToggleRow
            icon={AlertTriangle}
            accent="red"
            label="Allow unrecognised licences"
            hint="Off refuses any skill whose licence the scanner cannot identify. Leave it off unless you know the source."
            control={<Switch checked={unsafeLicense} onCheckedChange={setUnsafeLicense} />}
          />
        </CreateSurfaceDisclosure>
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        onCancel={onClose}
        primaryLabel={tab === "repo" && dryRun ? "Preview" : "Import"}
        primaryIcon={Upload}
        primaryDisabled={!ready}
        onPrimary={onClose}
      />
    </>
  )
}

/* ══ Credentials → Add secret ═══════════════════════════════════════════ */

const SECRET_STEPS = [
  { id: "type", label: "Shape" },
  { id: "values", label: "Values" },
  { id: "scope", label: "Who gets it" },
]

/**
 * The six shapes, taken from `CREDENTIAL_ITEM_TYPES` in lib/credentials/item-types.ts
 * rather than invented.
 *
 * The first draft of this specimen made up "OAuth tokens", "AWS keypair" and
 * "Custom fields" and dropped FILE and CERTIFICATE — a demo that claims to be
 * the real field set and is not is worse than no demo, because it moves the
 * migration's target. `extra` mirrors each shape's dependent fields, which is
 * the machinery the real wizard has and this specimen still only sketches.
 */
const SHAPES = [
  { id: "TOKEN", icon: KeyRound, accent: "amber" as const, title: "Token", description: "One secret field.", primary: "Token", extra: [] as string[] },
  { id: "LOGIN", icon: Lock, accent: "purple" as const, title: "Login", description: "Username and password.", primary: "Password", extra: ["Username"] },
  { id: "KEYPAIR", icon: Server, accent: "gold" as const, title: "Key pair", description: "Id and secret.", primary: "Secret access key", extra: ["Access key ID", "Region"] },
  { id: "SSH_KEY", icon: FileCode2, accent: "teal" as const, title: "SSH key", description: "PEM private key.", primary: "Private key", extra: ["Passphrase", "Public key"] },
  { id: "FILE", icon: FileJson, accent: "blue" as const, title: "File", description: "JSON, kubeconfig.", primary: "File contents", extra: ["File name"] },
  { id: "CERTIFICATE", icon: ShieldCheck, accent: "green" as const, title: "Certificate", description: "PEM chain for mTLS.", primary: "Certificate (PEM)", extra: ["Private key", "CA chain"] },
]

export function AddSecretContent({ onClose }: { onClose: () => void }) {
  const [step, setStep] = React.useState(0)
  const [shape, setShape] = React.useState<string | null>("TOKEN")
  const shapeDef = SHAPES.find((x) => x.id === shape)
  const [securityLevel, setSecurityLevel] = React.useState<"1" | "2" | "3">("2")
  const [value, setValue] = React.useState("")
  const [name, setName] = React.useState("")
  const [accountLabel, setAccountLabel] = React.useState("")
  const [tags, setTags] = React.useState<string[]>(["llm"])
  const [tagDraft, setTagDraft] = React.useState("")
  const [scope, setScope] = React.useState<"WORKSPACE" | "CREW">("WORKSPACE")
  const [slot, setSlot] = React.useState("")
  const [expiry, setExpiry] = React.useState("")

  const last = step === SECRET_STEPS.length - 1
  // Validate what the field SHOWS, not what was typed into it. `valid` read
  // `slot` while the input rendered `derivedSlot`, so after naming a secret in
  // step 2 the variable name looked filled — ANTHROPIC_PRODUCTION_KEY, right
  // there — and Save stayed disabled until you retyped it by hand.
  const derivedSlot = slot || name.trim().toUpperCase().replace(/[^A-Z0-9]+/g, "_")
  const valid =
    step === 0 ? shape !== null : step === 1 ? value.trim() !== "" && name.trim() !== "" : derivedSlot !== ""

  return (
    <>
      <CreateSurfaceHeader
        concept="credentials"
        context="Credentials"
        title="Add a credential"
        description="Encrypted with AES-256-GCM and never shown again."
        onClose={onClose}
        meta={
          <span className="max-sm:hidden">
            Step {step + 1} of {SECRET_STEPS.length}
          </span>
        }
      />

      <CreateSurfaceSteps steps={SECRET_STEPS} current={step} onJump={setStep} />

      <CreateSurfaceBody className="space-y-4">
        {step === 0 && (
          <div className="grid gap-2 sm:grid-cols-2 group-data-[mobile=true]/surface:grid-cols-1">
            {SHAPES.map((s) => (
              <CreateSurfaceTile
                key={s.id}
                icon={s.icon}
                accent={s.accent}
                title={s.title}
                description={s.description}
                selected={shape === s.id}
                meta={shape === s.id ? <Check className="h-3 w-3 text-primary-hover" /> : undefined}
                onClick={() => setShape(s.id)}
              />
            ))}
          </div>
        )}

        {step === 1 && (
          <>
            <CreateSurfaceField label="Name" htmlFor="secret-name" required hint="What this is, in your words.">
              <Input
                id="secret-name"
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Anthropic production key"
                className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>

            <CreateSurfaceField
              label={shapeDef?.primary ?? "Secret"}
              htmlFor="secret-value"
              required
              hint="Pasted once. It is never displayed again."
            >
              <Input
                id="secret-value"
                type="password"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="sk-ant-…"
                className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>

            {/* Dependent fields belong to the SHAPE, not to the form. Picking
                "Key pair" has to grow an access-key id and a region, or the
                shape step was decoration. */}
            {shapeDef && shapeDef.extra.length > 0 && (
              <CreateSurfaceGrid>
                {shapeDef.extra.map((f) => (
                  <CreateSurfaceField key={f} label={f}>
                    <Input placeholder={f} aria-label={f} className="h-8 text-xs max-sm:h-12 max-sm:text-sm" />
                  </CreateSurfaceField>
                ))}
              </CreateSurfaceGrid>
            )}

            <CreateSurfaceGrid>
              <CreateSurfaceField label="Account" htmlFor="secret-account" hint="Which account this belongs to, if it matters.">
                <Input
                  id="secret-account"
                  value={accountLabel}
                  onChange={(e) => setAccountLabel(e.target.value)}
                  placeholder="ops@unify.cz"
                  className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
                />
              </CreateSurfaceField>
              <CreateSurfaceField label="Expires" htmlFor="secret-expiry" hint="Warned about 14 days ahead.">
                <Input
                  id="secret-expiry"
                  type="date"
                  value={expiry}
                  onChange={(e) => setExpiry(e.target.value)}
                  className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
                />
              </CreateSurfaceField>
            </CreateSurfaceGrid>

            <CreateSurfaceField label="Security tier" hint="Tier 3 secrets require an approval before an agent may use one.">
              <CreateSurfaceChoice
                ariaLabel="Security tier"
                value={securityLevel}
                onChange={setSecurityLevel}
                options={[
                  { value: "1", label: "1 · Low" },
                  { value: "2", label: "2 · Guarded" },
                  { value: "3", label: "3 · Approval" },
                ]}
              />
            </CreateSurfaceField>

            <CreateSurfaceField label="Tags" hint="Used by the vault's facets. Enter to add.">
              <div className="flex flex-wrap items-center gap-1.5">
                {tags.map((t) => (
                  <span
                    key={t}
                    className="flex h-7 items-center gap-1 rounded-md border border-hairline bg-foreground/[0.04] pl-2 pr-1 text-xs text-foreground/85"
                  >
                    <Tag className="h-3 w-3 text-notice" />
                    {t}
                    <button
                      type="button"
                      aria-label={`Remove ${t}`}
                      onClick={() => setTags((ts) => ts.filter((x) => x !== t))}
                      className="rounded p-0.5 text-muted-foreground hover:text-foreground"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </span>
                ))}
                <Input
                  value={tagDraft}
                  onChange={(e) => setTagDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && tagDraft.trim()) {
                      e.preventDefault()
                      setTags((ts) => [...ts, tagDraft.trim()])
                      setTagDraft("")
                    }
                  }}
                  placeholder="Add tag…"
                  aria-label="Add tag"
                  className="h-7 w-28 text-xs max-sm:h-12 max-sm:w-full max-sm:text-sm"
                />
              </div>
            </CreateSurfaceField>
          </>
        )}

        {step === 2 && (
          <>
            <CreateSurfaceField
              label="Variable name"
              htmlFor="secret-slot"
              required
              hint="The environment variable the container will see this under."
            >
              <Input
                id="secret-slot"
                autoFocus
                value={derivedSlot}
                onChange={(e) => setSlot(e.target.value)}
                placeholder="ANTHROPIC_API_KEY"
                className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm"
              />
            </CreateSurfaceField>

            <CreateSurfaceField label="Scope">
              <CreateSurfaceChoice
                ariaLabel="Scope"
                value={scope}
                onChange={setScope}
                options={[
                  { value: "WORKSPACE", label: "Whole workspace" },
                  { value: "CREW", label: "Chosen crews" },
                ]}
              />
            </CreateSurfaceField>

            {scope === "CREW" && (
              <CreateSurfaceField label="Crews" hint="Only these crews' containers receive the variable.">
                <div className="relative">
                  <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft" />
                  <Input placeholder="Search crews…" className="h-8 pl-8 text-xs max-sm:h-12 max-sm:text-sm" />
                </div>
              </CreateSurfaceField>
            )}

            <CreateSurfaceNotice tone="ok" icon={ShieldCheck}>
              Written encrypted, and readable only by the containers you just named. Every read is recorded
              in the audit log.
            </CreateSurfaceNotice>
          </>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        onCancel={onClose}
        secondary={
          step > 0 ? (
            <CreateSurfaceSecondaryAction onClick={() => setStep((s) => s - 1)}>Back</CreateSurfaceSecondaryAction>
          ) : undefined
        }
        primaryLabel={last ? "Save credential" : "Continue"}
        primaryIcon={last ? Lock : undefined}
        primaryDisabled={!valid}
        onPrimary={() => (last ? onClose() : setStep((s) => s + 1))}
      />
    </>
  )
}

/* ══ Credentials → Connect via OAuth ════════════════════════════════════ */

const OAUTH_PROVIDERS = [
  { id: "github", icon: GitBranch, accent: "slate" as const, title: "GitHub", scopes: "repo · read:org" },
  { id: "slack", icon: MessageSquare, accent: "red" as const, title: "Slack", scopes: "chat:write · channels:read" },
  { id: "google", icon: Globe, accent: "blue" as const, title: "Google", scopes: "drive.readonly · calendar" },
  { id: "atlassian", icon: Network, accent: "teal" as const, title: "Atlassian", scopes: "read:jira-work" },
]

export function ConnectOAuthContent({ onClose }: { onClose: () => void }) {
  const [picked, setPicked] = React.useState<string | null>(null)
  const [scope, setScope] = React.useState<"WORKSPACE" | "CREW">("WORKSPACE")

  return (
    <>
      <CreateSurfaceHeader
        icon={Link2}
        accent="amber"
        context="Credentials"
        title="Connect via OAuth"
        description="Authorise in a popup. The resulting tokens are stored as an encrypted OAUTH2 credential and refreshed for you."
        onClose={onClose}
      />

      <CreateSurfaceBody className="space-y-2">
        {OAUTH_PROVIDERS.map((p) => (
          <CreateSurfaceTile
            key={p.id}
            icon={p.icon}
            accent={p.accent}
            title={p.title}
            description={p.scopes}
            selected={picked === p.id}
            meta={picked === p.id ? <Check className="h-3 w-3 text-primary-hover" /> : undefined}
            onClick={() => setPicked(p.id)}
          />
        ))}

        {picked && (
          <div className="pt-1">
            <CreateSurfaceField label="Scope" hint="Who may use the tokens once they come back.">
              <CreateSurfaceChoice
                ariaLabel="Scope"
                value={scope}
                onChange={setScope}
                options={[
                  { value: "WORKSPACE", label: "Whole workspace" },
                  { value: "CREW", label: "Chosen crews" },
                ]}
              />
            </CreateSurfaceField>
          </div>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        hint="A popup opens on the provider's own domain. Crewship never sees the password."
        onCancel={onClose}
        primaryLabel="Authorise"
        primaryIcon={Link2}
        primaryDisabled={!picked}
        onPrimary={onClose}
      />
    </>
  )
}

/* ══ Integrations → Add integration ═════════════════════════════════════ */

const KINDS = [
  {
    id: "notification",
    icon: Bell,
    accent: "blue" as const,
    title: "Notifications",
    description: "Chat, push, on-call, e-mail or your own endpoint.",
    meta: "reaches a person",
  },
  {
    id: "tools",
    icon: Wrench,
    accent: "purple" as const,
    title: "Tools & MCP",
    description: "Managed app accounts and MCP servers your agents can call.",
    meta: "an agent acts through it",
  },
]

const SERVICES: Record<string, { id: string; icon: typeof Bell; accent: "blue" | "teal" | "green" | "amber" | "purple" | "red" | "slate" | "gold"; title: string; description: string; auth: string }[]> = {
  notification: [
    { id: "slack", icon: MessageSquare, accent: "red", title: "Slack", description: "Channels, threads and approvals", auth: "OAuth" },
    { id: "email", icon: Globe, accent: "teal", title: "Email (SMTP)", description: "Digest and escalation mail", auth: "Password" },
    { id: "webhook", icon: Network, accent: "slate", title: "Webhook", description: "Push events to a URL you control", auth: "Secret" },
    { id: "ntfy", icon: Bell, accent: "amber", title: "ntfy", description: "Push to a phone without an app account", auth: "Topic" },
  ],
  tools: [
    { id: "github", icon: GitBranch, accent: "slate", title: "GitHub", description: "Issues, PRs and repository access", auth: "OAuth" },
    { id: "mcp", icon: Server, accent: "blue", title: "MCP server", description: "Any Model Context Protocol server, stdio or HTTP", auth: "URL" },
    { id: "vault", icon: Shield, accent: "gold", title: "External vault", description: "Read secrets from an existing store", auth: "Token" },
    { id: "linear", icon: Network, accent: "purple", title: "Linear", description: "Issues and cycles", auth: "OAuth" },
  ],
}

export function AddIntegrationContent({ onClose }: { onClose: () => void }) {
  const [kind, setKind] = React.useState<string | null>(null)
  const [service, setService] = React.useState<string | null>(null)
  const [query, setQuery] = React.useState("")

  const list = kind ? SERVICES[kind].filter((s) => s.title.toLowerCase().includes(query.toLowerCase())) : []

  return (
    <>
      <CreateSurfaceHeader
        concept="integrations"
        context="Integrations"
        title={kind ? (kind === "tools" ? "Tools & MCP" : "Notifications") : "Add integration"}
        description={
          kind
            ? "Picking a service opens its own setup. Nothing is connected until that finishes."
            : "Two kinds, because a Slack webhook and a managed tool account have nothing in common beyond the word."
        }
        onBack={kind ? () => { setKind(null); setService(null); setQuery("") } : undefined}
        onClose={onClose}
        meta={kind ? <span className="max-sm:hidden">{list.length} available</span> : undefined}
      />

      <CreateSurfaceBody className="space-y-3">
        {!kind &&
          KINDS.map((k) => (
            <CreateSurfaceTile
              key={k.id}
              icon={k.icon}
              accent={k.accent}
              title={k.title}
              description={k.description}
              meta={k.meta}
              onClick={() => setKind(k.id)}
            />
          ))}

        {kind && (
          <>
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft" />
              <Input
                autoFocus
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search services…"
                className="h-8 pl-8 text-xs max-sm:h-12 max-sm:text-sm"
              />
            </div>
            <div className="grid gap-2 sm:grid-cols-2 group-data-[mobile=true]/surface:grid-cols-1">
              {list.map((s) => (
                <CreateSurfaceTile
                  key={s.id}
                  icon={s.icon}
                  accent={s.accent}
                  title={s.title}
                  description={s.description}
                  meta={service === s.id ? <Check className="h-3 w-3 text-primary-hover" /> : s.auth}
                  selected={service === s.id}
                  onClick={() => setService(s.id)}
                />
              ))}
            </div>
            {list.length === 0 && (
              <CreateSurfaceNotice tone="info" icon={AlertTriangle}>
                Nothing matches “{query}”.
              </CreateSurfaceNotice>
            )}

            <CreateSurfaceDisclosure
              icon={Clock}
              accent="teal"
              label="Delivery defaults"
              summary="immediate · retry 3× · no quiet hours"
            >
              <CreateSurfaceGrid>
                <CreateSurfaceField label="Retries" htmlFor="int-retries">
                  <Input id="int-retries" type="number" defaultValue={3} className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm" />
                </CreateSurfaceField>
                <CreateSurfaceField label="Quiet hours" htmlFor="int-quiet" hint="Local to the workspace timezone.">
                  <Input id="int-quiet" placeholder="22:00–07:00" className="h-8 font-mono text-xs max-sm:h-12 max-sm:text-sm" />
                </CreateSurfaceField>
              </CreateSurfaceGrid>
            </CreateSurfaceDisclosure>
          </>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        onCancel={onClose}
        primaryLabel="Continue"
        primaryDisabled={!service}
        onPrimary={onClose}
      />
    </>
  )
}
