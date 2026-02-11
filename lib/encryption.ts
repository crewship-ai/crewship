import { createCipheriv, createDecipheriv, randomBytes } from 'crypto'

const ALGORITHM = 'aes-256-gcm'
const CURRENT_KEY_VERSION = 'v1'

function getEncryptionKey(version: string = CURRENT_KEY_VERSION): Buffer {
  const envVar = version === 'v1' ? 'ENCRYPTION_KEY' : `ENCRYPTION_KEY_${version.toUpperCase()}`
  const key = process.env[envVar] ?? process.env.ENCRYPTION_KEY

  if (!key) {
    throw new Error(`${envVar} is not set`)
  }

  return Buffer.from(key, 'hex')
}

/**
 * Encrypts plaintext with AES-256-GCM and prepends key version prefix.
 * Output format: "v1:base64(IV + AuthTag + Ciphertext)"
 */
export function encrypt(plaintext: string): string {
  const key = getEncryptionKey()
  const iv = randomBytes(16)
  const cipher = createCipheriv(ALGORITHM, key, iv)

  const encrypted = Buffer.concat([
    cipher.update(plaintext, 'utf8'),
    cipher.final(),
  ])

  const authTag = cipher.getAuthTag()
  const combined = Buffer.concat([iv, authTag, encrypted])

  return `${CURRENT_KEY_VERSION}:${combined.toString('base64')}`
}

/**
 * Decrypts ciphertext with AES-256-GCM. Supports key versioning.
 * Accepts both "v1:base64data" (versioned) and plain "base64data" (legacy).
 */
export function decrypt(ciphertext: string): string {
  let version = CURRENT_KEY_VERSION
  let encoded = ciphertext

  const colonIndex = ciphertext.indexOf(':')
  if (colonIndex > 0 && colonIndex <= 3) {
    const prefix = ciphertext.slice(0, colonIndex)
    if (/^v\d+$/.test(prefix)) {
      version = prefix
      encoded = ciphertext.slice(colonIndex + 1)
    }
  }

  const key = getEncryptionKey(version)
  const buffer = Buffer.from(encoded, 'base64')

  const iv = buffer.subarray(0, 16)
  const authTag = buffer.subarray(16, 32)
  const encrypted = buffer.subarray(32)

  if (authTag.length !== 16) {
    throw new Error('Invalid authentication tag length')
  }

  const decipher = createDecipheriv(ALGORITHM, key, iv, { authTagLength: 16 })
  decipher.setAuthTag(authTag)

  const decrypted = Buffer.concat([
    decipher.update(encrypted),
    decipher.final(),
  ])

  return decrypted.toString('utf8')
}
