import { createServer } from 'vite'
import { resolve } from 'node:path'
export async function startBoardFixture() {
  const server = await createServer({ configFile: false, root: resolve('e2e/board-editor'), publicDir: resolve('e2e/board-editor/public'), server: { host: '127.0.0.1', port: 0, strictPort: false, fs: { allow: [resolve('.')] } } })
  await server.listen()
  return { url: server.resolvedUrls!.local[0], close: () => server.close() }
}
