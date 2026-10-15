import http from 'node:http';
import { readFile } from 'node:fs/promises';
const secret = (await readFile(process.env.CREWSHIP_DOCS_SERVICE_TOKEN_FILE, 'utf8')).trim();
const request = http.get({ socketPath: process.env.CREWSHIP_DOCS_SOCKET, path: '/health/ready', headers: { 'X-Crewship-Docs-Service': secret } }, response => {
  response.resume(); response.on('end', () => process.exit(response.statusCode === 200 ? 0 : 1));
});
request.setTimeout(12000, () => request.destroy());
request.on('error', () => process.exit(1));
