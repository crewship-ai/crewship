import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { randomBytes } from "crypto"

// Generate a valid 32-byte hex key for testing
const TEST_KEY = randomBytes(32).toString("hex")
const ALT_KEY = randomBytes(32).toString("hex")

describe("encryption", () => {
  beforeEach(() => {
    vi.stubEnv("ENCRYPTION_KEY", TEST_KEY)
  })

  afterEach(() => {
    vi.unstubAllEnvs()
  })

  // Dynamic import to pick up env stubs each time
  async function loadEncryption() {
    return await import("@/lib/encryption")
  }

  it("encrypt returns string starting with 'v1:'", async () => {
    const { encrypt } = await loadEncryption()
    const result = encrypt("hello")
    expect(result).toMatch(/^v1:/)
  })

  it("decrypt of encrypted value returns original plaintext", async () => {
    const { encrypt, decrypt } = await loadEncryption()
    const plaintext = "secret-api-key-12345"
    const encrypted = encrypt(plaintext)
    const decrypted = decrypt(encrypted)
    expect(decrypted).toBe(plaintext)
  })

  it("different plaintexts produce different ciphertexts", async () => {
    const { encrypt } = await loadEncryption()
    const a = encrypt("plaintext-a")
    const b = encrypt("plaintext-b")
    expect(a).not.toBe(b)
  })

  it("same plaintext encrypted twice produces different ciphertexts (random IV)", async () => {
    const { encrypt } = await loadEncryption()
    const a = encrypt("same-text")
    const b = encrypt("same-text")
    expect(a).not.toBe(b)
  })

  it("encrypt/decrypt with empty string", async () => {
    const { encrypt, decrypt } = await loadEncryption()
    const encrypted = encrypt("")
    expect(encrypted).toMatch(/^v1:/)
    const decrypted = decrypt(encrypted)
    expect(decrypted).toBe("")
  })

  it("encrypt/decrypt with long string (1000+ chars)", async () => {
    const { encrypt, decrypt } = await loadEncryption()
    const longText = "x".repeat(2000)
    const encrypted = encrypt(longText)
    const decrypted = decrypt(encrypted)
    expect(decrypted).toBe(longText)
  })

  it("encrypt/decrypt with special characters and unicode", async () => {
    const { encrypt, decrypt } = await loadEncryption()
    const special = "héllo wörld! 🚀 日本語 中文 <script>alert('xss')</script> \n\t"
    const encrypted = encrypt(special)
    const decrypted = decrypt(encrypted)
    expect(decrypted).toBe(special)
  })

  it("decrypt with wrong key throws", async () => {
    const { encrypt, decrypt } = await loadEncryption()
    const encrypted = encrypt("test-data")

    // Change the key
    vi.stubEnv("ENCRYPTION_KEY", ALT_KEY)

    expect(() => decrypt(encrypted)).toThrow()
  })

  it("decrypt with invalid base64 throws", async () => {
    const { decrypt } = await loadEncryption()
    expect(() => decrypt("v1:not-valid-base64!!!")).toThrow()
  })

  it("encrypt without ENCRYPTION_KEY env var throws", async () => {
    vi.stubEnv("ENCRYPTION_KEY", "")
    // Need to also delete it since stubEnv sets it to empty string
    delete process.env.ENCRYPTION_KEY

    const { encrypt } = await loadEncryption()
    expect(() => encrypt("test")).toThrow(/ENCRYPTION_KEY.*not set/)
  })
})
