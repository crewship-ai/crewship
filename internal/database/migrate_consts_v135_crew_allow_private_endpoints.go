package database

// migrationCrewAllowPrivateEndpoints (v135) adds the per-crew opt-in for
// reaching a private/LAN model endpoint (#961). Default 0 (off): the SSRF
// egress fence refuses an ENDPOINT_URL whose host resolves into an RFC1918 /
// loopback / ULA range unless the crew's operator explicitly enables it (a
// legitimate on-prem Ollama, or host.docker.internal whose gateway is a
// private IP). Link-local/metadata addresses stay blocked regardless of this
// flag. Additive, non-nullable with a safe default so pre-migration crews
// keep the strict default.
const migrationCrewAllowPrivateEndpoints = `
ALTER TABLE crews ADD COLUMN allow_private_endpoints INTEGER NOT NULL DEFAULT 0;
`
