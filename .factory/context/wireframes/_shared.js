/**
 * Crewship — Shared top toolbar component for all wireframe pages.
 * Injects the toolbar, restructures layout, removes sidebar logo header + user footer.
 */
(function () {
  document.addEventListener('DOMContentLoaded', function () {
    var root = document.querySelector('body > div');
    if (!root) return;

    // --- 1. Restructure: flex h-screen → flex-col h-screen > toolbar + flex row ---
    root.className = 'flex flex-col h-screen';
    var aside = root.querySelector('aside');
    var main = root.querySelector('main');
    if (!aside || !main) return;

    // Remove sidebar logo header (first child with border-b)
    var logoHeader = aside.children[0];
    if (logoHeader && logoHeader.tagName === 'DIV') logoHeader.remove();

    // Remove sidebar user footer (last child with border-t and "PS" avatar)
    var userFooter = aside.lastElementChild;
    if (userFooter && userFooter.tagName === 'DIV' && userFooter.textContent.indexOf('Pavel') !== -1) {
      userFooter.remove();
    }

    // Create inner wrapper for sidebar + main
    var inner = document.createElement('div');
    inner.className = 'flex flex-1 overflow-hidden';
    inner.appendChild(aside);
    inner.appendChild(main);

    // --- 2. Build toolbar ---
    var toolbar = document.createElement('header');
    toolbar.className = 'h-12 bg-white border-b border-neutral-300 flex items-center justify-between px-4 flex-shrink-0';
    toolbar.innerHTML =
      // Left: Logo
      '<div class="flex items-center gap-2.5">' +
        '<svg class="w-5 h-5 text-primary-600" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 21c.6.5 1.2 1 2.5 1 2.5 0 2.5-2 5-2 1.3 0 1.9.5 2.5 1 .6.5 1.2 1 2.5 1 2.5 0 2.5-2 5-2 1.3 0 1.9.5 2.5 1"/><path d="M19.38 20A11.6 11.6 0 0 0 21 14l-9-4-9 4c0 2.9.94 5.34 2.81 7.76"/><path d="M19 13V7a2 2 0 0 0-2-2H7a2 2 0 0 0-2 2v6"/><path d="M12 10v4"/><path d="M12 2v3"/></svg>' +
        '<span class="text-sm font-semibold text-neutral-950">Crewship</span>' +
      '</div>' +
      // Right: Org selector + utilities
      '<div class="flex items-center gap-1.5">' +
        // Org selector
        '<button class="flex items-center gap-2 px-3 py-1.5 border border-neutral-300 rounded-md hover:bg-neutral-50 mr-1">' +
          '<div class="w-5 h-5 rounded bg-primary-600 flex items-center justify-center text-white text-[9px] font-bold">U</div>' +
          '<span class="text-xs text-neutral-700 font-medium">Unify Technology</span>' +
          '<svg class="w-3.5 h-3.5 text-neutral-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m6 9 6 6 6-6"/></svg>' +
        '</button>' +
        // Search (Cmd+K)
        '<button class="flex items-center gap-2 h-8 px-3 rounded-full border border-neutral-300 bg-transparent text-neutral-500 hover:border-neutral-400 hover:text-neutral-700 transition-colors">' +
          '<svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>' +
          '<span class="text-xs">Search...</span>' +
          '<kbd class="ml-1 flex items-center gap-0.5 h-5 px-1.5 rounded border border-neutral-200 bg-neutral-100 text-[10px] font-medium text-neutral-400">' +
            '<span>&#8984;</span>K' +
          '</kbd>' +
        '</button>' +
        // Help & Docs
        '<a href="#" class="p-2 text-neutral-400 hover:text-neutral-600 rounded-md hover:bg-neutral-100" title="Help &amp; Documentation">' +
          '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H19a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1H6.5a1 1 0 0 1 0-5H20"/></svg>' +
        '</a>' +
        // Notifications (with count badge)
        '<button class="p-2 text-neutral-400 hover:text-neutral-600 rounded-md hover:bg-neutral-100 relative" title="Notifications">' +
          '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/></svg>' +
          '<span class="absolute -top-0.5 -right-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-error-500 text-[9px] font-bold text-white ring-2 ring-white">3</span>' +
        '</button>' +
        // Settings
        '<a href="#" class="p-2 text-neutral-400 hover:text-neutral-600 rounded-md hover:bg-neutral-100" title="Settings">' +
          '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>' +
        '</a>' +
        // Separator
        '<div class="w-px h-6 bg-neutral-200 mx-1"></div>' +
        // User avatar + dropdown
        '<button class="flex items-center gap-2 px-1.5 py-1 rounded-md hover:bg-neutral-100">' +
          '<div class="w-7 h-7 rounded-full bg-primary-600 flex items-center justify-center text-white text-[10px] font-semibold">PS</div>' +
          '<span class="text-xs text-neutral-700 font-medium">Pavel</span>' +
          '<svg class="w-3 h-3 text-neutral-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m6 9 6 6 6-6"/></svg>' +
        '</button>' +
      '</div>';

    // --- 3. Assemble ---
    root.innerHTML = '';
    root.appendChild(toolbar);
    root.appendChild(inner);

    // --- 4. Remove Docs and Settings from sidebar nav (now in toolbar) ---
    var nav = aside.querySelector('nav');
    if (nav) {
      var links = Array.from(nav.querySelectorAll('a'));
      links.forEach(function (a) {
        var text = a.textContent.trim();
        if (text === 'Docs' || text === 'Settings') a.remove();
      });
      // Remove the separator div (pt-4 pb-2 wrapper containing border-t)
      var divs = Array.from(nav.children);
      divs.forEach(function (el) {
        if (el.tagName === 'DIV' && el.querySelector('.border-t') && el.classList.contains('pt-4')) {
          el.remove();
        }
      });
    }
  });
})();
