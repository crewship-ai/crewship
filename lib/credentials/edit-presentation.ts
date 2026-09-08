/** Based on stored metadata, never guessed from secret bytes. */
export function credentialEditPresentation(type: string, keys: string[] = []) {
  if (type === "USERPASS") return { label: "Password", multiline: false, hint: "Replace the password issued by the service. Edit the username separately." }
  if (type === "SSH_KEY") return { label: "Private key (PEM)", multiline: true, hint: "Paste the complete private key. This does not install its public key on a server." }
  if (type === "CERTIFICATE" || type === "CERT") return { label: "Certificate (PEM)", multiline: true, hint: "Paste the complete certificate chain. This does not issue or renew a certificate." }
  if (type === "GENERIC_SECRET" && keys.includes("access_key_id")) return { label: "Secret access key", multiline: false, hint: "Use the secret matching the access key ID in Additional fields." }
  if (type === "GENERIC_SECRET") return { label: "File or secret contents", multiline: true, hint: "Paste the complete contents. Existing values are never loaded into this editor." }
  if (type === "ENDPOINT_URL") return { label: "Endpoint URL", multiline: false, hint: "Use the full endpoint URL. It may contain credentials and stays hidden by default." }
  return { label: type === "API_KEY" ? "API key" : "Secret value", multiline: false, hint: "Obtain a replacement from the issuing service first. Saving changes Crewship only." }
}
