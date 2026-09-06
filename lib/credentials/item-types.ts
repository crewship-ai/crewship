/**
 * Credential item types — the Vaultwarden-shaped answer from
 * PRD-CREDENTIALS-V2-2026 §0 and §2.2.
 *
 * The first draft of that PRD proposed a ~150-brand recipe catalog. It was
 * rejected, and this module is what replaced it: SIX shapes, plus arbitrary
 * custom fields. The argument that settled it is worth repeating here because
 * it is the reason this file must stay short — a catalog only ever bought two
 * things, an icon and a suggested env-var name, and we already have both
 * (`lib/credential-providers/registry.ts`, `detectFromValue`). A wrong row in
 * a brand catalog is worse than no row at all, because the user believes it.
 *
 * So: the SHAPE comes from the type the user picks. The BRAND only ever
 * contributes an icon and a suggestion — a hint, never a gate.
 *
 * Storage split, which is the whole reason the shapes matter:
 *   · `primary`  → `credentials.encrypted_value`, via POST /api/v1/credentials
 *   · `extra[]`  → `credential_fields`, via POST /credentials/{id}/fields
 *   · LOGIN's username → `credentials.username`, cleartext ON PURPOSE
 *     (§2.2: "non-secret identifiers are cleartext" — it is an identifier,
 *     and keeping it out of the AEAD surface is what lets the list search
 *     and sort without a per-row decrypt).
 */

/** The closed set of credential `type` values internal/api/credentials_types.go accepts. */
export const SERVER_CREDENTIAL_TYPES = [
  "PROVIDER_LOGIN",
  "AI_CLI_TOKEN",
  "API_KEY",
  "CLI_TOKEN",
  "SECRET",
  "OAUTH2",
  "USERPASS",
  "SSH_KEY",
  "CERTIFICATE",
  "GENERIC_SECRET",
  "ENDPOINT_URL",
] as const

export type ServerCredentialType = (typeof SERVER_CREDENTIAL_TYPES)[number]

/**
 * Short, lowercase names for the server's type enum — what the list badge, the
 * rail chip and the overview breakdown all call a type.
 *
 * Lowercase on purpose: these sit next to a monospace credential name, and
 * SCREAMING_SNAKE beside it reads as part of the identifier rather than as a
 * label about it. Two server types collapse to "secret" because that is what
 * they are; the distinction between SECRET and GENERIC_SECRET is a storage
 * detail nobody scanning a table is asking about.
 */
const CREDENTIAL_TYPE_LABELS: Record<string, string> = {
  PROVIDER_LOGIN: "provider login",
  AI_CLI_TOKEN: "ai cli",
  API_KEY: "api key",
  CLI_TOKEN: "token",
  SECRET: "secret",
  OAUTH2: "oauth",
  USERPASS: "userpass",
  SSH_KEY: "ssh key",
  CERTIFICATE: "cert",
  GENERIC_SECRET: "secret",
  ENDPOINT_URL: "endpoint",
}

/**
 * The label for a server type. An unrecognised value is lowercased rather than
 * dropped or renamed: a type the console has not heard of is still a real type
 * on the row, and showing it verbatim is the only honest answer.
 */
export function credentialTypeLabel(type: string): string {
  return CREDENTIAL_TYPE_LABELS[type] ?? type.toLowerCase().replace(/_/g, " ")
}

export type ItemTypeKey = "PROVIDER_LOGIN" | "TOKEN" | "LOGIN" | "KEYPAIR" | "SSH_KEY" | "FILE" | "CERTIFICATE"

export interface CredentialItemField {
  /** Field key. Must satisfy credential_fields.go: `^[a-z][a-z0-9_]{0,63}$`. */
  key: string
  label: string
  /** Secret parts are encrypted and NEVER read back — the API returns a null value. */
  secret: boolean
  required: boolean
  multiline?: boolean
  placeholder?: string
  hint?: string
}

export interface CredentialItemType {
  key: ItemTypeKey
  label: string
  /** One line under the tile — what the type is for, in the user's words. */
  blurb: string
  /** What POST /api/v1/credentials is told this credential is. */
  credentialType: ServerCredentialType
  /** The single value that lands in `credentials.encrypted_value`. */
  primary: CredentialItemField
  /** Additional parts, stored as credential_fields rows in this order. */
  extra: CredentialItemField[]
  /** True when the type also fills `credentials.username` (cleartext). */
  usernameOnRow?: boolean
  /** Shown as a warning on the form — types that must materialise as a file. */
  fileNote?: string
}

export const CREDENTIAL_ITEM_TYPES: CredentialItemType[] = [
  {
    // The seventh shape (#2428, docs/prd/provider-logins.md §5–6). A provider
    // login is not a secret the agent USES but a seat the agent PAYS WITH —
    // it has an owner, a plan and an expiry, and Crewship keeps it valid. The
    // six shapes could not express it: "Token" stores CLI_TOKEN, which lands
    // in /secrets as a file no model CLI reads, so until this tile there was
    // no way to create an AI_CLI_TOKEN or an API_KEY from the console at all.
    //
    // credentialType is the contract's own type (§10.2): the server parses
    // the pasted value into parts and seals the refresh token itself, and the
    // mode travels as its own field. Presentation — label, placeholder, hint,
    // suggested slot — depends on the brand and the mode and lives in
    // providerLoginPresentation, not in a static field.
    key: "PROVIDER_LOGIN",
    label: "Provider login",
    blurb: "Subscription or API key that pays for a model",
    credentialType: "PROVIDER_LOGIN",
    primary: {
      key: "value",
      label: "Login",
      secret: true,
      required: true,
      placeholder: "Paste the login",
    },
    extra: [],
  },
  {
    key: "TOKEN",
    label: "Token",
    blurb: "One secret field",
    credentialType: "CLI_TOKEN",
    primary: {
      key: "value",
      label: "Token",
      secret: true,
      required: true,
      placeholder: "Paste the token",
    },
    extra: [],
  },
  {
    key: "LOGIN",
    label: "Login",
    blurb: "Username and password",
    credentialType: "USERPASS",
    usernameOnRow: true,
    primary: {
      key: "value",
      label: "Password",
      secret: true,
      required: true,
      placeholder: "Paste the password",
    },
    extra: [],
  },
  {
    key: "KEYPAIR",
    label: "Key pair",
    blurb: "Id and secret",
    credentialType: "GENERIC_SECRET",
    primary: {
      key: "value",
      label: "Secret access key",
      secret: true,
      required: true,
      placeholder: "The secret half",
    },
    extra: [
      {
        key: "access_key_id",
        label: "Access key ID",
        secret: false,
        required: true,
        placeholder: "AKIA…",
        hint: "An identifier, not a secret — stored in the clear so it stays searchable.",
      },
      {
        key: "region",
        label: "Region",
        secret: false,
        required: false,
        placeholder: "eu-central-1",
      },
    ],
  },
  {
    key: "SSH_KEY",
    label: "SSH key",
    blurb: "PEM private key",
    credentialType: "SSH_KEY",
    primary: {
      key: "value",
      label: "Private key",
      secret: true,
      required: true,
      multiline: true,
      placeholder: "-----BEGIN OPENSSH PRIVATE KEY-----",
    },
    extra: [
      { key: "passphrase", label: "Passphrase", secret: true, required: false },
      {
        key: "public_key",
        label: "Public key",
        secret: false,
        required: false,
        multiline: true,
        placeholder: "ssh-ed25519 AAAA…",
      },
    ],
  },
  {
    key: "FILE",
    label: "File",
    blurb: "JSON, kubeconfig",
    credentialType: "GENERIC_SECRET",
    primary: {
      key: "value",
      label: "File contents",
      secret: true,
      required: true,
      multiline: true,
      placeholder: "Paste the file contents",
    },
    extra: [
      {
        key: "filename",
        label: "File name",
        secret: false,
        required: false,
        placeholder: "sa-key.json",
      },
    ],
    fileNote:
      "This shape has to become a file inside the container. It is written to tmpfs for the run and removed afterwards — never to a persistent disk.",
  },
  {
    key: "CERTIFICATE",
    label: "Certificate",
    blurb: "PEM chain for mTLS",
    credentialType: "CERTIFICATE",
    primary: {
      key: "value",
      label: "Certificate (PEM)",
      secret: true,
      required: true,
      multiline: true,
      placeholder: "-----BEGIN CERTIFICATE-----",
    },
    extra: [
      { key: "key_pem", label: "Private key (PEM)", secret: true, required: false, multiline: true },
      { key: "ca_pem", label: "CA chain (PEM)", secret: false, required: false, multiline: true },
    ],
    fileNote:
      "Certificates are delivered as files. They are written to tmpfs for the run and removed afterwards.",
  },
]

export const ITEM_TYPE_KEYS: ItemTypeKey[] = CREDENTIAL_ITEM_TYPES.map((t) => t.key)

const BY_KEY = new Map<string, CredentialItemType>(CREDENTIAL_ITEM_TYPES.map((t) => [t.key, t]))

/**
 * Look a type up. An unknown key returns TOKEN rather than throwing: this runs
 * during render, and a single secret field is the shape that is always safe to
 * ask for.
 */
export function getItemType(key: ItemTypeKey | string): CredentialItemType {
  return BY_KEY.get(key) ?? BY_KEY.get("TOKEN")!
}

/**
 * Reverse map, for reading an EXISTING credential back into the picker. The
 * server's type enum is wider than the six shapes (SECRET, OAUTH2,
 * ENDPOINT_URL, …) because it predates them, so anything without a shape of
 * its own reads as a plain token — one secret field, which is what those rows
 * actually are.
 */
export function itemTypeForCredentialType(credentialType: string): ItemTypeKey {
  switch (credentialType) {
    case "PROVIDER_LOGIN":
    case "AI_CLI_TOKEN":
      return "PROVIDER_LOGIN"
    case "USERPASS":
      return "LOGIN"
    case "SSH_KEY":
      return "SSH_KEY"
    case "CERTIFICATE":
      return "CERTIFICATE"
    default:
      return "TOKEN"
  }
}

/** How a provider login pays: a flat-rate seat, or a metered key. */
export type ProviderLoginMode = "subscription" | "api_key"

/**
 * The server type a provider login is created as. One type for both modes
 * since contract §10.2: `PROVIDER_LOGIN`, with `mode` as its own field. The
 * older rows (`AI_CLI_TOKEN`, or `API_KEY` with an AI provider) still read as
 * logins — the server derives their `login` object — but nothing new is
 * written that way.
 */
export function providerLoginCredentialType(_mode: ProviderLoginMode): ServerCredentialType {
  return "PROVIDER_LOGIN"
}

export interface ProviderLoginPresentation {
  /** Label over the secret box. */
  label: string
  placeholder: string
  /** A Codex login is a whole JSON file; everything else is one line. */
  multiline: boolean
  /** How to obtain the value, in the user's words. Empty when nothing needs saying. */
  hint: string
  /** The binding slot to suggest. For a subscription it is a NAME, never a variable the CLI reads. */
  slot: string | null
  /** False when Crewship cannot deliver a subscription login for this brand (yet). */
  supported: boolean
}

/**
 * What the value box asks for, per brand and mode. The table is small on
 * purpose and every row states a fact about the CLI it names (measured, not
 * assumed — docs/guides/cli/*.mdx): Claude Code takes a setup-token in an env
 * var; Codex reads its ChatGPT login only from $CODEX_HOME/auth.json, so the
 * WHOLE file is the value and the refresh token in it never leaves the
 * server; the rest have no subscription login Crewship can deliver, only a key.
 */
export function providerLoginPresentation(provider: string, mode: ProviderLoginMode): ProviderLoginPresentation {
  const p = (provider ?? "").toUpperCase()
  if (mode === "api_key") {
    const keyOf: Record<string, { placeholder: string; slot: string }> = {
      ANTHROPIC: { placeholder: "sk-ant-api03-…", slot: "ANTHROPIC_API_KEY" },
      OPENAI: { placeholder: "sk-proj-… or sk-svcacct-…", slot: "OPENAI_API_KEY" },
      GOOGLE: { placeholder: "AIza…", slot: "GOOGLE_API_KEY" },
      CURSOR: { placeholder: "key_…", slot: "CURSOR_API_KEY" },
      FACTORY: { placeholder: "fk-…", slot: "FACTORY_API_KEY" },
      XAI: { placeholder: "Paste the xAI API key", slot: "XAI_API_KEY" },
      GROQ: { placeholder: "Paste the Groq API key", slot: "GROQ_API_KEY" },
      OPENROUTER: { placeholder: "Paste the OpenRouter API key", slot: "OPENROUTER_API_KEY" },
      DEEPSEEK: { placeholder: "Paste the DeepSeek API key", slot: "DEEPSEEK_API_KEY" },
      MOONSHOT: { placeholder: "Paste the Moonshot API key", slot: "MOONSHOT_API_KEY" },
      ZAI: { placeholder: "Paste the Z.AI API key", slot: "ZAI_API_KEY" },
      MINIMAX: { placeholder: "Paste the MiniMax API key", slot: "MINIMAX_API_KEY" },
    }
    const k = keyOf[p]
    return {
      label: "API key",
      placeholder: k?.placeholder ?? "Paste the API key",
      multiline: false,
      hint: "",
      slot: k?.slot ?? null,
      supported: Boolean(k),
    }
  }
  switch (p) {
    case "ANTHROPIC":
      return {
        label: "Setup token",
        placeholder: "sk-ant-oat01-…",
        multiline: false,
        hint: "Run `claude setup-token` on your computer, then paste the generated token below.",
        slot: "CLAUDE_CODE_OAUTH_TOKEN",
        supported: true,
      }
    case "GOOGLE":
      return {
        label: "Gemini login (oauth_creds.json)",
        placeholder: '{ "access_token": "…", "refresh_token": "…", "expiry_date": 0 }',
        multiline: true,
        hint: "Run `gemini`, choose Sign in with Google, then paste the contents of ~/.gemini/oauth_creds.json below. For company accounts requiring a Cloud project, use an API key here.",
        slot: "GEMINI_API_KEY",
        supported: true,
      }
    case "OPENAI":
      return {
        label: "Codex login (auth.json)",
        placeholder: '{ "auth_mode": "chatgpt", "tokens": { … } }  — the whole ~/.codex/auth.json',
        multiline: true,
        hint: "Run `codex login` on your computer, then paste the contents of ~/.codex/auth.json below.",
        slot: "OPENAI_API_KEY",
        supported: true,
      }
    default:
      return {
        label: "Login",
        placeholder: "",
        multiline: false,
        hint: p && p !== "NONE"
          ? "This provider has no subscription login Crewship can deliver yet — switch to API key."
          : "Choose your AI provider above to see its sign-in instructions.",
        slot: null,
        supported: false,
      }
  }
}

/** One row for POST /api/v1/credentials/{id}/fields. */
export interface CredentialFieldPayload {
  key: string
  value: string
  is_secret: boolean
  ordinal: number
}

/** A field the user added themselves — the long-tail escape hatch (§2.2). */
export interface CustomFieldDraft {
  key: string
  value: string
  secret: boolean
}

/**
 * Turn the typed parts plus any user-added fields into POST bodies.
 *
 * Blank values are dropped rather than sent: credential_fields.go answers
 * "field value is required (delete the field instead of storing an empty one)"
 * with a 400, so an untouched optional part would fail the whole save for a
 * field the user never asked for. Ordinals are assigned after filtering so the
 * surviving rows are contiguous.
 */
export function extraFieldsFor(
  itemTypeKey: ItemTypeKey | string,
  values: Record<string, string>,
  custom: CustomFieldDraft[] = [],
): CredentialFieldPayload[] {
  const type = getItemType(itemTypeKey)
  const out: CredentialFieldPayload[] = []

  for (const field of type.extra) {
    const raw = (values[field.key] ?? "").trim()
    if (!raw) continue
    out.push({ key: field.key, value: raw, is_secret: field.secret, ordinal: 0 })
  }
  for (const draft of custom) {
    const key = (draft.key ?? "").trim()
    const value = (draft.value ?? "").trim()
    if (!key || !value) continue
    out.push({ key, value, is_secret: draft.secret, ordinal: 0 })
  }
  return out.map((f, i) => ({ ...f, ordinal: i }))
}
