#!/usr/bin/env python3
"""
Deterministic detector of documentation ↔ code drift.

It looks for TWO directions of drift — both have bitten us:

  PHANTOM  — the documentation describes a configuration key the code does
             not have. (devcontainers.mdx described a feature-registry
             allowlist that exists nowhere in internal/devcontainer.)
  GAP      — the code has a YAML/JSON field the documentation never mentions.
             (manifest-schema.mdx did not know files:, composio or
             notification_channels a month after they landed in the schema.)

It is not a substitute for reading — it is a cheap filter that hands an agent
concrete candidates instead of "go through a thousand lines". The agent then
decides which candidate is a real defect and which is just another name for
the same thing.

Usage:
    python3 docs_drift.py --doc docs/configuration/manifest-schema.mdx \
                          --pkg internal/manifest --pkg internal/api
    python3 docs_drift.py --doc X --pkg Y --format md   # human-readable report

Output: JSON (default) or markdown.

The exit code is ALWAYS 0. In a routine `script` step a non-zero exit means
the step failed, so "I found drift" would abort the run instead of reporting
it. The state is in the `drift` field. For shell/CI pass `--exit-code`.
"""
import argparse
import json
import os
import re
import sys

# A key from a Go struct tag: `yaml:"network_mode,omitempty"` -> network_mode
TAG = re.compile(r'(?:yaml|json):"([a-z0-9_]+)[",]')
# An identifier in the documentation: `network_mode` in backticks or in a
# YAML block.
BACKTICK = re.compile(r"`([a-z][a-z0-9_]{3,40})`")
YAML_KEY = re.compile(r"^\s{0,12}([a-z][a-z0-9_]{3,40}):", re.M)

# Words that look like a configuration key but are not — common English and
# technical words that appear in backticks in prose.
STOPLIST = {
    "true", "false", "null", "none", "string", "integer", "number", "boolean",
    "array", "object", "list", "name", "type", "kind", "spec", "metadata",
    "note", "warning", "example", "default", "value", "text", "json", "yaml",
    "bash", "shell", "http", "https", "curl", "make", "test", "main", "this",
    "that", "when", "then", "else", "with", "from", "into", "over", "each",
}


def keys_from_code(paths):
    """Every yaml/json tag in .go files under the given paths."""
    found = {}
    for root in paths:
        if os.path.isfile(root):
            files = [root]
        else:
            files = []
            for dirpath, _, names in os.walk(root):
                files += [os.path.join(dirpath, n) for n in names
                          if n.endswith(".go") and not n.endswith("_test.go")]
        for path in files:
            try:
                with open(path, encoding="utf-8", errors="replace") as f:
                    for i, line in enumerate(f, 1):
                        for m in TAG.finditer(line):
                            k = m.group(1)
                            if k not in found:
                                found[k] = f"{path}:{i}"
            except OSError:
                continue
    return found


def keys_from_docs(path):
    with open(path, encoding="utf-8", errors="replace") as f:
        text = f.read()
    found = {}
    for i, line in enumerate(text.splitlines(), 1):
        for m in BACKTICK.finditer(line):
            found.setdefault(m.group(1), i)
    for m in YAML_KEY.finditer(text):
        k = m.group(1)
        if k not in found:
            found[k] = text[:m.start()].count("\n") + 1
    return found


def assess(doc_keys, code_keys, min_length=4):
    def interesting(k):
        return (k not in STOPLIST and len(k) >= min_length
                # a key with an underscore is almost certainly configuration;
                # a single word could be anything, so it only counts when it
                # also exists in the code
                and ("_" in k or k in code_keys))

    phantoms = [
        {"key": k, "line": doc_keys[k]}
        for k in sorted(doc_keys)
        if interesting(k) and k not in code_keys
    ]
    gaps = [
        {"key": k, "source": code_keys[k]}
        for k in sorted(code_keys)
        if interesting(k) and k not in doc_keys
    ]
    return phantoms, gaps


def as_markdown(result):
    r = [f"# Docs drift — `{result['doc']}`", ""]
    r.append(f"Packages: {', '.join('`%s`' % b for b in result['packages'])}")
    r.append(f"Keys in code: {result['keys_in_code']} · "
             f"in documentation: {result['keys_in_docs']}")
    r.append("")
    if result["phantoms"]:
        r.append("## Phantoms — documented, but not in the code")
        r.append("")
        for p in result["phantoms"]:
            r.append(f"- `{p['key']}` — {result['doc']}:{p['line']}")
        r.append("")
    if result["gaps"]:
        r.append("## Gaps — in the code, documentation is silent")
        r.append("")
        for g in result["gaps"]:
            r.append(f"- `{g['key']}` — {g['source']}")
        r.append("")
    if not result["phantoms"] and not result["gaps"]:
        r.append("Clean — no drift candidate.")
    return "\n".join(r)


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--doc", required=True, help="path to a .md/.mdx page")
    ap.add_argument("--pkg", action="append", required=True,
                    help="directory or file with Go code (repeatable)")
    ap.add_argument("--min-length", type=int, default=4)
    ap.add_argument("--format", choices=["json", "md"], default="json")
    ap.add_argument("--exit-code", action="store_true",
                    help="exit 1 when drift is found (shell/CI; NOT in a routine)")
    args = ap.parse_args()

    code = keys_from_code(args.pkg)
    docs = keys_from_docs(args.doc)
    phantoms, gaps = assess(docs, code, args.min_length)

    result = {
        "doc": args.doc,
        "packages": args.pkg,
        "keys_in_code": len(code),
        "keys_in_docs": len(docs),
        "phantoms": phantoms,
        "gaps": gaps,
        "drift": len(phantoms) + len(gaps),
    }
    print(as_markdown(result) if args.format == "md"
          else json.dumps(result, ensure_ascii=False, indent=2))
    sys.exit(1 if (args.exit_code and result["drift"]) else 0)


if __name__ == "__main__":
    main()
