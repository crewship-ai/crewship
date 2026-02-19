import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'happy-dom',
    globals: true,
    setupFiles: ['./vitest.setup.ts'],
    exclude: ['node_modules', 'e2e'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json', 'html', 'lcov'],
      exclude: [
        'node_modules/',
        '.next/',
        'dist/',
        '**/*.d.ts',
        '**/*.config.*',
        '**/mockData',
        'tests/',
        'scripts/',
        'prisma/',
        'cmd/',
        'internal/',
        'public/',
        'app/**/*.tsx', // Exclude React components for now (Phase 3)
        'components/**/*.tsx',
        'components/**/hooks/*.ts', // Exclude React hooks (require integration tests)
        'lib/logger.ts', // Infrastructure - tested via integration
        'lib/rate-limit.ts', // Infrastructure - tested via integration
        'lib/api/validation.ts', // Middleware - tested via API route tests
        '**/index.ts', // Barrel files
      ],
      // Coverage thresholds - CI will fail if below these
      thresholds: {
        lines: 75,
        functions: 75,
        branches: 65,
        statements: 75,
      },
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './'),
    },
  },
})
