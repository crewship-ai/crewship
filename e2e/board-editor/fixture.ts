import { createServer, type Plugin } from 'vite'
import { resolve } from 'node:path'
export async function startBoardFixture(options: { product?: boolean; imageUpload?: (bytes: Buffer) => object } = {}) {
  const plugins: Plugin[] = options.product ? [...(await import("@vitejs/plugin-react")).default()] : []
  const imageSink: Plugin = { name: "board-image-fixture", configureServer(server) {
    if (options.imageUpload) server.middlewares.use(async (request, response, next) => {
    if (request.method !== 'POST' || new URL(request.url ?? '/', 'http://fixture.test').pathname !== '/api/v1/pages/design/board/files') return next()
    // Real bounded HTTP bytes, independent of browser automation's body
    // introspection (WebKit does not expose Blob/File bodies there).
    const chunks: Buffer[] = []; let length = 0
    try {
      for await (const chunk of request) { const bytes = Buffer.from(chunk); length += bytes.length; if (length > 5 * 1024 * 1024) { response.writeHead(413); response.end(); return }; chunks.push(bytes) }
      const result = options.imageUpload!(Buffer.concat(chunks))
      response.writeHead(201, { 'Content-Type': 'application/json' }); response.end(JSON.stringify(result))
    } catch { response.writeHead(500); response.end(JSON.stringify({ error: 'Fixture image sink failed' })) }
  })
  } }
  plugins.push(imageSink)
  const server = await createServer({ configFile: false, plugins, resolve: { alias: [{ find: 'next/dynamic', replacement: resolve('e2e/board-editor/product-loader.tsx') }, { find: '@', replacement: resolve('.') }] }, root: resolve('e2e/board-editor'), publicDir: resolve('e2e/board-editor/public'), server: { host: '127.0.0.1', port: 0, strictPort: false, fs: { allow: [resolve('.')] } } })
  await server.listen()
  return { url: server.resolvedUrls!.local[0], close: () => server.close() }
}
