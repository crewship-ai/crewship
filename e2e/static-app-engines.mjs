// Engine selection for the static App browser harness. Fails closed: a set but
// empty or misspelled STATIC_APP_ENGINES must never narrow the run to nothing.

// `available` are engine names; undefined means "not set" and selects them all.
export function selectEngines(value, available) {
  if (value === undefined) return [...available]
  const requested = value.split(",").map(name => name.trim()).filter(Boolean)
  if (requested.length === 0) {
    throw new Error(`STATIC_APP_ENGINES is set but empty; unset it for all engines or use a list from: ${available.join(", ")}`)
  }
  const unknown = requested.filter(name => !available.includes(name))
  if (unknown.length) {
    throw new Error(`STATIC_APP_ENGINES has unknown engine(s): ${unknown.join(", ")}; allowed: ${available.join(", ")}`)
  }
  return available.filter(name => requested.includes(name))
}

// A run that executed no browser proves nothing and must not report success.
export function requireBrowserRuns(count) {
  if (count < 1) throw new Error("Static App browser qualification executed 0 browsers; refusing to report success")
}
