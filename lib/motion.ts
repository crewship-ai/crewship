import type { Transition } from "motion/react";

/**
 * Returns true when the user has requested reduced motion at the OS level.
 * Use to gate animation distance / stagger / spring physics.
 *
 * SSR-safe: returns false on the server.
 */
export function prefersReducedMotion(): boolean {
  if (typeof window === "undefined") return false;
  return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
}

export const spring = {
  snappy: { type: "spring", stiffness: 600, damping: 40 } satisfies Transition,
  smooth: { type: "spring", stiffness: 280, damping: 32 } satisfies Transition,
  gentle: { type: "spring", stiffness: 180, damping: 26 } satisfies Transition,
  bouncy: { type: "spring", stiffness: 400, damping: 18 } satisfies Transition,
} as const;

export const ease = {
  out: [0.16, 1, 0.3, 1] as const,
  in: [0.7, 0, 0.84, 0] as const,
  inOut: [0.65, 0, 0.35, 1] as const,
} as const;

export const duration = {
  micro: 0.12,
  short: 0.18,
  base: 0.24,
  long: 0.36,
} as const;

export const arrival = {
  initial: { opacity: 0, y: 16 },
  animate: { opacity: 1, y: 0 },
  exit: { opacity: 0, y: -8 },
  transition: spring.smooth,
} as const;

export const stagger = {
  parts: { staggerChildren: 0.03 },
  steps: { staggerChildren: 0.08 },
  chips: { staggerChildren: 0.05 },
} as const;

// Right/left side detail panel. Spring values lifted verbatim from the
// original trace-side-panel implementation that the team already vetted.
export const panel = {
  side: {
    initial: { x: 360, opacity: 0 },
    animate: { x: 0, opacity: 1 },
    exit: { x: 360, opacity: 0 },
    transition: { type: "spring" as const, damping: 28, stiffness: 320 },
  },
  sideLeft: {
    initial: { x: -360, opacity: 0 },
    animate: { x: 0, opacity: 1 },
    exit: { x: -360, opacity: 0 },
    transition: { type: "spring" as const, damping: 28, stiffness: 320 },
  },
} as const;

// Animated underline indicator for TabBar. Caller supplies the layoutId
// (or uses the default below) so multiple tab groups on one page get
// independent indicators.
export const tabIndicator = {
  layoutId: "tab-indicator",
  transition: spring.smooth,
} as const;

// motion.li / motion.div used by ListRow — `layout` makes selection
// transitions slide rather than snap when adjacent rows reflow.
export const listRow = {
  layout: true as const,
  transition: spring.snappy,
} as const;
