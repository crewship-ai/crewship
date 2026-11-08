/*
 * A small stand-in for the claude.ai Design canvas runtime (support.js), so the
 * artboards of "Template Crewship Design" run unchanged inside a Crewship App.
 * It implements only what those artboards use: <x-dc> with <helmet>,
 * {{ dotted.lookup }} holes in text and attributes, <sc-for list as>,
 * <sc-if value>, onClick / onChange / onInput handlers, boolean and value
 * attributes, and a `class Component extends DCLogic` script with props from
 * data-props defaults and setState. Re-renders patch the existing DOM, so
 * focus, caret and scroll survive a keystroke.
 */
(function () {
  "use strict"
  var hole = /\{\{\s*([^{}]+?)\s*\}\}/g
  var whole = /^\s*\{\{\s*([^{}]+?)\s*\}\}\s*$/
  var head = document.head || document.documentElement
  var guard = document.createElement("style")
  guard.textContent = "x-dc{display:contents}x-dc:not([data-dc-ready]){visibility:hidden}helmet{display:none}"
  head.appendChild(guard)

  var queued = false
  var renderNow = null
  function DCLogic(props) {
    this.props = props || {}
    this.state = {}
  }
  DCLogic.prototype.setState = function (patch) {
    var next = typeof patch === "function" ? patch(this.state, this.props) : patch
    this.state = Object.assign({}, this.state, next)
    if (!queued && renderNow) {
      queued = true
      Promise.resolve().then(function () { queued = false; renderNow() })
    }
  }
  window.DCLogic = DCLogic

  function lookup(path, scopes) {
    path = path.trim()
    if (path === "true") return true
    if (path === "false") return false
    if (path === "null") return null
    if (/^-?\d+(\.\d+)?$/.test(path)) return Number(path)
    var parts = path.split(".")
    for (var i = scopes.length - 1; i >= 0; i--) {
      var scope = scopes[i]
      if (scope != null && typeof scope === "object" && parts[0] in scope) {
        var value = scope
        for (var j = 0; j < parts.length; j++) {
          if (value == null) return undefined
          value = value[parts[j]]
        }
        return value
      }
    }
    return undefined
  }
  function text(value) { return value == null || value === false ? "" : String(value) }
  function fill(source, scopes) { return source.replace(hole, function (_, path) { return text(lookup(path, scopes)) }) }

  function eventFor(el, attr) {
    var name = attr.slice(2).toLowerCase()
    if (name === "change") {
      var type = (el.getAttribute("type") || "").toLowerCase()
      if (el.localName === "textarea" || (el.localName === "input" && type !== "checkbox" && type !== "radio")) return "input"
    }
    return name
  }
  function dispatch(event) {
    var fn = event.currentTarget.__dcHandlers && event.currentTarget.__dcHandlers[event.type]
    if (typeof fn === "function") fn(event)
  }
  function listen(el, type) {
    el.__dcListening = el.__dcListening || {}
    if (!el.__dcListening[type]) { el.addEventListener(type, dispatch); el.__dcListening[type] = true }
  }

  function element(source, scopes) {
    var el = document.importNode(source, false)
    var handlers = {}
    var props = {}
    for (var i = 0; i < source.attributes.length; i++) {
      var attr = source.attributes[i]
      if (attr.value.indexOf("{{") < 0) continue
      var single = whole.exec(attr.value)
      var name = attr.name
      if (/^on/i.test(name)) {
        el.removeAttribute(name)
        if (single) { var fn = lookup(single[1], scopes); if (typeof fn === "function") handlers[eventFor(el, name)] = fn }
        continue
      }
      if (!single) { el.setAttribute(name, fill(attr.value, scopes)); continue }
      var value = lookup(single[1], scopes)
      if (value === false || value == null) el.removeAttribute(name)
      else el.setAttribute(name, value === true ? "" : String(value))
      if (name === "value" || name === "checked") props[name] = value
    }
    el.__dcHandlers = handlers
    for (var type in handlers) listen(el, type)
    el.__dcProps = props
    applyProps(el, props)
    build(source, scopes, el)
    return el
  }
  function applyProps(el, props) {
    if ("value" in props && el.value !== text(props.value)) el.value = text(props.value)
    if ("checked" in props) el.checked = !!props.checked
  }

  function build(source, scopes, into) {
    for (var node = source.firstChild; node; node = node.nextSibling) {
      if (node.nodeType === 3) { into.appendChild(document.createTextNode(fill(node.nodeValue, scopes))); continue }
      if (node.nodeType !== 1) continue
      if (node.localName === "sc-for") {
        var m = whole.exec(node.getAttribute("list") || "")
        var list = m ? lookup(m[1], scopes) : null
        var as = node.getAttribute("as") || "item"
        if (list && typeof list.length === "number") {
          for (var i = 0; i < list.length; i++) {
            var scope = {}
            scope[as] = list[i]
            build(node, scopes.concat([scope]), into)
          }
        }
        continue
      }
      if (node.localName === "sc-if") {
        var v = whole.exec(node.getAttribute("value") || "")
        if (v && lookup(v[1], scopes)) build(node, scopes, into)
        continue
      }
      into.appendChild(element(node, scopes))
    }
  }

  function patch(current, next) {
    var a = current.firstChild
    var b = next.firstChild
    while (b) {
      var following = b.nextSibling
      if (!a) { current.appendChild(b) }
      else if (a.nodeType !== b.nodeType || a.nodeName !== b.nodeName || (a.nodeType === 1 && a.namespaceURI !== b.namespaceURI)) {
        current.replaceChild(b, a)
        a = b.nextSibling
      } else {
        if (a.nodeType === 3) { if (a.nodeValue !== b.nodeValue) a.nodeValue = b.nodeValue }
        else if (a.nodeType === 1) {
          for (var i = a.attributes.length - 1; i >= 0; i--) if (!b.hasAttribute(a.attributes[i].name)) a.removeAttribute(a.attributes[i].name)
          for (var j = 0; j < b.attributes.length; j++) if (a.getAttribute(b.attributes[j].name) !== b.attributes[j].value) a.setAttribute(b.attributes[j].name, b.attributes[j].value)
          a.__dcHandlers = b.__dcHandlers
          for (var type in b.__dcHandlers) listen(a, type)
          applyProps(a, b.__dcProps || {})
          patch(a, b)
        }
        a = a.nextSibling
      }
      b = following
    }
    while (a) { var gone = a; a = a.nextSibling; current.removeChild(gone) }
  }

  function start() {
    var host = document.querySelector("x-dc")
    var code = document.querySelector("script[data-dc-script]")
    if (!host) return
    var helmet = host.querySelector("helmet")
    if (helmet) { while (helmet.firstChild) head.appendChild(helmet.firstChild); helmet.parentNode.removeChild(helmet) }
    var template = document.createElement("template")
    template.innerHTML = host.innerHTML
    while (host.firstChild) host.removeChild(host.firstChild)

    var props = {}
    try {
      var declared = JSON.parse((code && code.getAttribute("data-props")) || "{}")
      for (var key in declared) if (key.charAt(0) !== "$" && declared[key] && "default" in declared[key]) props[key] = declared[key].default
    } catch (_) {}

    var Component = DCLogic
    if (code) {
      // An inline script, not eval: the Studio policy allows inline scripts only.
      var run = document.createElement("script")
      run.textContent = code.textContent + "\n;window.__dcComponent = typeof Component === 'function' ? Component : undefined;"
      head.appendChild(run)
      if (typeof window.__dcComponent === "function") Component = window.__dcComponent
    }
    var instance = new Component(props)
    if (!instance.state) instance.state = {}
    renderNow = function () {
      var vals = typeof instance.renderVals === "function" ? instance.renderVals() : {}
      var next = document.createElement("x-dc")
      build(template.content, [vals], next)
      patch(host, next)
    }
    try { renderNow() } catch (error) { if (window.console) console.error("dc.js render failed", error) }
    host.setAttribute("data-dc-ready", "")
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start)
  else start()
})()
