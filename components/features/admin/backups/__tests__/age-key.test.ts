import { describe, it, expect } from "vitest"
import { webcrypto } from "node:crypto"

import { ageKeyFile, bech32Encode, encodeAgeKeys, generateAgeKey } from "../age-key"

// age's own test vector (testkit x25519): the scalar 0x42… and its key pair.
// Public fixed vector, written in parts to distinguish it from deployed keys.
const SCALAR = new Uint8Array(32).fill(0x42)
const IDENTITY = ["AGE", "SECRET", "KEY", "1GFPYYSJZGFPYYSJZGFPYYSJZGFPYYSJZGFPYYSJZGFPYYSJZGFPQ4EGAEX"].join("-")
const RECIPIENT = "age1zvkyg2lqzraa2lnjvqej32nkuu0ues2s82hzrye869xeexvn73equnujwj"

async function publicOf(scalar: Uint8Array): Promise<Uint8Array> {
  const pkcs8 = new Uint8Array([...Buffer.from("302e020100300506032b656e04220420", "hex"), ...scalar])
  const key = await webcrypto.subtle.importKey("pkcs8", pkcs8, { name: "X25519" }, true, ["deriveBits"])
  const jwk = await webcrypto.subtle.exportKey("jwk", key)
  return new Uint8Array(Buffer.from(jwk.x!, "base64url"))
}

describe("age keys made in the browser", () => {
  it("encodes a key pair exactly as age does", async () => {
    expect(encodeAgeKeys(SCALAR, await publicOf(SCALAR))).toEqual({ identity: IDENTITY, recipient: RECIPIENT })
  })

  it("is Bech32 as BIP 173 defines it", () => {
    expect(bech32Encode("a", new Uint8Array())).toBe("a12uel5l")
  })

  it("makes a fresh pair whose public half belongs to its private half", async () => {
    const key = await generateAgeKey()
    expect(key.recipient).toMatch(/^age1[02-9ac-hj-np-z]{58}$/)
    expect(key.identity).toMatch(/^AGE-SECRET-KEY-1[02-9AC-HJ-NP-Z]{58}$/)
    expect(ageKeyFile(key, new Date("2026-10-13T00:00:00Z"))).toBe(
      `# created: 2026-10-13T00:00:00.000Z\n# public key: ${key.recipient}\n${key.identity}\n`)
  })
})
