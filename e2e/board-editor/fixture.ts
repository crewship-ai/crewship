import { createServer } from 'vite'
import { resolve } from 'node:path'
export async function startBoardFixture(options: { product?: boolean } = {}) {
  const plugins = options.product ? [(await import("@vitejs/plugin-react")).default()] : []
  const server = await createServer({ configFile: false, plugins, resolve: { alias: [{ find: 'next/dynamic', replacement: resolve('e2e/board-editor/product-loader.tsx') }, { find: '@', replacement: resolve('.') }] }, root: resolve('e2e/board-editor'), publicDir: resolve('e2e/board-editor/public'), server: { host: '127.0.0.1', port: 0, strictPort: false, fs: { allow: [resolve('.')] } } })
  await server.listen()
  return { url: server.resolvedUrls!.local[0], close: () => server.close() }
}
