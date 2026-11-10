// The part of the original dashboard host's `dash` API that this page uses,
// written over Crewship's artifact data (`crewship.data`). The page's own
// script is kept verbatim and runs against it unchanged.
//
// Datasets: each dataset is one collection of this artifact; its rows are the
// collection's documents in id order. Calcs are worked out from those rows.
// Drawings (`data-draw`) and `onData` run after the page's scripts, whenever
// data, a filter or the width changes. Filters are this viewer's own: the
// host remembers them in its browser storage (crewship.prefs) and the page
// restores them on the next visit; they never reach the shared data.
(function () {
  "use strict"
  var DATASETS = ["tasks"]
  var DEFAULT_PARAMS = { crew: "" }
  var COLORS = [1, 2, 3, 4, 5, 6, 7, 8].map(function (n) { return "var(--cds-chart-categorical-" + n + ")" })

  var loaded = {}
  var calcs = {}
  var draws = {}
  var listeners = []
  var params = Object.assign({}, DEFAULT_PARAMS)
  // Keys restored from the viewer's storage, checked against the data once it arrives.
  var restored = {}
  var started = false
  var scheduled = false

  function source(id) {
    if (Object.prototype.hasOwnProperty.call(loaded, id)) return loaded[id]
    if (DATASETS.indexOf(id) >= 0) return { status: "loading", data: [], columns: [], message: "", refreshing: false, meta: [] }
    return null
  }

  function calcResult(id, seen) {
    var def = calcs[id]
    if (seen.indexOf(id) >= 0) return { status: "error", data: [], columns: [], message: "A calc reads itself.", refreshing: false, meta: [] }
    var inputs = []
    for (var i = 0; i < def.inputs.length; i++) {
      var input = read(def.inputs[i], seen.concat(id))
      if (input.status !== "ok") return { status: input.status, data: [], columns: [], message: input.message, refreshing: false, meta: [] }
      inputs.push(input.data)
    }
    try {
      var data = def.fn.apply(null, inputs)
      return { status: "ok", data: data, columns: columnsOf(data), message: "", refreshing: false, meta: [] }
    } catch (error) {
      return { status: "error", data: [], columns: [], message: String(error && error.message || error), refreshing: false, meta: [] }
    }
  }

  function read(id, seen) {
    if (Object.prototype.hasOwnProperty.call(calcs, id)) return calcResult(id, seen || [])
    return source(id) || { status: "missing", data: [], columns: [], message: "No dataset or calc named " + id + ".", refreshing: false, meta: [] }
  }

  function columnsOf(rows) {
    if (!Array.isArray(rows) || !rows.length || typeof rows[0] !== "object" || rows[0] === null) return []
    return Object.keys(rows[0])
  }

  function stampStates() {
    var root = document.getElementById("dash-root")
    if (!root) return
    var marks = root.querySelectorAll("[data-source]")
    var any = { loading: false, error: false, empty: false }
    for (var i = 0; i < marks.length; i++) {
      var el = marks[i]
      var ids = (el.getAttribute("data-source") || "").split(/\s+/).filter(Boolean)
      var state = ""
      for (var j = 0; j < ids.length; j++) {
        var s = read(ids[j]).status
        if (s === "loading") { state = "loading"; any.loading = true }
        else if (s === "empty" && !state) { state = "empty"; any.empty = true }
        else if (s !== "ok" && s !== "empty" && state !== "loading") { state = "error"; any.error = true }
      }
      if (!state && el.tagName !== "TABLE" && el.textContent.trim() === "—" && !el.querySelector("svg")) state = "empty"
      if (state) el.setAttribute("data-dash-source-state", state)
      else el.removeAttribute("data-dash-source-state")
    }
    if (any.loading) root.setAttribute("data-dash-state", "loading")
    else if (any.error) root.setAttribute("data-dash-state", "error")
    else if (any.empty) root.setAttribute("data-dash-state", "empty")
    else root.removeAttribute("data-dash-state")
  }

  function runDraw(name, el) {
    try { draws[name](el) } catch (error) { console.error("dash.draw(\"" + name + "\")", error) }
  }

  function render() {
    scheduled = false
    if (!started) return
    for (var i = 0; i < listeners.length; i++) {
      try { listeners[i]() } catch (error) { console.error("dash.onData", error) }
    }
    var names = Object.keys(draws)
    for (var n = 0; n < names.length; n++) {
      var els = document.querySelectorAll('[data-draw="' + names[n] + '"]')
      for (var e = 0; e < els.length; e++) if (!els[e].closest("[hidden]")) runDraw(names[n], els[e])
    }
    stampStates()
  }

  function schedule() {
    if (scheduled) return
    scheduled = true
    requestAnimationFrame(render)
  }

  function load(id) {
    var api = window.crewship && window.crewship.data
    if (!api) {
      loaded[id] = { status: "error", data: [], columns: [], message: "This page needs the Crewship artifact runtime.", refreshing: false, meta: [] }
      schedule()
      return
    }
    api.get(id).then(function (result) {
      var docs = (result && result.documents || []).slice().sort(function (a, b) { return a.id < b.id ? -1 : a.id > b.id ? 1 : 0 })
      var rows = docs.map(function (d) { return d.value })
      // No rows is its own state, so calcs over it show a dash instead of 0 or NaN.
      loaded[id] = rows.length
        ? { status: "ok", data: rows, columns: columnsOf(rows), message: "", refreshing: false, meta: [] }
        : { status: "empty", data: [], columns: [], message: "No rows yet.", refreshing: false, meta: [] }
      revalidate(rows)
      schedule()
    }, function (error) {
      loaded[id] = { status: "error", data: [], columns: [], message: String(error && error.message || error), refreshing: false, meta: [] }
      schedule()
    })
  }

  // Only the page's own filter keys, with values of the default's type, and only those that differ from it.
  function prefsApi() { return window.crewship && window.crewship.prefs }
  function remember() {
    var api = prefsApi()
    if (!api) return
    var changed = {}
    for (var key in DEFAULT_PARAMS) if (params[key] !== DEFAULT_PARAMS[key]) changed[key] = params[key]
    api.set(changed).catch(function () {})
  }
  function restore(next) {
    var api = prefsApi()
    if (!api) { next(); return }
    // An older host refuses prefs.get; a host that never answers must not hold the page back either.
    var finished = false
    function done() { if (!finished) { finished = true; next() } }
    setTimeout(done, 1500)
    api.get().then(function (result) {
      if (finished) return
      var saved = result && result.value
      if (saved && typeof saved === "object") {
        for (var key in DEFAULT_PARAMS) {
          if (Object.prototype.hasOwnProperty.call(saved, key) && typeof saved[key] === typeof DEFAULT_PARAMS[key]) { params[key] = saved[key]; restored[key] = true }
        }
      }
      done()
    }, function () { done() })
  }

  // A remembered value the data no longer has (a crew renamed or removed, or
  // rows the viewer can no longer read) falls back to the default and is forgotten.
  function revalidate(rows) {
    var dropped = false
    for (var key in restored) {
      var value = params[key]
      var present = value === DEFAULT_PARAMS[key] || rows.some(function (r) { return r && r[key] === value })
      if (!present) { params[key] = DEFAULT_PARAMS[key]; dropped = true }
      delete restored[key]
    }
    if (dropped) remember()
  }

  var widths = new WeakMap()
  var observer = typeof ResizeObserver === "function" ? new ResizeObserver(function (entries) {
    var changed = false
    for (var i = 0; i < entries.length; i++) {
      var w = Math.round(entries[i].contentRect.width)
      if (widths.get(entries[i].target) !== w) { widths.set(entries[i].target, w); changed = true }
    }
    if (changed) schedule()
  }) : null

  window.dash = Object.freeze({
    data: function (id) { return read(id) },
    calc: function (id, def) { calcs[id] = { inputs: def.inputs || [], fn: def.fn, title: def.title || id, description: def.description || "" }; schedule() },
    loader: function () {},
    draw: function (name, fn) {
      if (typeof fn === "function") draws[name] = fn
      if (started) schedule()
    },
    onData: function (fn) { listeners.push(fn); if (started) { try { fn() } catch (error) { console.error("dash.onData", error) } } },
    colors: COLORS,
    refresh: function (id) { if (DATASETS.indexOf(id) >= 0) load(id) },
    params: function () { return Object.assign({}, params) },
    setParams: function (next) { params = Object.assign({}, params, next || {}); restored = {}; remember(); schedule() },
    resetParams: function () { params = Object.assign({}, DEFAULT_PARAMS); restored = {}; remember(); schedule() },
    setLink: function () {},
  })

  // Called once by the host document after the page's own scripts ran.
  window.__dashStart = function () {
    if (started) return
    started = true
    var root = document.getElementById("dash-root")
    if (observer && root) {
      observer.observe(root)
      var els = root.querySelectorAll("[data-draw]")
      for (var i = 0; i < els.length; i++) observer.observe(els[i])
    }
    // The viewer's remembered filter applies before the first data arrives, so the page never flashes the default.
    restore(function () {
      for (var d = 0; d < DATASETS.length; d++) load(DATASETS[d])
      render()
    })
  }
})()
