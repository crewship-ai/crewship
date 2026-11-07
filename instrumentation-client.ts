// Next.js evaluates this file before any application module. It has to stay
// first: Zod decides at schema construction whether to probe eval, so route
// chunks that build schemas at import time must see this configuration.
import "@/lib/zod-config"
