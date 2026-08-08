// Client-side filtering for the instance-served OpenAPI rendering (#1846).
//
// Everything else on these pages is server-rendered and works with JavaScript
// off — <details> does the expanding, links do the navigating. This file only
// narrows an already-complete list of 500+ operations, so a browser that never
// runs it still shows the whole contract.
//
// No framework, no bundler, no CDN: the binary embeds its frontend and must
// keep working air-gapped, and this surface is served under a Content-Security
// -Policy of default-src 'none'; script-src 'self' — no inline script, no eval.
(function () {
  "use strict";

  var input = document.getElementById("op-filter");
  if (!input) return;

  var rows = Array.prototype.slice.call(document.querySelectorAll("[data-search]"));
  var groups = Array.prototype.slice.call(document.querySelectorAll("details.tag"));
  var readout = document.getElementById("op-count");
  var noun = readout && readout.textContent.indexOf("schema") !== -1 ? "schemas" : "operations";
  var total = rows.length;

  function apply() {
    var term = input.value.trim().toLowerCase();
    var shown = 0;

    for (var i = 0; i < rows.length; i++) {
      var hit = term === "" || rows[i].getAttribute("data-search").indexOf(term) !== -1;
      rows[i].hidden = !hit;
      if (hit) shown++;
    }

    for (var g = 0; g < groups.length; g++) {
      var any = groups[g].querySelector("[data-search]:not([hidden])") !== null;
      groups[g].hidden = !any;
      // Opening on a search and leaving it open afterwards would silently
      // change the page's resting state, so only a live term forces it.
      if (term !== "") groups[g].open = any;
    }

    if (readout) {
      readout.textContent = shown === total
        ? total + " " + noun
        : shown + " of " + total + " " + noun;
    }
  }

  input.addEventListener("input", apply);
  apply();
})();
