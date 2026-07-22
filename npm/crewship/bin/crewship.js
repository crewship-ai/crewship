#!/usr/bin/env node
'use strict';

// `crewship` npm shim.
//
// Resolves the platform-specific binary that npm installed as an
// optionalDependency and runs it as a child process, forwarding argv, stdio,
// signals and the exit status. Node has no execve(2) binding, so a child
// process is the closest we get to a transparent exec.

const fs = require('fs');
const { spawn } = require('child_process');
const { resolveBinaryPath, BinaryNotFoundError } = require('../lib/platform');

// Signals worth forwarding. SIGKILL/SIGSTOP cannot be caught; the rest either
// terminate the daemon cleanly (`crewship start` traps SIGINT/SIGTERM) or are
// window/terminal events the child cares about.
const FORWARDED_SIGNALS = ['SIGINT', 'SIGTERM', 'SIGHUP', 'SIGQUIT', 'SIGBREAK'];

function ensureExecutable(binPath) {
  // npm preserves the executable bit inside package tarballs, but a few
  // environments (some CI caches, `npm pack` round-trips through tools that
  // normalise modes, Windows-authored artifacts) lose it. Restoring it is
  // cheap and turns a cryptic EACCES into a working install.
  try {
    fs.accessSync(binPath, fs.constants.X_OK);
  } catch (_err) {
    try {
      fs.chmodSync(binPath, 0o755);
    } catch (_chmodErr) {
      // Read-only install dir: let the spawn below produce the real error.
    }
  }
}

function main() {
  let binPath;
  try {
    binPath = resolveBinaryPath();
  } catch (err) {
    if (err instanceof BinaryNotFoundError) {
      process.stderr.write(`${err.message}\n`);
      process.exit(1);
    }
    throw err;
  }

  ensureExecutable(binPath);

  const child = spawn(binPath, process.argv.slice(2), {
    stdio: 'inherit',
    // Stay in the parent's process group so an interactive Ctrl-C reaches the
    // Go process directly; the explicit forwarding below covers programmatic
    // kills sent to the shim's PID only.
    windowsHide: false,
  });

  const forward = (signal) => {
    if (child.killed || child.exitCode !== null) return;
    try {
      child.kill(signal);
    } catch (_err) {
      // Child already gone.
    }
  };

  const handlers = new Map();
  for (const signal of FORWARDED_SIGNALS) {
    const handler = () => forward(signal);
    handlers.set(signal, handler);
    try {
      process.on(signal, handler);
    } catch (_err) {
      // Signal unsupported on this platform (e.g. SIGBREAK off Windows).
      handlers.delete(signal);
    }
  }

  const cleanup = () => {
    for (const [signal, handler] of handlers) {
      process.removeListener(signal, handler);
    }
  };

  child.on('error', (err) => {
    cleanup();
    process.stderr.write(`crewship: failed to run ${binPath}: ${err.message}\n`);
    process.exit(1);
  });

  child.on('exit', (code, signal) => {
    cleanup();
    if (signal) {
      // Re-raise so the parent shell observes the same "killed by signal"
      // status the real binary would have produced (128+n), rather than a
      // synthetic exit code.
      process.kill(process.pid, signal);
      return;
    }
    process.exit(code === null ? 1 : code);
  });
}

main();
