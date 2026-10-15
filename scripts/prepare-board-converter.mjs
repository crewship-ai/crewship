import { build } from 'vite'
import path from 'node:path'
const production = process.argv.includes('--production')
await build({ configFile: false, publicDir: false, logLevel: 'warn', define: { 'process.env.NODE_ENV': JSON.stringify('production') }, build: { lib: { entry: path.resolve('lib/boards/converter-runner.ts'), name: 'CrewshipBoardConverter', formats: ['iife'], fileName: () => 'runner.js' }, outDir: path.resolve(production ? 'public/board-converter' : 'e2e/board-editor/public/board-converter'), emptyOutDir: true, rollupOptions: { output: { inlineDynamicImports: true } }, minify: true } })
