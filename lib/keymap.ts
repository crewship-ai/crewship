export const KEYS = {
  palette: "mod+k",
  search: "mod+f",
  exportConv: "mod+e",
  newSession: "mod+n",
  prevSession: "mod+shift+j",
  nextSession: "mod+shift+l",
  toggleSlash: "mod+/",
  closeOverlay: "esc",
  toggleDrawer: "mod+b",
  focusComposer: "mod+i",
} as const;

export type KeymapKey = keyof typeof KEYS;
