// The stop-loop guarantee is verified for desktop Chromium. iOS browser names
// do not identify Chromium engines, and mobile process isolation is not covered.
export function supportsPageApplications(userAgent: string): boolean {
  return /(?:Chrome|Chromium)\/\d+/.test(userAgent)
    && !/Android|Mobile|iPhone|iPad|iPod|CriOS|FxiOS/i.test(userAgent)
}

export const pageBrowserRequirement = "Custom Page applications require desktop Chrome or Edge. You can still use the Page panels in this browser."
