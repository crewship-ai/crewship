"use client";

import { useEffect, useMemo, useReducer } from "react";
import { cjk } from "@streamdown/cjk";
import { code } from "@streamdown/code";
import { math } from "@streamdown/math";

// Mermaid is by far the heaviest streamdown plugin (it drags the whole
// mermaid renderer, ~500 KB+ into the chat bundle) and the overwhelming
// majority of assistant messages contain no diagram. It therefore loads
// lazily, only when the content actually contains a ```mermaid fence;
// until the chunk arrives the fence renders as a plain code block and
// upgrades in place.

// eslint-disable-next-line @typescript-eslint/no-explicit-any -- mermaid peer dep version mismatch between @streamdown/mermaid and streamdown
const basePlugins = { cjk, code, math } as any;

let mermaidPlugin: unknown | null = null;
let mermaidLoading: Promise<void> | null = null;

function loadMermaid(): Promise<void> {
  if (!mermaidLoading) {
    mermaidLoading = import("@streamdown/mermaid")
      .then((m) => {
        mermaidPlugin = m.mermaid;
      })
      .catch(() => {
        // Chunk fetch failed (offline, deploy skew) — allow a retry on
        // the next mermaid-bearing message.
        mermaidLoading = null;
      });
  }
  return mermaidLoading;
}

/**
 * Streamdown plugin set for a given markdown string: always cjk/code/
 * math; mermaid joins lazily when (and only when) the content carries a
 * ```mermaid fence. Bumps a local reducer when the mermaid chunk lands
 * so the consuming component re-renders and the diagram appears.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any -- plugin map shape comes from streamdown's untyped plugin contract
export function useStreamdownPlugins(content: string | undefined): any {
  const needsMermaid =
    typeof content === "string" && content.includes("```mermaid");
  const mermaidReady = mermaidPlugin !== null;
  const [, bump] = useReducer((x: number) => x + 1, 0);

  useEffect(() => {
    if (!needsMermaid || mermaidPlugin) return;
    let live = true;
    void loadMermaid().then(() => {
      if (live) bump();
    });
    return () => {
      live = false;
    };
  }, [needsMermaid]);

  return useMemo(
    () =>
      needsMermaid && mermaidPlugin
        ? { ...basePlugins, mermaid: mermaidPlugin }
        : basePlugins,
    // eslint-disable-next-line react-hooks/exhaustive-deps -- mermaidReady is the memo trigger for the module-level cache
    [needsMermaid, mermaidReady]
  );
}
