/**
 * An age X25519 key pair, made in the browser so the private half never
 * reaches the server: backups are encrypted to the public half (age1…), and
 * only whoever holds the private one (AGE-SECRET-KEY-1…) can read them.
 *
 * The encoding is age's: Bech32 (BIP 173, without its length limit) of the
 * 32-byte X25519 public key under "age", and of the private scalar under
 * "age-secret-key-", upper-cased. Checked against age's test vector and the
 * age CLI (age -R / age -d -i).
 */

const CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
const GEN = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3]

function polymod(values: number[]): number {
  let chk = 1
  for (const v of values) {
    const top = chk >>> 25
    chk = ((chk & 0x1ffffff) << 5) ^ v
    for (let i = 0; i < 5; i++) if ((top >>> i) & 1) chk ^= GEN[i]
  }
  return chk >>> 0
}

function hrpExpand(hrp: string): number[] {
  const codes = [...hrp].map((c) => c.charCodeAt(0))
  return [...codes.map((c) => c >> 5), 0, ...codes.map((c) => c & 31)]
}

/** 8-bit bytes to 5-bit groups, padded. */
function toWords(data: Uint8Array): number[] {
  let acc = 0
  let bits = 0
  const out: number[] = []
  for (const b of data) {
    acc = (acc << 8) | b
    bits += 8
    while (bits >= 5) {
      bits -= 5
      out.push((acc >> bits) & 31)
    }
  }
  if (bits > 0) out.push((acc << (5 - bits)) & 31)
  return out
}

export function bech32Encode(hrp: string, data: Uint8Array): string {
  const words = toWords(data)
  const mod = polymod([...hrpExpand(hrp), ...words, 0, 0, 0, 0, 0, 0]) ^ 1
  const checksum = [0, 1, 2, 3, 4, 5].map((i) => (mod >>> (5 * (5 - i))) & 31)
  return `${hrp}1${[...words, ...checksum].map((w) => CHARSET[w]).join("")}`
}

function fromBase64Url(s: string): Uint8Array {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)
  const bin = atob(b64)
  return Uint8Array.from(bin, (c) => c.charCodeAt(0))
}

export interface AgeKeyPair {
  /** The public key backups are encrypted to: age1… */
  recipient: string
  /** The private key that reads them: AGE-SECRET-KEY-1… Never sent anywhere. */
  identity: string
}

/** The age identity and recipient of a 32-byte X25519 private scalar and its public key. */
export function encodeAgeKeys(privateScalar: Uint8Array, publicKey: Uint8Array): AgeKeyPair {
  return {
    recipient: bech32Encode("age", publicKey),
    identity: bech32Encode("age-secret-key-", privateScalar).toUpperCase(),
  }
}

/** Whether this browser can make an X25519 key (Web Crypto). */
export function canGenerateAgeKey(): boolean {
  return typeof globalThis.crypto?.subtle?.generateKey === "function"
}

/** A fresh age key pair, made here with Web Crypto's X25519. */
export async function generateAgeKey(): Promise<AgeKeyPair> {
  const pair = await crypto.subtle.generateKey({ name: "X25519" }, true, ["deriveBits"]) as CryptoKeyPair
  const publicKey = new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey))
  const jwk = await crypto.subtle.exportKey("jwk", pair.privateKey)
  if (!jwk.d) throw new Error("This browser did not give out the private key")
  return encodeAgeKeys(fromBase64Url(jwk.d), publicKey)
}

/** The file to keep: age-keygen's layout, so the age CLI and `crewship recover --identity` read it. */
export function ageKeyFile(key: AgeKeyPair, created = new Date()): string {
  return `# created: ${created.toISOString()}\n# public key: ${key.recipient}\n${key.identity}\n`
}
