/** Server projection of declared recipe rules; never infer historic permissions. */
export interface RoutineStepBehavior {
  id: string
  name: string
  performer: string
  checks: string[]
  failure: string
  attempts: string
  timeout: string
}
export interface RoutineBehavior {
  steps: RoutineStepBehavior[]
  cost: string
  scope: string
}
