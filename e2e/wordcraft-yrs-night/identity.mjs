import assert from 'node:assert/strict'
import { localRequest } from './local.mjs'

export async function verifyProductIdentity(context, baseURL, expectedSHA) {
  assert.match(expectedSHA, /^[a-f0-9]{40}$/, 'Exact expected product SHA is required')
  // APIRequestContext belongs to the authenticated browser context, so its
  // actual cookie jar reaches the auth-protected version endpoint.
  const response = await localRequest(context, baseURL, '/api/v1/system/version')
  assert.equal(response.status(), 200, 'Authenticated product identity response required')
  const version = await response.json()
  assert.equal(version.commit, expectedSHA, 'Unexpected product binary')
  return version.commit
}
