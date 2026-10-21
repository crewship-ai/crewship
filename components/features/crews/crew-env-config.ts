/**
 * Read and edit a crew's environment the way the Environment cards need it:
 * one feature, one pinned tool or the security block at a time, without
 * losing anything else in devcontainer_config / mise_config (the image, the
 * other features, unmodelled keys, the mise env).
 */
import {
  buildDevcontainerJSON,
  buildMiseJSON,
  isVisualMiseConfig,
  parseDevcontainerFull,
  parseMiseConfig,
  type ParsedDevcontainer,
} from "./runtime-config-data"
import type { SecurityConfigValue } from "./runtime-security-config"

export interface EnvSummary {
  image: string
  features: string[]
  tools: Array<[string, string]>
  env: Array<[string, string]>
  /** False when mise_config is something the visual editor cannot read (TOML, …). */
  miseVisual: boolean
}

export function envSummary(devcontainerConfig: string, miseConfig: string): EnvSummary {
  const dc = parseDevcontainerFull(devcontainerConfig)
  return {
    image: dc.image,
    features: Object.keys(dc.features),
    tools: Object.entries(parseMiseConfig(miseConfig)),
    env: Object.entries(dc.containerEnv),
    miseVisual: isVisualMiseConfig(miseConfig),
  }
}

function rebuild(dc: ParsedDevcontainer): string {
  return buildDevcontainerJSON(dc.image, dc.features, {
    containerEnv: dc.containerEnv,
    privileged: dc.privileged,
    init: dc.init,
    capAdd: dc.capAdd,
    mounts: dc.mounts,
    postStartCommand: dc.postStartCommand,
    passthrough: dc.passthrough,
  })
}

export function withoutFeature(devcontainerConfig: string, ref: string): string {
  const dc = parseDevcontainerFull(devcontainerConfig)
  const features = { ...dc.features }
  delete features[ref]
  return rebuild({ ...dc, features })
}

export function withoutTool(miseConfig: string, tool: string): string {
  if (!isVisualMiseConfig(miseConfig)) return miseConfig
  const tools = { ...parseMiseConfig(miseConfig) }
  delete tools[tool]
  return buildMiseJSON(tools, miseConfig)
}

export function envSecurity(devcontainerConfig: string): SecurityConfigValue {
  const dc = parseDevcontainerFull(devcontainerConfig)
  return {
    privileged: dc.privileged,
    init: dc.init,
    capAdd: dc.capAdd,
    mounts: dc.mounts,
    containerEnv: dc.containerEnv,
    postStartCommand: dc.postStartCommand,
  }
}

export function withSecurity(devcontainerConfig: string, security: SecurityConfigValue): string {
  const dc = parseDevcontainerFull(devcontainerConfig)
  return rebuild({ ...dc, ...security })
}
