import type { InstanceGovRow } from "./use-instance-keeper"
import { accessLabel } from "./security-model"

/** The check cadence an untouched workspace runs on (governance.DefaultBehaviorSampleEvery). */
export const SAMPLE_DEFAULT = 5

/** "1 in 5": how often the watchdog looks at a tool call. */
export const checkLabel = (r: Pick<InstanceGovRow, "behavior_sample_every">) => `1 in ${r.behavior_sample_every || SAMPLE_DEFAULT}`

/** A workspace's watch in a few words, for the panel and a phone row. */
export const watchSummary = (r: Pick<InstanceGovRow, "enabled" | "behavior_sample_every">) =>
  r.enabled ? `Watchdog on · ${checkLabel(r)}` : "Watchdog off"

/** Which judge a workspace actually uses: "Instance" or "Its own", and the detail. */
export const judgeKind = (r: Pick<InstanceGovRow, "gov_model_provider">) => (r.gov_model_provider ? "Its own" : "Instance")
export const judgeDetail = (r: Pick<InstanceGovRow, "gov_model_provider" | "gov_model_id">) =>
  r.gov_model_provider ? `${r.gov_model_provider} · ${r.gov_model_id}` : "The instance judge decides"

export { accessLabel }
