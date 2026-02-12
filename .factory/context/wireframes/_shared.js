/**
 * Crewship — Shared UI shell for all wireframe pages.
 * Injects: Top Toolbar + Sidebar (with 3 view modes). Pages only need <main>.
 * Sidebar modes: expanded (w-64) | collapsed (w-14, icons) | hover (w-14, expands on mouse)
 * Org switcher lives in the sidebar header. Mode persisted in localStorage.
 */
(function () {
  var STORAGE_KEY = 'crewship-sidebar-mode';

  // --- SVG icons (lucide-style) ---
  var icons = {
    ship: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 21c.6.5 1.2 1 2.5 1 2.5 0 2.5-2 5-2 1.3 0 1.9.5 2.5 1 .6.5 1.2 1 2.5 1 2.5 0 2.5-2 5-2 1.3 0 1.9.5 2.5 1"/><path d="M19.38 20A11.6 11.6 0 0 0 21 14l-9-4-9 4c0 2.9.94 5.34 2.81 7.76"/><path d="M19 13V7a2 2 0 0 0-2-2H7a2 2 0 0 0-2 2v6"/><path d="M12 10v4"/><path d="M12 2v3"/></svg>',
    dashboard: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect width="7" height="9" x="3" y="3" rx="1"/><rect width="7" height="5" x="14" y="3" rx="1"/><rect width="7" height="9" x="14" y="12" rx="1"/><rect width="7" height="5" x="3" y="16" rx="1"/></svg>',
    crews: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>',
    agents: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 8V4H8"/><rect width="16" height="12" x="4" y="8" rx="2"/><path d="M2 14h2"/><path d="M20 14h2"/><path d="M15 13v2"/><path d="M9 13v2"/></svg>',
    skills: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M19.439 7.85c-.049.322.059.648.289.878l1.568 1.568c.47.47.706 1.087.706 1.704s-.235 1.233-.706 1.704l-1.611 1.611a.98.98 0 0 1-.837.276c-.47-.07-.802-.48-.968-.925a2.501 2.501 0 1 0-3.214 3.214c.446.166.855.497.925.968a.979.979 0 0 1-.276.837l-1.61 1.61a2.404 2.404 0 0 1-1.705.707 2.402 2.402 0 0 1-1.704-.706l-1.568-1.568a1.026 1.026 0 0 0-.877-.29c-.493.074-.84.504-1.02.968a2.5 2.5 0 1 1-3.237-3.237c.464-.18.894-.527.967-1.02a1.026 1.026 0 0 0-.289-.877l-1.568-1.568A2.402 2.402 0 0 1 1.998 12c0-.617.236-1.234.706-1.704L4.315 8.685a.98.98 0 0 1 .837-.276c.47.07.802.48.968.925a2.501 2.501 0 1 0 3.214-3.214c-.446-.166-.855-.497-.925-.968a.979.979 0 0 1 .276-.837l1.61-1.61A2.404 2.404 0 0 1 12 2c.617 0 1.234.236 1.704.706l1.568 1.568c.23.23.556.338.877.29.493-.074.84-.504 1.02-.968a2.5 2.5 0 1 1 3.237 3.237c-.464.18-.894.527-.967 1.02Z"/></svg>',
    marketplace: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m2 7 4.41-4.41A2 2 0 0 1 7.83 2h8.34a2 2 0 0 1 1.42.59L22 7"/><path d="M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8"/><path d="M15 22v-4a2 2 0 0 0-2-2h-2a2 2 0 0 0-2 2v4"/><path d="M2 7h20"/><path d="M22 7v3a2 2 0 0 1-2 2a2.7 2.7 0 0 1-1.59-.63.7.7 0 0 0-.82 0A2.7 2.7 0 0 1 16 12a2.7 2.7 0 0 1-1.59-.63.7.7 0 0 0-.82 0A2.7 2.7 0 0 1 12 12a2.7 2.7 0 0 1-1.59-.63.7.7 0 0 0-.82 0A2.7 2.7 0 0 1 8 12a2.7 2.7 0 0 1-1.59-.63.7.7 0 0 0-.82 0A2.7 2.7 0 0 1 4 12a2 2 0 0 1-2-2V7"/></svg>',
    credentials: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/></svg>',
    auditlog: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 12h-5"/><path d="M15 8h-5"/><path d="M19 17V5a2 2 0 0 0-2-2H4"/><path d="M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2"/></svg>',
    search: '<svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>',
    book: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H19a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1H6.5a1 1 0 0 1 0-5H20"/></svg>',
    bell: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/></svg>',
    settings: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>',
    chevron: '<svg class="w-3.5 h-3.5 text-neutral-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m6 9 6 6 6-6"/></svg>',
    chevronSm: '<svg class="w-3 h-3 text-neutral-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m6 9 6 6 6-6"/></svg>',
    panelLeft: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/></svg>',
    chevronsLeft: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m11 17-5-5 5-5"/><path d="m18 17-5-5 5-5"/></svg>',
    chevronsRight: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m6 17 5-5-5-5"/><path d="m13 17 5-5-5-5"/></svg>'
  };

  // --- Page config: title, status pills, CTA per page ---
  // Status pills show org-wide platform health in the toolbar
  var pageConfig = {
    '01-': {
      title: 'Dashboard',
      pills: [
        { label: '3 running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' },
        { label: '3 idle', color: 'bg-primary-100 text-primary-700' },
        { label: '1 error', color: 'bg-error-50 text-error-700' }
      ]
    },
    '02-': {
      title: 'Agents',
      pills: [
        { label: '3 running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' },
        { label: '3 idle', color: 'bg-primary-100 text-primary-700' },
        { label: '1 error', color: 'bg-error-50 text-error-700' }
      ]
    },
    '03-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [{ label: 'Running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' }] },
    '04-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [{ label: 'Running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' }] },
    '05-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [{ label: 'Running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' }] },
    '06-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [{ label: 'Running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' }] },
    '07-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [{ label: 'Running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' }] },
    '08-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [{ label: 'Running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' }] },
    '09-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [] },
    '10-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [{ label: '4 skills', color: 'bg-primary-100 text-primary-700' }] },
    '11-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [{ label: '2 credentials', color: 'bg-neutral-100 text-neutral-600' }] },
    '12-': { title: 'Agents', breadcrumb: 'Claude — SEO Writer', pills: [] },
    '13-': {
      title: 'Credentials',
      pills: [
        { label: '5 active', color: 'bg-success-50 text-success-700', dot: 'bg-success-500' },
        { label: '1 expiring', color: 'bg-warning-50 text-warning-700' }
      ]
    },
    '14-': {
      title: 'Skills',
      pills: [
        { label: '5 bundled', color: 'bg-neutral-100 text-neutral-600' },
        { label: '5 managed', color: 'bg-primary-100 text-primary-700' },
        { label: '2 custom', color: 'bg-brand-teal/10 text-teal-700' }
      ]
    },
    '15-': {
      title: 'Marketplace',
      pills: [
        { label: '39 available', color: 'bg-primary-100 text-primary-700' },
        { label: '3 sources', color: 'bg-neutral-100 text-neutral-600' }
      ]
    },
    '16-': {
      title: 'Crews',
      pills: [
        { label: '4 crews', color: 'bg-neutral-100 text-neutral-600' },
        { label: '7 agents', color: 'bg-primary-100 text-primary-700' },
        { label: '3 connections', color: 'bg-primary-100 text-primary-700' }
      ]
    },
    '19-': {
      title: 'Audit Log',
      pills: [
        { label: '248 events', color: 'bg-neutral-100 text-neutral-600' },
        { label: 'Last 30 days', color: 'bg-neutral-100 text-neutral-600' }
      ]
    }
  };

  function getPageConfig(filename) {
    for (var prefix in pageConfig) {
      if (filename.indexOf(prefix) === 0) return pageConfig[prefix];
    }
    return { title: 'Crewship', pills: [] };
  }

  // --- Nav items ---
  var navItems = [
    { label: 'Dashboard',     icon: 'dashboard',    href: '01-dashboard.html',   match: ['01-'] },
    { label: 'Crews',         icon: 'crews',         href: '16-crews.html',      match: ['16-'], badge: '4' },
    { label: 'Agents',        icon: 'agents',        href: '02-agents-list.html', match: ['02-','03-','04-','05-','06-','07-','08-','09-','10-','11-','12-'], badge: '7' },
    { label: 'Skills',        icon: 'skills',        href: '14-skills.html',     match: ['14-'], badge: '12' },
    { label: 'Marketplace',   icon: 'marketplace',   href: '15-marketplace.html', match: ['15-'], badge: '39' },
    { label: 'Credentials',   icon: 'credentials',   href: '13-credentials.html', match: ['13-'], badge: '6' },
    { label: 'Audit Log',     icon: 'auditlog',      href: '19-audit-log.html',  match: ['19-'] }
  ];

  // --- Helpers ---
  function getMode() {
    try { return localStorage.getItem(STORAGE_KEY) || 'expanded'; } catch (e) { return 'expanded'; }
  }
  function setMode(m) {
    try { localStorage.setItem(STORAGE_KEY, m); } catch (e) {}
  }

  // Inject pulse-dot animation if not already present
  if (!document.querySelector('style[data-shared]')) {
    var style = document.createElement('style');
    style.setAttribute('data-shared', '1');
    style.textContent = '@keyframes pulse-dot{0%,100%{opacity:1}50%{opacity:.4}}.pulse-dot{animation:pulse-dot 2s ease-in-out infinite}';
    document.head.appendChild(style);
  }

  document.addEventListener('DOMContentLoaded', function () {
    var root = document.querySelector('body > div');
    if (!root) return;
    var main = root.querySelector('main');
    if (!main) return;

    var path = window.location.pathname;
    var filename = path.substring(path.lastIndexOf('/') + 1);

    var oldAside = root.querySelector('aside');
    if (oldAside) oldAside.remove();
    root.className = 'flex flex-col h-screen';
    main.remove();

    // ======== TOOLBAR ========
    var pg = getPageConfig(filename);

    // Build left side: page title + breadcrumb + status pills
    var titleHtml = '<h1 class="text-base font-semibold text-neutral-950">' + pg.title + '</h1>';
    if (pg.breadcrumb) {
      titleHtml =
        '<div class="flex items-center gap-1.5 text-sm">' +
          '<a href="02-agents-list.html" class="text-neutral-400 hover:text-neutral-600">' + pg.title + '</a>' +
          '<span class="text-neutral-300">/</span>' +
          '<span class="font-semibold text-neutral-950">' + pg.breadcrumb + '</span>' +
        '</div>';
    }
    var pillsHtml = '';
    if (pg.pills && pg.pills.length) {
      pillsHtml = '<div class="flex items-center gap-2">';
      pg.pills.forEach(function (p) {
        var dotStr = p.dot ? '<span class="w-1.5 h-1.5 rounded-full ' + p.dot + '"></span>' : '';
        pillsHtml += '<span class="flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium ' + p.color + '">' + dotStr + p.label + '</span>';
      });
      pillsHtml += '</div>';
    }

    var toolbar = document.createElement('header');
    toolbar.className = 'h-12 bg-white flex items-center justify-between px-4 flex-shrink-0';
    toolbar.innerHTML =
      '<div class="flex items-center gap-3">' + titleHtml + pillsHtml + '</div>' +
      '<div class="flex items-center gap-1.5">' +
        '<button class="flex items-center gap-2 h-8 px-3 rounded-full border border-neutral-200 bg-transparent text-neutral-500 hover:border-neutral-300 hover:text-neutral-700 transition-colors">' +
          icons.search +
          '<span class="text-xs">Search...</span>' +
          '<kbd class="ml-1 flex items-center gap-0.5 h-5 px-1.5 rounded border border-neutral-200 bg-neutral-50 text-[10px] font-medium text-neutral-400"><span>&#8984;</span>K</kbd>' +
        '</button>' +
        '<a href="#" class="p-2 text-neutral-400 hover:text-neutral-600 rounded-md hover:bg-neutral-50" title="Help &amp; Documentation">' + icons.book + '</a>' +
        '<button class="p-2 text-neutral-400 hover:text-neutral-600 rounded-md hover:bg-neutral-50 relative" title="Notifications">' +
          icons.bell +
          '<span class="absolute -top-0.5 -right-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-error-500 text-[9px] font-bold text-white ring-2 ring-white">3</span>' +
        '</button>' +
        '<a href="#" class="p-2 text-neutral-400 hover:text-neutral-600 rounded-md hover:bg-neutral-50" title="Settings">' + icons.settings + '</a>' +
        '<button class="flex items-center gap-2 px-1.5 py-1 rounded-md hover:bg-neutral-50">' +
          '<div class="w-7 h-7 rounded-full bg-primary-600 flex items-center justify-center text-white text-[10px] font-semibold">PS</div>' +
          '<span class="text-xs text-neutral-700 font-medium">Pavel</span>' +
          icons.chevronSm +
        '</button>' +
      '</div>';

    // ======== SIDEBAR ========
    var sidebar = document.createElement('aside');
    var currentMode = getMode();

    function buildSidebar(mode, isHoverExpanded) {
      var isWide = (mode === 'expanded') || (mode === 'hover' && isHoverExpanded);

      // --- Logo ---
      var logoHtml;
      if (isWide) {
        logoHtml =
          '<div class="h-12 px-4 flex items-center gap-2.5 flex-shrink-0">' +
            '<span class="text-primary-600">' + icons.ship + '</span>' +
            '<span class="text-sm font-semibold text-neutral-950">Crewship</span>' +
          '</div>';
      } else {
        logoHtml =
          '<div class="h-12 flex items-center justify-center flex-shrink-0">' +
            '<span class="text-primary-600">' + icons.ship + '</span>' +
          '</div>';
      }

      // --- Org switcher header ---
      var orgHtml;
      if (isWide) {
        orgHtml =
          '<div class="px-2 pb-2">' +
            '<button class="flex items-center gap-2.5 w-full px-2.5 py-2 rounded-md hover:bg-neutral-100 transition-colors">' +
              '<div class="w-7 h-7 rounded-lg bg-primary-600 flex items-center justify-center text-white text-[10px] font-bold flex-shrink-0">U</div>' +
              '<div class="flex-1 min-w-0 text-left">' +
                '<div class="text-xs font-semibold text-neutral-900 truncate">Unify Technology</div>' +
                '<div class="text-[10px] text-neutral-400 truncate">3 members</div>' +
              '</div>' +
              icons.chevron +
            '</button>' +
          '</div>';
      } else {
        orgHtml =
          '<div class="px-2 pb-2 flex justify-center">' +
            '<button class="w-8 h-8 rounded-lg bg-primary-600 flex items-center justify-center text-white text-[10px] font-bold hover:opacity-90 transition-opacity" title="Unify Technology">U</button>' +
          '</div>';
      }

      // --- Nav items ---
      var navHtml = '<nav class="flex-1 px-2 py-1 space-y-0.5 overflow-y-auto">';
      navItems.forEach(function (item) {
        var isActive = item.match.some(function (prefix) { return filename.indexOf(prefix) === 0; });
        var baseCls = isActive
          ? 'flex items-center gap-3 rounded-md bg-primary-100 text-primary-600 font-medium text-sm'
          : 'flex items-center gap-3 rounded-md text-neutral-600 hover:bg-neutral-100 text-sm';

        if (isWide) {
          baseCls += ' px-3 py-2';
          var badgeHtml = '';
          if (item.badge) {
            if (item.badgeType === 'new') {
              badgeHtml = '<span class="ml-auto text-[10px] bg-primary-600 text-white px-1.5 py-0.5 rounded font-medium">' + item.badge + '</span>';
            } else {
              var bgCls = isActive ? 'bg-primary-200 text-primary-700' : 'bg-neutral-200 text-neutral-600';
              badgeHtml = '<span class="ml-auto text-xs ' + bgCls + ' px-1.5 py-0.5 rounded-full">' + item.badge + '</span>';
            }
          }
          navHtml += '<a href="' + item.href + '" class="' + baseCls + '">' +
            '<span class="flex-shrink-0">' + icons[item.icon] + '</span>' +
            '<span class="truncate">' + item.label + '</span>' +
            badgeHtml +
          '</a>';
        } else {
          baseCls += ' px-0 py-2 justify-center relative';
          var dotHtml = '';
          if (item.badgeType === 'new') {
            dotHtml = '<span class="absolute top-1 right-1 w-1.5 h-1.5 rounded-full bg-primary-600"></span>';
          }
          navHtml += '<a href="' + item.href + '" class="' + baseCls + '" title="' + item.label + '">' +
            '<span class="flex-shrink-0">' + icons[item.icon] + '</span>' +
            dotHtml +
          '</a>';
        }
      });
      navHtml += '</nav>';

      // --- View mode control (bottom) ---
      var controlHtml;
      if (isWide) {
        controlHtml =
          '<div class="px-3 py-3 mt-auto">' +
            '<div class="text-[10px] font-medium text-neutral-400 uppercase tracking-wider mb-2">Sidebar</div>' +
            '<div class="flex gap-1">' +
              '<button data-sidebar-mode="expanded" class="flex-1 text-[11px] py-1.5 rounded-md transition-colors ' +
                (mode === 'expanded' ? 'bg-primary-100 text-primary-600 font-medium' : 'text-neutral-500 hover:bg-neutral-100') +
              '">Expanded</button>' +
              '<button data-sidebar-mode="collapsed" class="flex-1 text-[11px] py-1.5 rounded-md transition-colors ' +
                (mode === 'collapsed' ? 'bg-primary-100 text-primary-600 font-medium' : 'text-neutral-500 hover:bg-neutral-100') +
              '">Collapsed</button>' +
              '<button data-sidebar-mode="hover" class="flex-1 text-[11px] py-1.5 rounded-md transition-colors ' +
                (mode === 'hover' ? 'bg-primary-100 text-primary-600 font-medium' : 'text-neutral-500 hover:bg-neutral-100') +
              '">On hover</button>' +
            '</div>' +
          '</div>';
      } else {
        controlHtml =
          '<div class="px-1.5 py-3 mt-auto flex justify-center">' +
            '<button data-sidebar-mode="expanded" class="p-2 rounded-md text-neutral-400 hover:text-neutral-600 hover:bg-neutral-100" title="Expand sidebar">' +
              icons.chevronsRight +
            '</button>' +
          '</div>';
      }

      sidebar.innerHTML = logoHtml + orgHtml + navHtml + controlHtml;
    }

    function applySidebarMode(mode, isHoverExpanded) {
      var isWide = (mode === 'expanded') || (mode === 'hover' && isHoverExpanded);
      sidebar.className = 'bg-white flex flex-col flex-shrink-0 transition-all duration-200 overflow-hidden ' +
        (isWide ? 'w-64' : 'w-14');
      if (mode === 'hover') {
        sidebar.style.position = isHoverExpanded ? 'relative' : 'relative';
      }
      buildSidebar(mode, isHoverExpanded);
      bindModeButtons(mode);
    }

    function bindModeButtons(mode) {
      var buttons = sidebar.querySelectorAll('[data-sidebar-mode]');
      buttons.forEach(function (btn) {
        btn.addEventListener('click', function (e) {
          e.preventDefault();
          var newMode = btn.getAttribute('data-sidebar-mode');
          currentMode = newMode;
          setMode(newMode);
          applySidebarMode(newMode, newMode === 'hover' ? false : undefined);
        });
      });
    }

    // Hover behavior for "hover" mode
    var hoverTimeout;
    sidebar.addEventListener('mouseenter', function () {
      if (currentMode === 'hover') {
        clearTimeout(hoverTimeout);
        applySidebarMode('hover', true);
      }
    });
    sidebar.addEventListener('mouseleave', function () {
      if (currentMode === 'hover') {
        hoverTimeout = setTimeout(function () {
          applySidebarMode('hover', false);
        }, 200);
      }
    });

    // ======== ASSEMBLE ========
    // Layout: flex-row h-screen → sidebar (full height) | flex-col (toolbar + main)
    var rightCol = document.createElement('div');
    rightCol.className = 'flex flex-col flex-1 overflow-hidden';
    rightCol.appendChild(toolbar);
    rightCol.appendChild(main);

    root.innerHTML = '';
    root.className = 'flex h-screen';
    root.appendChild(sidebar);
    root.appendChild(rightCol);

    // Initial render
    applySidebarMode(currentMode, false);
  });
})();
