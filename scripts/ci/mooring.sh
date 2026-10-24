#!/usr/bin/env bash
# Run from the product checkout root with Rust + wasm32-unknown-unknown,
# Node and the pinned wasm-bindgen CLI on PATH. No network dependency fetches.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"
if [[ $(wasm-bindgen --version) != 'wasm-bindgen 0.2.129' ]]; then
  echo 'Mooring CI requires wasm-bindgen 0.2.129' >&2
  exit 1
fi
output=$(mktemp -d "${TMPDIR:-/tmp}/mooring-ci.XXXXXXXX")
trap 'rm -rf -- "$output"' EXIT

cargo test --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --workspace --locked --offline
cargo build --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline --release --target wasm32-unknown-unknown -p mooring-client
cargo build --manifest-path mooring/Cargo.toml --config mooring/.cargo/config.toml --locked --offline --release --target wasm32-unknown-unknown -p mooring-wordcraft-client
wasm-bindgen "${CARGO_TARGET_DIR:-mooring/target}/wasm32-unknown-unknown/release/mooring_client.wasm" --target nodejs --out-dir "$output/node"
node mooring/client/test-wasm.cjs "$output/node/mooring_client.js"
