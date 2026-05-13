// Bridge between CSS utility classes (defined in app/globals.css under
// @layer utilities) and TypeScript components. Components import the
// named constants here so they get autocomplete and so the magic strings
// live in exactly one place. Changing visual rules = edit globals.css;
// changing API = edit this file.

export const selection = {
  row: {
    default: "row-interactive row-hover",
    selected: "row-interactive row-hover row-selected",
  },
  card: {
    default: "card-interactive card-hover",
    selected: "card-interactive card-hover card-selected",
  },
} as const;

export const chip = {
  idle: "chip-idle",
  active: "chip-active",
} as const;

export type SelectionVariant = keyof typeof selection;
export type SelectionState = keyof typeof selection.row;
