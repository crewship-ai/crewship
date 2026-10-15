import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { render } from './render.mjs';
import { hash } from './model.mjs';
export async function startEditor(sessions) {
  const server = createServer(async (req, res) => {
    try {
      res.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; frame-src 'self' about:; connect-src 'self'; base-uri 'none'; form-action 'none'");
      if (req.url === '/' && req.method === 'GET') { res.setHeader('Content-Type', 'text/html'); res.end(await readFile(new URL('./editor.html', import.meta.url))); return; }
      if (req.url === '/ui.mjs' && req.method === 'GET') { res.setHeader('Content-Type', 'text/javascript'); res.end(await readFile(new URL('./ui.mjs', import.meta.url))); return; }
      const name = req.url.slice(1); const session = sessions[name]; if (!session) { res.writeHead(404).end(); return; }
      if (req.method === 'POST') {
        // Test fixture only; same-origin host editor sends CAS operations, author content has no scripts.
        if (req.headers.origin !== `http://${req.headers.host}`) { res.writeHead(403).end(); return; }
        let body = ''; for await (const chunk of req) { body += chunk; if (Buffer.byteLength(body) > 65536) throw new Error('request limit'); }
        session.patch(JSON.parse(body));
      } else if (req.method !== 'GET') { res.writeHead(405).end(); return; }
      const deck = session.read(); res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ deck, targetHash: hash(deck.slides[0].elements[0]), previews: deck.slides.map(s => render(deck, s)) }));
    } catch (error) { res.writeHead(error.name === 'Conflict' ? 409 : 400, { 'Content-Type': 'application/json' }).end(JSON.stringify({ error: error.message })); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return { url: `http://127.0.0.1:${server.address().port}`, close: () => new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve())) };
}
