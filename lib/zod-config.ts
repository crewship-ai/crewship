import { config } from "zod"

// Zod probes `new Function` before compiling fast parsers. The web app's CSP
// refuses eval, so the probe could only fail and report a CSP violation on
// every page. Parsing behaves the same in the interpreted mode.
config({ jitless: true })
