import { describe, it, expect } from "vitest"
import {
  parseDevcontainerFull,
  buildDevcontainerJSON,
  normalizeCap,
  isKnownCap,
  isAllowedMountSource,
  KNOWN_CAPS,
} from "../runtime-config-data"

// Round-trip coverage for the structured container-privilege controls
// (#1380): privileged / capAdd / mounts / init / containerEnv / start hook
// serialize to the exact devcontainer_config JSON the backend accepts, and
// any top-level key the UI does not model survives the round-trip verbatim.

describe("parseDevcontainerFull", () => {
  it("defaults every field for empty input", () => {
    expect(parseDevcontainerFull("")).toEqual({
      image: "debian:bookworm-slim",
      features: {},
      containerEnv: {},
      privileged: false,
      init: false,
      capAdd: [],
      mounts: [],
      postStartCommand: "",
      passthrough: {},
    })
  })

  it("defaults every field for malformed JSON", () => {
    const parsed = parseDevcontainerFull("{not json")
    expect(parsed.image).toBe("debian:bookworm-slim")
    expect(parsed.privileged).toBe(false)
  })

  it("extracts the privilege fields", () => {
    const cfg = JSON.stringify({
      image: "img:1",
      privileged: true,
      init: true,
      capAdd: ["NET_BIND_SERVICE", "SYS_ADMIN"],
      containerEnv: { FOO: "bar" },
      mounts: [
        { source: "/dev/fuse", target: "/dev/fuse", type: "bind" },
        { source: "myvol", target: "/data", type: "volume", readonly: true },
      ],
      postStartCommand: "echo hi",
    })
    const parsed = parseDevcontainerFull(cfg)
    expect(parsed.privileged).toBe(true)
    expect(parsed.init).toBe(true)
    expect(parsed.capAdd).toEqual(["NET_BIND_SERVICE", "SYS_ADMIN"])
    expect(parsed.containerEnv).toEqual({ FOO: "bar" })
    expect(parsed.mounts).toEqual([
      { source: "/dev/fuse", target: "/dev/fuse", type: "bind", readonly: false },
      { source: "myvol", target: "/data", type: "volume", readonly: true },
    ])
    expect(parsed.postStartCommand).toBe("echo hi")
  })

  it("normalizes a postStartCommand array into newline-joined text", () => {
    const cfg = JSON.stringify({ image: "img:1", postStartCommand: ["a", "b"] })
    expect(parseDevcontainerFull(cfg).postStartCommand).toBe("a\nb")
  })

  it("preserves unmodeled top-level keys in passthrough (escape hatch)", () => {
    const cfg = JSON.stringify({
      image: "img:1",
      remoteUser: "agent",
      securityOpt: ["seccomp=unconfined"],
      postCreateCommand: "npm i",
    })
    const parsed = parseDevcontainerFull(cfg)
    expect(parsed.passthrough).toEqual({
      remoteUser: "agent",
      securityOpt: ["seccomp=unconfined"],
      postCreateCommand: "npm i",
    })
  })
})

describe("buildDevcontainerJSON with security extras", () => {
  it("omits every security key when nothing is set", () => {
    const out = buildDevcontainerJSON("img:1", {})
    expect(JSON.parse(out)).toEqual({ image: "img:1" })
  })

  it("emits privilege fields only when truthy / non-empty", () => {
    const out = buildDevcontainerJSON(
      "img:1",
      {},
      {
        privileged: true,
        init: false,
        capAdd: ["NET_BIND_SERVICE"],
        containerEnv: { A: "1" },
        mounts: [{ source: "/dev/fuse", target: "/dev/fuse", readonly: false }],
        postStartCommand: "echo hi",
      },
    )
    expect(JSON.parse(out)).toEqual({
      image: "img:1",
      containerEnv: { A: "1" },
      privileged: true,
      capAdd: ["NET_BIND_SERVICE"],
      mounts: [{ source: "/dev/fuse", target: "/dev/fuse", type: "bind" }],
      postStartCommand: "echo hi",
    })
  })

  it("round-trips a full config through parse -> build -> parse", () => {
    const original = {
      image: "img:1",
      privileged: true,
      init: true,
      capAdd: ["NET_BIND_SERVICE"],
      containerEnv: { X: "y" },
      mounts: [{ source: "/dev/fuse", target: "/dev/fuse", type: "bind", readonly: false }],
      postStartCommand: "echo hi",
    }
    const parsed = parseDevcontainerFull(JSON.stringify(original))
    const rebuilt = buildDevcontainerJSON(parsed.image, parsed.features, {
      privileged: parsed.privileged,
      init: parsed.init,
      capAdd: parsed.capAdd,
      containerEnv: parsed.containerEnv,
      mounts: parsed.mounts,
      postStartCommand: parsed.postStartCommand,
      passthrough: parsed.passthrough,
    })
    expect(parseDevcontainerFull(rebuilt)).toEqual(parsed)
  })

  it("re-emits passthrough keys the UI does not model", () => {
    const out = buildDevcontainerJSON("img:1", {}, {
      passthrough: { remoteUser: "agent", securityOpt: ["seccomp=unconfined"] },
    })
    expect(JSON.parse(out)).toEqual({
      image: "img:1",
      remoteUser: "agent",
      securityOpt: ["seccomp=unconfined"],
    })
  })
})

describe("capability helpers", () => {
  it("normalizes casing and the CAP_ prefix", () => {
    expect(normalizeCap("cap_net_bind_service")).toBe("NET_BIND_SERVICE")
    expect(normalizeCap("  sys_admin ")).toBe("SYS_ADMIN")
  })

  it("recognizes known caps and rejects gibberish", () => {
    expect(isKnownCap("NET_BIND_SERVICE")).toBe(true)
    expect(isKnownCap("cap_sys_admin")).toBe(true)
    expect(isKnownCap("NOT_A_CAP")).toBe(false)
  })

  it("KNOWN_CAPS includes NET_BIND_SERVICE and flags dangerous caps", () => {
    const names = KNOWN_CAPS.map((c) => c.name)
    expect(names).toContain("NET_BIND_SERVICE")
    const sysAdmin = KNOWN_CAPS.find((c) => c.name === "SYS_ADMIN")
    expect(sysAdmin?.danger).toBe(true)
  })
})

describe("mount source allowlist (mirrors internal/devcontainer/mount_validate.go)", () => {
  it("allows /dev/fuse and valid named volumes", () => {
    expect(isAllowedMountSource("/dev/fuse")).toBe(true)
    expect(isAllowedMountSource("my-vol.1")).toBe(true)
  })

  it("rejects the docker socket and arbitrary host paths", () => {
    expect(isAllowedMountSource("/var/run/docker.sock")).toBe(false)
    expect(isAllowedMountSource("/etc/passwd")).toBe(false)
    expect(isAllowedMountSource("/tmp")).toBe(false)
    expect(isAllowedMountSource("../../etc")).toBe(false)
    expect(isAllowedMountSource("")).toBe(false)
  })
})
