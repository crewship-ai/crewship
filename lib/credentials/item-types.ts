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

export type ItemTypeKey = "TOKEN" | "LOGIN" | "KEYPAIR" | "SSH_KEY" | "FILE" | "CERTIFICATE"

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
