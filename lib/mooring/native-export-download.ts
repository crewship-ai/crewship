/** Download an explicit editor export in the parent, keeping the editor frame opaque.
 * A downloaded copy never acknowledges or changes the server save state.
 */
export function downloadNativeExport(file: {name: string; bytes: Uint8Array}): void {
  if (!file.name || file.name.length > 256 || /[\\/\u0000-\u001f\u007f]/.test(file.name) ||
      file.bytes.byteLength === 0 || file.bytes.byteLength > 32 * 1024 * 1024) {
    throw new Error("invalid_native_export")
  }
  const bytes = new Uint8Array(file.bytes)
  const url = URL.createObjectURL(new Blob([bytes], {type: "application/octet-stream"}))
  const revoke = URL.revokeObjectURL.bind(URL)
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = file.name
  anchor.hidden = true
  try {
    document.body.append(anchor)
    anchor.click()
  } catch (error) {
    revoke(url)
    throw error
  } finally {
    anchor.remove()
  }
  // Chromium retains its download reference after dispatch; allow it to start first.
  setTimeout(() => revoke(url), 10_000)
}
