import globals from "globals";
import typescriptEslint from "@typescript-eslint/eslint-plugin";
import tsParser from "@typescript-eslint/parser";
import reactHooksPlugin from "eslint-plugin-react-hooks";

// eslint-plugin-react was previously loaded only to turn off two defaults
// (react-in-jsx-scope, prop-types). It's effectively a no-op here, and as of
// 7.37 still hasn't declared eslint ^10 in its peerDeps, so dropping it is the
// cleanest way through the eslint 10 bump.
export default [
  {
    files: ["**/*.{js,mjs,cjs,ts,jsx,tsx}"],
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node,
        ...globals.es2021,
      },
      parser: tsParser,
      parserOptions: {
        ecmaVersion: "latest",
        sourceType: "module",
        ecmaFeatures: {
          jsx: true,
        },
      },
    },
    plugins: {
      "@typescript-eslint": typescriptEslint,
      "react-hooks": reactHooksPlugin,
    },
    rules: {
      // TypeScript rules (relaxed for gradual adoption)
      "@typescript-eslint/no-unused-vars": ["warn", {
        argsIgnorePattern: "^_",
        varsIgnorePattern: "^_",
        caughtErrorsIgnorePattern: "^_"
      }],
      "@typescript-eslint/no-explicit-any": "warn",

      // React Hooks rules
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",

      // Force same-origin /api/* calls through apiFetch (lib/api-fetch.ts):
      // single-flight 401->refresh, cross-tab session-expired broadcast, and
      // the same-origin/CSRF guard live there. A bare `fetch("/api/...")`
      // silently bypasses all of it. New bypasses are caught here.
      //
      // Legitimate exceptions (auth/pre-session flows where a 401 is
      // meaningful rather than "session expired", and request/response
      // streaming that the refresh-retry would break) must opt out with an
      // explicit `// eslint-disable-next-line no-restricted-syntax -- <reason>`
      // on the offending fetch call.
      "no-restricted-syntax": [
        "error",
        {
          selector:
            "CallExpression[callee.name='fetch'] > Literal.arguments[value=/^\\/api\\//]",
          message:
            "Route same-origin /api/* calls through apiFetch (@/lib/api-fetch) instead of bare fetch(). If this must stay on raw fetch (auth flow / streaming), add `// eslint-disable-next-line no-restricted-syntax -- <reason>`.",
        },
        {
          selector:
            "CallExpression[callee.name='fetch'] > TemplateLiteral.arguments > TemplateElement.quasis:first-child[value.raw=/^\\/api\\//]",
          message:
            "Route same-origin /api/* calls through apiFetch (@/lib/api-fetch) instead of bare fetch(). If this must stay on raw fetch (auth flow / streaming), add `// eslint-disable-next-line no-restricted-syntax -- <reason>`.",
        },
        {
          selector:
            "CallExpression[callee.property.name='fetch'] > Literal.arguments[value=/^\\/api\\//]",
          message:
            "Route same-origin /api/* calls through apiFetch (@/lib/api-fetch) instead of bare fetch(). If this must stay on raw fetch (auth flow / streaming), add `// eslint-disable-next-line no-restricted-syntax -- <reason>`.",
        },
      ],
    },
  },
  {
    ignores: [
      ".next/**",
      "out/**",
      "web/out/**",
      "node_modules/**",
      "dist/**",
      "build/**",
      "coverage/**",
      "*.config.{js,ts}",
      "public/**",
      "lib/generated/**",
      ".claude/**",
      "e2e/**",
    ],
  },
];
