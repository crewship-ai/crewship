import type { Transition } from "motion/react";

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
