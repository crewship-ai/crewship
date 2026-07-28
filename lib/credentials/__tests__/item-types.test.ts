// The item-type catalog is the KISS answer PRD-CREDENTIALS-V2-2026 §0 landed
// on: SIX shapes plus custom fields, never a 150-brand catalog. These tests
// pin the two things a UI can get wrong without anyone noticing —
//
//   1. the shape a type implies (how many parts, which of them are secret)
//   2. the wire enum each one maps onto, which is a CLOSED set in
//      internal/api/credentials_types.go. A type the server rejects is a 400
//      the user cannot act on, and no component test would catch it because
//      the request never leaves the mock.

import { describe, it, expect } from "vitest"
import {
  CREDENTIAL_ITEM_TYPES,
  ITEM_TYPE_KEYS,
  SERVER_CREDENTIAL_TYPES,
  getItemType,
  itemTypeForCredentialType,
  extraFieldsFor,
} from "../item-types"

describe("catalog integrity", () => {
  it("offers exactly the six shapes the PRD scoped, and no brand catalog", () => {
    expect(ITEM_TYPE_KEYS).toEqual([
      "TOKEN",
      "LOGIN",
      "KEYPAIR",
      "SSH_KEY",
      "FILE",
      "CERTIFICATE",
    ])
  })

  it("maps every item type onto a credential type the server accepts", () => {
    for (const t of CREDENTIAL_ITEM_TYPES) {
      expect(SERVER_CREDENTIAL_TYPES).toContain(t.credentialType)
    }
  })

  it("gives every type exactly one primary secret — the value that becomes encrypted_value", () => {
    for (const t of CREDENTIAL_ITEM_TYPES) {
      expect(t.primary.secret).toBe(true)
      expect(t.primary.required).toBe(true)
    }
  })

  // credential_fields.go rejects `username`, `value` and `password` as field
  // keys ("that value lives on the credential row"), and requires
  // lower_snake_case. A catalog entry that violates either is a 400 on save.
  it("keeps every extra field key legal and non-reserved for POST /fields", () => {
    for (const t of CREDENTIAL_ITEM_TYPES) {
      for (const f of t.extra) {
        expect(f.key).toMatch(/^[a-z][a-z0-9_]{0,63}$/)
        expect(["username", "value", "password"]).not.toContain(f.key)
      }
    }
  })

  it("never repeats a field key inside one type", () => {
    for (const t of CREDENTIAL_ITEM_TYPES) {
      const keys = t.extra.map((f) => f.key)
      expect(new Set(keys).size).toBe(keys.length)
    }
  })
})

describe("shapes the old one-value model could not express (§1.5 V5)", () => {
  it("KEYPAIR is three parts, and the two identifiers stay cleartext", () => {
    const kp = getItemType("KEYPAIR")
    expect(kp.extra.map((f) => f.key)).toEqual(["access_key_id", "region"])
    expect(kp.extra.every((f) => f.secret)).toBe(false)
    // region is optional; the id is not.
    expect(kp.extra.find((f) => f.key === "access_key_id")?.required).toBe(true)
    expect(kp.extra.find((f) => f.key === "region")?.required).toBe(false)
  })

  it("SSH_KEY carries an optional secret passphrase and a cleartext public half", () => {
    const ssh = getItemType("SSH_KEY")
    expect(ssh.extra.find((f) => f.key === "passphrase")?.secret).toBe(true)
    expect(ssh.extra.find((f) => f.key === "public_key")?.secret).toBe(false)
    expect(ssh.primary.multiline).toBe(true)
  })

  it("LOGIN puts the username on the credential row, not in a custom field", () => {
    const login = getItemType("LOGIN")
    expect(login.usernameOnRow).toBe(true)
    expect(login.extra.map((f) => f.key)).not.toContain("username")
  })

  it("FILE is a blob plus a cleartext filename", () => {
    const file = getItemType("FILE")
    expect(file.primary.multiline).toBe(true)
    expect(file.extra.map((f) => f.key)).toEqual(["filename"])
  })
})

describe("getItemType", () => {
  it("returns the requested type", () => {
    expect(getItemType("CERTIFICATE").key).toBe("CERTIFICATE")
  })

  // An unknown key must not crash a render. Falling back to TOKEN — one
  // secret field — is the shape that is always safe to ask for.
  it("falls back to TOKEN for an unknown key instead of throwing", () => {
    expect(getItemType("NOPE" as never).key).toBe("TOKEN")
  })
})

describe("itemTypeForCredentialType — reading an existing row back", () => {
  it.each([
    ["USERPASS", "LOGIN"],
    ["SSH_KEY", "SSH_KEY"],
    ["CERTIFICATE", "CERTIFICATE"],
    ["CLI_TOKEN", "TOKEN"],
    ["API_KEY", "TOKEN"],
    ["AI_CLI_TOKEN", "TOKEN"],
  ] as const)("maps %s back to %s", (server, item) => {
    expect(itemTypeForCredentialType(server)).toBe(item)
  })

  it("treats anything unrecognised as a plain token", () => {
    expect(itemTypeForCredentialType("SOMETHING_NEW")).toBe("TOKEN")
  })
})

describe("extraFieldsFor", () => {
  it("drops empty optional parts so no empty field is POSTed", () => {
    // credential_fields.go: "field value is required (delete the field
    // instead of storing an empty one)" — an empty optional would be a 400.
    const out = extraFieldsFor("KEYPAIR", { access_key_id: "AKIA1", region: "   " })
    expect(out).toEqual([
      { key: "access_key_id", value: "AKIA1", is_secret: false, ordinal: 0 },
    ])
  })

  it("preserves the catalog's secret classification per field", () => {
    const out = extraFieldsFor("SSH_KEY", { passphrase: "hunter2", public_key: "ssh-ed25519 AAAA" })
    expect(out).toEqual([
      { key: "passphrase", value: "hunter2", is_secret: true, ordinal: 0 },
      { key: "public_key", value: "ssh-ed25519 AAAA", is_secret: false, ordinal: 1 },
    ])
  })

  it("returns nothing for a type with no extras and no user-added fields", () => {
    expect(extraFieldsFor("TOKEN", {})).toEqual([])
  })

  it("appends user-defined custom fields after the catalog ones", () => {
    const out = extraFieldsFor(
      "TOKEN",
      {},
      [{ key: "tenant_id", value: "acme", secret: false }],
    )
    expect(out).toEqual([{ key: "tenant_id", value: "acme", is_secret: false, ordinal: 0 }])
  })

  it("skips a custom field with a blank key or blank value", () => {
    const out = extraFieldsFor(
      "TOKEN",
      {},
      [
        { key: "", value: "x", secret: false },
        { key: "ok", value: "  ", secret: true },
        { key: "kept", value: "v", secret: true },
      ],
    )
    expect(out).toEqual([{ key: "kept", value: "v", is_secret: true, ordinal: 0 }])
  })
})
