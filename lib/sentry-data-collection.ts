import type { init } from "@sentry/nextjs"

// Sentry 11 collects these categories by default. Crewship reports crashes;
// customer requests, AI conversations and local variables are not crash data.
// Share the explicit policy across browser, Node and Edge initialization.
export const crashDataCollection: NonNullable<Parameters<typeof init>[0]["dataCollection"]> = {
  userInfo: false,
  cookies: false,
  httpHeaders: false,
  httpBodies: [],
  urlQueryParams: false,
  genAI: { inputs: false, outputs: false },
  graphQL: { document: false, variables: false },
  databaseQueryData: false,
  queues: false,
  stackFrameVariables: false,
  frameContextLines: 0,
}
