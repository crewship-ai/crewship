import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { confirmNativeInitialized } from "../craft-native-initialize"

// The very file internal/api/testdata/craft_native_initialize_response.json that a Go test ties to the server's struct.
const golden = JSON.parse(readFileSync(join(__dirname, "../../internal/api/testdata/craft_native_initialize_response.json"), "utf8"))

describe("confirmNativeInitialized", () => {
  it("accepts the shape the server sends, for initialize and for the initialize from an import", () => {
    expect(confirmNativeInitialized(golden, "gridcraft", "pg_1")).toEqual({ sessionToken: golden.session_token, generation: "7" })
  })
  it("never confirms another application, another page or another version", () => {
    expect(confirmNativeInitialized(golden, "wordcraft", "pg_1")).toBeNull()
    expect(confirmNativeInitialized(golden, "gridcraft", "pg_2")).toBeNull()
    expect(confirmNativeInitialized({ ...golden, version: "2" }, "gridcraft", "pg_1")).toBeNull()
  })
  it("never confirms a malformed token or generation, or a missing field", () => {
    for (const bad of [{ session_token: "short" }, { session_token: "G".repeat(64) }, { generation: "0" }, { generation: "07" }, { generation: 7 }, { version: undefined }]) {
      expect(confirmNativeInitialized({ ...golden, ...bad }, "gridcraft", "pg_1")).toBeNull()
    }
    expect(confirmNativeInitialized(null, "gridcraft", "pg_1")).toBeNull()
    expect(confirmNativeInitialized([golden], "gridcraft", "pg_1")).toBeNull()
  })
})
