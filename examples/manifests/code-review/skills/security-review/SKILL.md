---
name: security-review
description: Checklist-driven security review pass for pull requests.
license: MIT
---

# Security Review

When asked to review a pull request, walk through the following
checklist and report findings inline:

1. **Input validation** — any user-controlled input that reaches a
   shell, SQL query, file path, or URL must be either escaped or
   parameterised. Flag any concatenation.
2. **AuthN/AuthZ** — every new endpoint must consult the same auth
   middleware as its peers. Routes registered without a guard are
   defects.
3. **Secrets in code** — flag any string that looks like an API key,
   token, or certificate. Suggest moving to the credentials vault.
4. **Dependency pins** — flag bumps that widen version ranges or
   downgrade transitive deps.
5. **Logging** — assert that credentials, PII, and full request
   bodies are NOT logged. Flag any `log` / `console.log` of headers,
   query params, or environment dumps.

Output format: a bulleted list grouped by severity (HIGH / MEDIUM /
LOW). For each finding include the file + line range and a one-line
suggested fix.
