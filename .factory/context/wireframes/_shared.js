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
    chevronsRight: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m6 17 5-5-5-5"/><path d="m13 17 5-5-5-5"/></svg>',
    activity: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/></svg>',
    health: '<svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/></svg>',
    settingsNav: '<svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>'
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
    '17-': {
      title: 'Runs',
      pills: [
        { label: '4 running', color: 'bg-success-50 text-success-700', dot: 'bg-success-500 pulse-dot' },
        { label: '18 today', color: 'bg-neutral-100 text-neutral-600' },
        { label: '2 failed', color: 'bg-error-50 text-error-700' }
      ]
    },
    '19-': {
      title: 'Audit Log',
      pills: [
        { label: '248 events', color: 'bg-neutral-100 text-neutral-600' },
        { label: 'Last 30 days', color: 'bg-neutral-100 text-neutral-600' }
      ]
    },
    '20-': {
      title: 'Settings',
      pills: []
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
    { type: 'section', label: 'Work' },
    { label: 'Dashboard',     icon: 'dashboard',    href: '01-dashboard.html',   match: ['01-'] },
    { label: 'Agents',        icon: 'agents',        href: '02-agents-list.html', match: ['02-','03-','04-','05-','06-','07-','08-','09-','10-','11-','12-'], badge: '7', errorDot: true },
    { label: 'Crews',         icon: 'crews',         href: '16-crews.html',      match: ['16-'], badge: '4' },
    { type: 'section', label: 'Configure' },
    { label: 'Skills',        icon: 'skills',        href: '14-skills.html',     match: ['14-'], badge: '12' },
    { label: 'Marketplace',   icon: 'marketplace',   href: '15-marketplace.html', match: ['15-'], badge: '39', future: true },
    { label: 'Credentials',   icon: 'credentials',   href: '13-credentials.html', match: ['13-'], badge: '6' },
    { type: 'section', label: 'Monitor' },
    { label: 'Runs',          icon: 'activity',      href: '17-runs.html',       match: ['17-'], todo: true },
    { label: 'Audit Log',     icon: 'auditlog',      href: '19-audit-log.html',  match: ['19-'] },
    { type: 'section', label: 'System' },
    { label: 'Settings',      icon: 'settingsNav',   href: '20-settings.html',   match: ['20-'] }
  ];

  // --- Helpers ---
  function getMode() {
    try { return localStorage.getItem(STORAGE_KEY) || 'expanded'; } catch (e) { return 'expanded'; }
  }
  function setMode(m) {
    try { localStorage.setItem(STORAGE_KEY, m); } catch (e) {}
  }

  // Inject animations
  if (!document.querySelector('style[data-shared]')) {
    var style = document.createElement('style');
    style.setAttribute('data-shared', '1');
    style.textContent =
      '@keyframes pulse-dot{0%,100%{opacity:1}50%{opacity:.4}}.pulse-dot{animation:pulse-dot 2s ease-in-out infinite}' +
      '@keyframes ai-glow{0%,100%{box-shadow:0 0 12px 4px rgba(78,205,196,.3)}50%{box-shadow:0 0 28px 8px rgba(78,205,196,.45)}}' +
      '@keyframes ai-entrance{0%{opacity:0;transform:scale(.6)}100%{opacity:1;transform:scale(1)}}' +
      '@keyframes ai-panel-in{0%{opacity:0;transform:translateY(16px) scale(.95)}100%{opacity:1;transform:translateY(0) scale(1)}}' +
      '@keyframes ship-wave{0%,100%{transform:translateY(0) rotate(0deg)}20%{transform:translateY(-3px) rotate(2deg)}40%{transform:translateY(1px) rotate(-1deg)}60%{transform:translateY(-2px) rotate(1.5deg)}80%{transform:translateY(1px) rotate(-0.5deg)}}' +
      '.ai-btn-glow{animation:ai-glow 3s ease-in-out infinite}' +
      '.ai-btn-entrance{animation:ai-entrance .4s cubic-bezier(.34,1.56,.64,1) both}' +
      '.ai-panel-entrance{animation:ai-panel-in .25s ease-out both}' +
      '.ai-btn-pill{transition:width .25s cubic-bezier(.4,0,.2,1),padding .25s cubic-bezier(.4,0,.2,1)}' +
      '.ai-btn-label{transition:opacity .15s ease,max-width .25s ease;overflow:hidden;white-space:nowrap}' +
      '.ship-wave{animation:ship-wave 4s ease-in-out infinite}';
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
        '<div class="flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-success-50 border border-success-500/20 mr-1" title="Backend service status">' +
          '<span class="w-1.5 h-1.5 rounded-full bg-success-500 pulse-dot"></span>' +
          '<span class="text-[10px] font-medium text-success-700">crewshipd</span>' +
        '</div>' +
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
        '<a href="20-settings.html" class="p-2 text-neutral-400 hover:text-neutral-600 rounded-md hover:bg-neutral-50" title="Settings">' + icons.settings + '</a>' +
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
        // Section separator
        if (item.type === 'section') {
          if (isWide) {
            navHtml += '<div class="pt-4 pb-1 px-3 text-[10px] font-medium text-neutral-400 uppercase tracking-wider">' + item.label + '</div>';
          } else {
            navHtml += '<div class="pt-3 pb-1 px-2"><div class="border-t border-neutral-200"></div></div>';
          }
          return;
        }

        var isActive = item.match.some(function (prefix) { return filename.indexOf(prefix) === 0; });
        var baseCls = isActive
          ? 'flex items-center gap-3 rounded-md bg-primary-100 text-primary-600 font-medium text-sm'
          : 'flex items-center gap-3 rounded-md text-neutral-600 hover:bg-neutral-100 text-sm';

        if (isWide) {
          baseCls += ' px-3 py-2';
          var badgeHtml = '';
          if (item.todo) {
            badgeHtml = '<span class="ml-auto text-[9px] bg-warning-50 text-warning-700 border border-warning-500/30 px-1.5 py-0.5 rounded font-medium">TODO</span>';
          } else if (item.future) {
            badgeHtml = '<span class="ml-auto text-[9px] bg-neutral-100 text-neutral-400 px-1.5 py-0.5 rounded font-medium">FUTURE</span>';
          } else if (item.badge) {
            var bgCls = isActive ? 'bg-primary-200 text-primary-700' : 'bg-neutral-200 text-neutral-600';
            badgeHtml = '<span class="ml-auto text-xs ' + bgCls + ' px-1.5 py-0.5 rounded-full">' + item.badge + '</span>';
          }
          // Error dot for agents (shows when there's an agent in error state)
          var errorDotHtml = '';
          if (item.errorDot) {
            errorDotHtml = '<span class="w-1.5 h-1.5 rounded-full bg-error-500 flex-shrink-0"></span>';
          }
          navHtml += '<a href="' + item.href + '" class="' + baseCls + '">' +
            '<span class="flex-shrink-0">' + icons[item.icon] + '</span>' +
            '<span class="truncate">' + item.label + '</span>' +
            errorDotHtml +
            badgeHtml +
          '</a>';
        } else {
          baseCls += ' px-0 py-2 justify-center relative';
          var dotHtml = '';
          if (item.errorDot) {
            dotHtml = '<span class="absolute top-1 right-0.5 w-1.5 h-1.5 rounded-full bg-error-500"></span>';
          } else if (item.todo) {
            dotHtml = '<span class="absolute top-1 right-0.5 w-1.5 h-1.5 rounded-full bg-warning-500"></span>';
          }
          navHtml += '<a href="' + item.href + '" class="' + baseCls + '" title="' + item.label + (item.todo ? ' (TODO)' : item.future ? ' (Future)' : '') + '">' +
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

    // ======== CREWSHIP AI FLOATING BUTTON ========
    var aiOpen = false;

    // Crewship logo SVG (cropped, white fill, no background)
    var shipSvg = '<svg class="ship-wave" style="width:36px;height:24px" viewBox="62 88 185 120" xmlns="http://www.w3.org/2000/svg"><g fill="white" fill-rule="nonzero"><path d="M80.49302,160.9043c19.04106,-0.63867 38.56128,1.70508 57.52178,3.33105c22.81187,1.98633 45.64146,3.7749 68.48423,5.36572c11.27197,0.78809 23.95166,1.91602 35.18994,2.23242l0.00146,12.5625c0,7.68311 1.00342,16.11475 -6.81006,20.52832c-2.56494,1.4502 -4.82959,1.45752 -7.67725,1.46924l-132.00088,0.01758c-1.37109,-1.00928 -4.05396,-3.87451 -5.22305,-5.22217c-9.83247,-11.34375 -17.83081,-25.69043 -24.554,-39.12012c5.29438,-0.65479 9.75776,-0.94336 15.06782,-1.16455z"/><path d="M79.33345,143.18101c3.42158,-0.12788 6.95186,-0.03823 10.37549,0.01846c23.16064,0.3835 45.99814,4.23413 68.89849,7.51538c26.93994,3.94482 53.93701,7.47803 80.98535,10.59814c0.43652,1.35352 0.84668,2.76123 1.26416,4.12646c-1.89697,-0.30908 -4.96729,-0.50098 -6.95801,-0.68701l-13.2583,-1.22754c-24.26514,-2.3042 -48.51562,-4.771 -72.74854,-7.39893c-28.43335,-3.04834 -58.20586,-6.79687 -86.79185,-3.41748c-0.83701,-2.12695 -1.91279,-4.48975 -2.81426,-6.61245c6.6145,-2.26084 14.11143,-2.65151 21.04746,-2.91504z"/><path d="M183.02197,105.7519l12.83496,-0.01011c-0.05127,7.02935 -0.48779,14.0729 -0.61377,21.10415c6.30762,1.4499 13.31543,3.42495 19.64355,5.05591c0.66943,1.78931 1.30811,3.56953 2.09473,5.31064c-7.50879,-1.99775 -16.11035,-3.81577 -23.75244,-5.65049l-14.62207,-3.5373c-2.51807,-0.61333 -5.4917,-1.41973 -8.02588,-1.83267c-1.42236,-0.23174 -4.16748,-0.15586 -5.6748,-0.14824c-10.78418,-0.10371 -22.31733,-0.20493 -33.08408,-0.00249c1.30635,-1.98428 2.70293,-3.90776 4.18535,-5.76416c6.79937,0.13521 13.75371,-0.02461 20.596,0.07046c-0.67822,-0.75088 -1.229,-1.5145 -1.43115,-2.52305c-0.21094,-1.00576 0.01025,-2.05342 0.60938,-2.88853c0.6709,-0.92725 1.70215,-1.52842 2.84033,-1.65557c2.31738,-0.25796 4.07959,1.47993 4.22168,3.77681c0.08936,1.4439 -0.73535,2.34814 -1.66992,3.32109c2.9458,0.01113 7.37549,-0.27056 10.14697,0.03398c1.95996,0.21548 6.1582,1.5186 8.40088,2.05547c1.0166,-5.57783 2.30859,-11.13647 3.30029,-16.71592z"/><path d="M123.98877,133.72485c1.93945,-0.07061 4.36699,0.01553 6.33662,0.02959l12.93354,0.06548l10.77964,-0.05552c2.06689,-0.00776 5.29248,-0.09844 7.27588,0.16699c2.57959,0.34512 5.63232,0.99199 8.20605,1.4918l14.82275,2.91563l47.84619,9.38921c0.70898,1.69629 1.38135,3.47461 2.05371,5.19287l-3.35742,-0.59912c-15.52734,-2.60449 -31.04297,-5.28516 -46.54395,-8.04448l-15.83057,-2.85132c-1.95264,-0.35215 -6.94336,-1.39248 -8.71289,-1.4562c-4.28906,-0.15425 -9.14355,-0.0731 -13.48022,-0.07427l-26.45215,0.01655c1.06538,-1.86006 2.85337,-4.4354 4.1228,-6.18721z"/><path d="M204.39697,99.91509c0.31641,0.05845 2.58838,0.91948 2.9751,1.06187l6.67822,2.47075c-2.96484,1.06641 -7.03564,2.2875 -10.06934,3.07866c-2.69824,-0.68584 -6.02197,-2.52773 -8.59717,-2.73794c-3.8877,-0.00601 -8.08594,-0.08101 -11.94434,0.05801l0.40283,-2.0543c4.52783,-0.09697 9.29297,0.19438 13.75928,-0.23979c2.02002,-0.19658 4.80908,-1.04692 6.79541,-1.63726z"/><path d="M195.78076,93.61919c0.78809,-0.11206 8.24854,2.91357 9.36768,3.51445c-3.82471,1.0812 -7.24072,2.34404 -11.21191,2.33438l-9.60498,-0.00103l0.59619,-2.88853c3.48047,-1.05293 7.32861,-2.02632 10.85303,-2.95928z"/></g></svg>';

    var shipSvgClose = '<svg style="width:28px;height:28px" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>';

    // --- Floating button ---
    var aiBtn = document.createElement('button');
    aiBtn.className = 'fixed z-50 ai-btn-entrance ai-btn-glow ai-btn-pill flex items-center gap-0 rounded-full shadow-xl cursor-pointer border-0 outline-none';
    aiBtn.style.cssText = 'background:linear-gradient(135deg,#4ECDC4 0%,#1877F2 100%);width:64px;height:64px;padding:0;bottom:32px;right:32px;';
    aiBtn.title = 'Crewship AI (⌘J)';
    aiBtn.innerHTML =
      '<span class="flex items-center justify-center flex-shrink-0" style="width:64px;height:64px">' + shipSvg + '</span>' +
      '<span class="ai-btn-label text-white text-sm font-semibold" style="max-width:0;opacity:0">Crewship AI</span>';

    // Hover: expand to pill
    aiBtn.addEventListener('mouseenter', function () {
      if (!aiOpen) {
        aiBtn.style.width = '180px';
        aiBtn.style.paddingRight = '20px';
        var label = aiBtn.querySelector('.ai-btn-label');
        label.style.maxWidth = '110px';
        label.style.opacity = '1';
      }
    });
    aiBtn.addEventListener('mouseleave', function () {
      if (!aiOpen) {
        aiBtn.style.width = '64px';
        aiBtn.style.paddingRight = '0';
        var label = aiBtn.querySelector('.ai-btn-label');
        label.style.maxWidth = '0';
        label.style.opacity = '0';
      }
    });

    // --- Chat panel ---
    var aiPanel = document.createElement('div');
    aiPanel.className = 'fixed z-50 hidden';
    aiPanel.style.cssText = 'width:400px;bottom:108px;right:32px;';
    aiPanel.innerHTML =
      '<div class="ai-panel-entrance bg-white rounded-2xl shadow-2xl border border-neutral-200 overflow-hidden flex flex-col" style="max-height:600px">' +
        // Header
        '<div class="px-4 py-3 flex items-center justify-between flex-shrink-0" style="background:linear-gradient(135deg,#4ECDC4 0%,#1877F2 100%)">' +
          '<div class="flex items-center gap-2.5">' +
            '<svg style="width:24px;height:16px" viewBox="62 88 185 120" xmlns="http://www.w3.org/2000/svg"><g fill="white" fill-rule="nonzero"><path d="M80.49302,160.9043c19.04106,-0.63867 38.56128,1.70508 57.52178,3.33105c22.81187,1.98633 45.64146,3.7749 68.48423,5.36572c11.27197,0.78809 23.95166,1.91602 35.18994,2.23242l0.00146,12.5625c0,7.68311 1.00342,16.11475 -6.81006,20.52832c-2.56494,1.4502 -4.82959,1.45752 -7.67725,1.46924l-132.00088,0.01758c-1.37109,-1.00928 -4.05396,-3.87451 -5.22305,-5.22217c-9.83247,-11.34375 -17.83081,-25.69043 -24.554,-39.12012c5.29438,-0.65479 9.75776,-0.94336 15.06782,-1.16455z"/><path d="M79.33345,143.18101c3.42158,-0.12788 6.95186,-0.03823 10.37549,0.01846c23.16064,0.3835 45.99814,4.23413 68.89849,7.51538c26.93994,3.94482 53.93701,7.47803 80.98535,10.59814c0.43652,1.35352 0.84668,2.76123 1.26416,4.12646c-1.89697,-0.30908 -4.96729,-0.50098 -6.95801,-0.68701l-13.2583,-1.22754c-24.26514,-2.3042 -48.51562,-4.771 -72.74854,-7.39893c-28.43335,-3.04834 -58.20586,-6.79687 -86.79185,-3.41748c-0.83701,-2.12695 -1.91279,-4.48975 -2.81426,-6.61245c6.6145,-2.26084 14.11143,-2.65151 21.04746,-2.91504z"/><path d="M183.02197,105.7519l12.83496,-0.01011c-0.05127,7.02935 -0.48779,14.0729 -0.61377,21.10415c6.30762,1.4499 13.31543,3.42495 19.64355,5.05591c0.66943,1.78931 1.30811,3.56953 2.09473,5.31064c-7.50879,-1.99775 -16.11035,-3.81577 -23.75244,-5.65049l-14.62207,-3.5373c-2.51807,-0.61333 -5.4917,-1.41973 -8.02588,-1.83267c-1.42236,-0.23174 -4.16748,-0.15586 -5.6748,-0.14824c-10.78418,-0.10371 -22.31733,-0.20493 -33.08408,-0.00249c1.30635,-1.98428 2.70293,-3.90776 4.18535,-5.76416c6.79937,0.13521 13.75371,-0.02461 20.596,0.07046c-0.67822,-0.75088 -1.229,-1.5145 -1.43115,-2.52305c-0.21094,-1.00576 0.01025,-2.05342 0.60938,-2.88853c0.6709,-0.92725 1.70215,-1.52842 2.84033,-1.65557c2.31738,-0.25796 4.07959,1.47993 4.22168,3.77681c0.08936,1.4439 -0.73535,2.34814 -1.66992,3.32109c2.9458,0.01113 7.37549,-0.27056 10.14697,0.03398c1.95996,0.21548 6.1582,1.5186 8.40088,2.05547c1.0166,-5.57783 2.30859,-11.13647 3.30029,-16.71592z"/></g></svg>' +
            '<span class="text-sm font-semibold text-white">Crewship AI</span>' +
            '<span class="text-[9px] px-1.5 py-0.5 rounded bg-white/20 text-white font-medium">BETA</span>' +
          '</div>' +
          '<button id="ai-close" class="p-1 rounded-md text-white/70 hover:text-white hover:bg-white/10">' +
            '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>' +
          '</button>' +
        '</div>' +
        // TODO Banner
        '<div class="px-4 py-2 bg-warning-50 border-b border-warning-500/20 flex items-center gap-2">' +
          '<svg class="w-3.5 h-3.5 text-warning-700 flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>' +
          '<span class="text-[10px] text-warning-700 font-medium">Phase 2 — wireframe preview. RAG + streaming in development.</span>' +
        '</div>' +
        // Messages area
        '<div class="flex-1 overflow-y-auto px-4 py-4 space-y-4" style="min-height:260px">' +
          // AI welcome message
          '<div class="flex gap-2.5">' +
            '<div class="w-8 h-8 rounded-full flex-shrink-0 flex items-center justify-center" style="background:linear-gradient(135deg,#4ECDC4,#1877F2)">' +
              '<svg style="width:18px;height:12px" viewBox="62 88 185 120" xmlns="http://www.w3.org/2000/svg"><g fill="white" fill-rule="nonzero"><path d="M80.49302,160.9043c19.04106,-0.63867 38.56128,1.70508 57.52178,3.33105c22.81187,1.98633 45.64146,3.7749 68.48423,5.36572c11.27197,0.78809 23.95166,1.91602 35.18994,2.23242l0.00146,12.5625c0,7.68311 1.00342,16.11475 -6.81006,20.52832c-2.56494,1.4502 -4.82959,1.45752 -7.67725,1.46924l-132.00088,0.01758c-1.37109,-1.00928 -4.05396,-3.87451 -5.22305,-5.22217c-9.83247,-11.34375 -17.83081,-25.69043 -24.554,-39.12012c5.29438,-0.65479 9.75776,-0.94336 15.06782,-1.16455z"/><path d="M79.33345,143.18101c3.42158,-0.12788 6.95186,-0.03823 10.37549,0.01846c23.16064,0.3835 45.99814,4.23413 68.89849,7.51538c26.93994,3.94482 53.93701,7.47803 80.98535,10.59814c0.43652,1.35352 0.84668,2.76123 1.26416,4.12646c-1.89697,-0.30908 -4.96729,-0.50098 -6.95801,-0.68701l-13.2583,-1.22754c-24.26514,-2.3042 -48.51562,-4.771 -72.74854,-7.39893c-28.43335,-3.04834 -58.20586,-6.79687 -86.79185,-3.41748c-0.83701,-2.12695 -1.91279,-4.48975 -2.81426,-6.61245c6.6145,-2.26084 14.11143,-2.65151 21.04746,-2.91504z"/></g></svg>' +
            '</div>' +
            '<div class="flex-1">' +
              '<div class="text-[10px] text-neutral-400 font-medium mb-1">Crewship AI</div>' +
              '<div class="bg-neutral-50 rounded-xl rounded-tl-sm px-3.5 py-2.5 text-sm text-neutral-800 leading-relaxed">' +
                'Ahoj! Jsem <strong>Crewship AI</strong> — vas asistent pro celou platformu. Pomahu s:' +
                '<ul class="mt-2 space-y-1 text-xs text-neutral-600">' +
                  '<li class="flex items-center gap-1.5"><svg class="w-3 h-3 text-brand-teal flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 6 9 17l-5-5"/></svg>Setup a konfigurace agentu</li>' +
                  '<li class="flex items-center gap-1.5"><svg class="w-3 h-3 text-brand-teal flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 6 9 17l-5-5"/></svg>Debugging a reseni chyb</li>' +
                  '<li class="flex items-center gap-1.5"><svg class="w-3 h-3 text-brand-teal flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 6 9 17l-5-5"/></svg>Tvorba custom skills</li>' +
                  '<li class="flex items-center gap-1.5"><svg class="w-3 h-3 text-brand-teal flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 6 9 17l-5-5"/></svg>Otazky k platforme a API</li>' +
                '</ul>' +
              '</div>' +
            '</div>' +
          '</div>' +
          // Suggested prompts
          '<div class="space-y-1.5">' +
            '<div class="text-[10px] text-neutral-400 font-medium uppercase tracking-wider px-1">Rychle otazky</div>' +
            '<button class="w-full text-left px-3 py-2 text-xs bg-white border border-neutral-200 rounded-lg hover:border-primary-300 hover:bg-primary-50/50 transition-colors text-neutral-700">' +
              '<span class="text-primary-600 mr-1.5">→</span>Jak vytvorim noveho agenta?' +
            '</button>' +
            '<button class="w-full text-left px-3 py-2 text-xs bg-white border border-neutral-200 rounded-lg hover:border-primary-300 hover:bg-primary-50/50 transition-colors text-neutral-700">' +
              '<span class="text-primary-600 mr-1.5">→</span>Muj agent hlasi error — pomoz mi debugovat' +
            '</button>' +
            '<button class="w-full text-left px-3 py-2 text-xs bg-white border border-neutral-200 rounded-lg hover:border-primary-300 hover:bg-primary-50/50 transition-colors text-neutral-700">' +
              '<span class="text-primary-600 mr-1.5">→</span>Vygeneruj skill pro web scraping' +
            '</button>' +
            '<button class="w-full text-left px-3 py-2 text-xs bg-white border border-neutral-200 rounded-lg hover:border-primary-300 hover:bg-primary-50/50 transition-colors text-neutral-700">' +
              '<span class="text-primary-600 mr-1.5">→</span>Jak nastavim webhook trigger?' +
            '</button>' +
          '</div>' +
        '</div>' +
        // Input area
        '<div class="px-4 py-3 border-t border-neutral-200 bg-white flex-shrink-0">' +
          '<div class="flex items-center gap-2">' +
            '<input type="text" placeholder="Napiste zpravu..." class="flex-1 px-3 py-2 text-sm border border-neutral-200 rounded-lg bg-neutral-50 focus:outline-none focus:ring-2 focus:ring-primary-500 focus:border-primary-500 focus:bg-white">' +
            '<button class="p-2 rounded-lg text-white flex-shrink-0" style="background:linear-gradient(135deg,#4ECDC4,#1877F2)">' +
              '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3.714 3.048a.498.498 0 0 0-.683.627l2.843 7.627a2 2 0 0 1 0 1.396l-2.842 7.627a.498.498 0 0 0 .682.627l18.168-8.215a.5.5 0 0 0 0-.904z"/><path d="M6 12h16"/></svg>' +
            '</button>' +
          '</div>' +
          '<div class="flex items-center justify-between mt-2">' +
            '<span class="text-[10px] text-neutral-400">Powered by Anthropic Claude</span>' +
            '<span class="text-[10px] text-neutral-400">⌘ + J</span>' +
          '</div>' +
        '</div>' +
      '</div>';

    // Toggle logic
    function openAiPanel() {
      aiOpen = true;
      aiPanel.classList.remove('hidden');
      aiBtn.style.width = '64px';
      aiBtn.style.paddingRight = '0';
      var label = aiBtn.querySelector('.ai-btn-label');
      label.style.maxWidth = '0';
      label.style.opacity = '0';
      aiBtn.querySelector('span:first-child').innerHTML = shipSvgClose;
      aiBtn.classList.remove('ai-btn-glow');
    }
    function closeAiPanel() {
      aiOpen = false;
      aiPanel.classList.add('hidden');
      aiBtn.querySelector('span:first-child').innerHTML = shipSvg;
      aiBtn.classList.add('ai-btn-glow');
    }
    aiBtn.addEventListener('click', function () {
      if (aiOpen) { closeAiPanel(); } else { openAiPanel(); }
    });
    aiPanel.querySelector('#ai-close').addEventListener('click', closeAiPanel);

    // Keyboard shortcut: Cmd+J
    document.addEventListener('keydown', function (e) {
      if ((e.metaKey || e.ctrlKey) && e.key === 'j') {
        e.preventDefault();
        aiBtn.click();
      }
    });

    document.body.appendChild(aiBtn);
    document.body.appendChild(aiPanel);
  });
})();

/* --- Settings page helper (used by 20-settings.html) --- */
var CrewshipSettings = (function () {
    // Small icon helpers for settings content
    var sIcons = {
      user: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>',
      lock: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>',
      palette: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="13.5" cy="6.5" r="0.5" fill="currentColor"/><circle cx="17.5" cy="10.5" r="0.5" fill="currentColor"/><circle cx="8.5" cy="7.5" r="0.5" fill="currentColor"/><circle cx="6.5" cy="12.5" r="0.5" fill="currentColor"/><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.926 0 1.648-.746 1.648-1.688 0-.437-.18-.835-.437-1.125-.29-.289-.438-.652-.438-1.125a1.64 1.64 0 0 1 1.668-1.668h1.996c3.051 0 5.555-2.503 5.555-5.554C21.965 6.012 17.461 2 12 2z"/></svg>',
      bellSm: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/></svg>',
      building: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect width="16" height="20" x="4" y="2" rx="2" ry="2"/><path d="M9 22v-4h6v4"/><path d="M8 6h.01"/><path d="M16 6h.01"/><path d="M12 6h.01"/><path d="M12 10h.01"/><path d="M12 14h.01"/><path d="M16 10h.01"/><path d="M16 14h.01"/><path d="M8 10h.01"/><path d="M8 14h.01"/></svg>',
      users: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>',
      shield: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/></svg>',
      creditCard: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect width="20" height="14" x="2" y="5" rx="2"/><line x1="2" x2="22" y1="10" y2="10"/></svg>',
      key: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m15.5 7.5 2.3 2.3a1 1 0 0 0 1.4 0l2.1-2.1a1 1 0 0 0 0-1.4L19 4"/><path d="m21 2-9.6 9.6"/><circle cx="7.5" cy="15.5" r="5.5"/></svg>',
      webhook: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 16.98h-5.99c-1.1 0-1.95.94-2.48 1.9A4 4 0 0 1 2 17c.01-.7.2-1.4.57-2"/><path d="m6 17 3.13-5.78c.53-.97.1-2.18-.5-3.1a4 4 0 1 1 6.89-4.06"/><path d="m12 6 3.13 5.73C15.66 12.7 16.9 13 18 13a4 4 0 0 1 0 8H12"/></svg>',
      toggleRight: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect width="20" height="12" x="2" y="6" rx="6" ry="6"/><circle cx="16" cy="12" r="2"/></svg>',
      alertTriangle: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>',
      check: '<svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M20 6 9 17l-5-5"/></svg>',
      x: '<svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>',
      upload: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" x2="12" y1="3" y2="15"/></svg>',
      copy: '<svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>',
      trash: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/></svg>',
      download: '<svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/></svg>'
    };

    var userTabs = [
      { key: 'profile', label: 'Profile', icon: 'user' },
      { key: 'password', label: 'Password & Auth', icon: 'lock' },
      { key: 'appearance', label: 'Appearance', icon: 'palette' },
      { key: 'notifications', label: 'Notifications', icon: 'bellSm' }
    ];
    var orgTabs = [
      { key: 'general', label: 'General', icon: 'building' },
      { key: 'members', label: 'Members', icon: 'users' },
      { key: 'roles', label: 'Roles & Permissions', icon: 'shield' },
      { key: 'billing', label: 'Billing', icon: 'creditCard' },
      { key: 'apikeys', label: 'API Keys', icon: 'key', phase2: true },
      { key: 'webhooks', label: 'Webhooks', icon: 'webhook', phase2: true },
      { key: 'flags', label: 'Feature Flags', icon: 'toggleRight', ownerOnly: true },
      { key: 'danger', label: 'Danger Zone', icon: 'alertTriangle', ownerOnly: true }
    ];

    // --- Toggle switch helper ---
    function toggleHtml(id, label, checked, description) {
      return '<div class="flex items-center justify-between py-2.5">' +
        '<div class="flex-1 min-w-0">' +
          '<div class="text-sm text-neutral-800">' + label + '</div>' +
          (description ? '<div class="text-xs text-neutral-400 mt-0.5">' + description + '</div>' : '') +
        '</div>' +
        '<div class="relative flex-shrink-0 ml-3">' +
          '<div class="w-9 h-5 rounded-full ' + (checked ? 'bg-primary-600' : 'bg-neutral-300') + ' cursor-pointer">' +
            '<div class="absolute top-0.5 ' + (checked ? 'left-4' : 'left-0.5') + ' w-4 h-4 rounded-full bg-white shadow-sm transition-all"></div>' +
          '</div>' +
        '</div>' +
      '</div>';
    }

    // --- Form field helper ---
    function fieldHtml(label, value, opts) {
      opts = opts || {};
      var readonly = opts.readonly ? ' readonly' : '';
      var cls = opts.readonly ? 'bg-neutral-50 text-neutral-500 cursor-not-allowed' : 'bg-white focus:ring-2 focus:ring-primary-500 focus:border-primary-500';
      if (opts.type === 'textarea') {
        return '<div class="mb-4"><label class="block text-xs font-medium text-neutral-600 mb-1.5">' + label + '</label>' +
          '<textarea class="w-full px-3 py-2 text-sm border border-neutral-200 rounded-lg ' + cls + ' focus:outline-none" rows="3"' + readonly + '>' + (value || '') + '</textarea></div>';
      }
      if (opts.type === 'select') {
        var optHtml = '';
        (opts.options || []).forEach(function (o) {
          optHtml += '<option' + (o === value ? ' selected' : '') + '>' + o + '</option>';
        });
        return '<div class="mb-4"><label class="block text-xs font-medium text-neutral-600 mb-1.5">' + label + '</label>' +
          '<select class="w-full px-3 py-2 text-sm border border-neutral-200 rounded-lg bg-white focus:outline-none focus:ring-2 focus:ring-primary-500">' + optHtml + '</select></div>';
      }
      return '<div class="mb-4"><label class="block text-xs font-medium text-neutral-600 mb-1.5">' + label + '</label>' +
        '<input type="' + (opts.type || 'text') + '" value="' + (value || '') + '" class="w-full px-3 py-2 text-sm border border-neutral-200 rounded-lg ' + cls + ' focus:outline-none"' + readonly + '></div>';
    }

    // --- Tab content builders ---
    var tabContent = {
      // === USER TABS ===
      profile: function () {
        return '<div class="space-y-5">' +
          '<div class="flex items-center gap-4 pb-4 border-b border-neutral-100">' +
            '<div class="w-16 h-16 rounded-full bg-primary-600 flex items-center justify-center text-white text-xl font-semibold">PS</div>' +
            '<div>' +
              '<div class="text-sm font-medium text-neutral-900">Pavel Srba</div>' +
              '<div class="text-xs text-neutral-400 mb-2">Member since Jan 2026</div>' +
              '<button class="flex items-center gap-1.5 text-xs text-primary-600 hover:text-primary-700 font-medium">' + sIcons.upload + ' Upload photo</button>' +
            '</div>' +
          '</div>' +
          fieldHtml('Full Name', 'Pavel Srba') +
          fieldHtml('Email', 'pavel@unifylab.cz', { readonly: true }) +
          fieldHtml('Timezone', 'Europe/Prague (UTC+1)', { type: 'select', options: ['Europe/Prague (UTC+1)', 'Europe/London (UTC+0)', 'America/New_York (UTC-5)', 'America/Los_Angeles (UTC-8)', 'Asia/Tokyo (UTC+9)'] }) +
          '<button class="px-4 py-2 text-sm font-medium text-white bg-primary-600 rounded-lg hover:bg-primary-700">Save Changes</button>' +
        '</div>';
      },
      password: function () {
        return '<div class="space-y-5">' +
          '<div class="pb-4 border-b border-neutral-100">' +
            '<div class="text-sm font-medium text-neutral-900 mb-1">Change Password</div>' +
            '<div class="text-xs text-neutral-400">Update your password to keep your account secure.</div>' +
          '</div>' +
          fieldHtml('Current Password', '', { type: 'password' }) +
          fieldHtml('New Password', '', { type: 'password' }) +
          fieldHtml('Confirm New Password', '', { type: 'password' }) +
          '<button class="px-4 py-2 text-sm font-medium text-white bg-primary-600 rounded-lg hover:bg-primary-700">Update Password</button>' +
          '<div class="pt-5 mt-5 border-t border-neutral-100">' +
            '<div class="text-sm font-medium text-neutral-900 mb-3">Connected Accounts</div>' +
            '<div class="space-y-2.5">' +
              '<div class="flex items-center justify-between p-3 border border-neutral-200 rounded-lg">' +
                '<div class="flex items-center gap-3">' +
                  '<div class="w-8 h-8 rounded-full bg-white border border-neutral-200 flex items-center justify-center"><svg class="w-4 h-4" viewBox="0 0 24 24"><path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 01-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z" fill="#4285F4"/><path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853"/><path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" fill="#FBBC05"/><path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#EA4335"/></svg></div>' +
                  '<div><div class="text-sm text-neutral-800">Google</div><div class="text-xs text-neutral-400">pavel@unifylab.cz</div></div>' +
                '</div>' +
                '<span class="text-xs text-success-700 bg-success-50 px-2 py-0.5 rounded-full font-medium">Connected</span>' +
              '</div>' +
              '<div class="flex items-center justify-between p-3 border border-neutral-200 rounded-lg">' +
                '<div class="flex items-center gap-3">' +
                  '<div class="w-8 h-8 rounded-full bg-neutral-900 flex items-center justify-center"><svg class="w-4 h-4 text-white" viewBox="0 0 24 24" fill="currentColor"><path d="M12 0c-6.626 0-12 5.373-12 12 0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23.957-.266 1.983-.399 3.003-.404 1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576 4.765-1.589 8.199-6.086 8.199-11.386 0-6.627-5.373-12-12-12z"/></svg></div>' +
                  '<div><div class="text-sm text-neutral-800">GitHub</div><div class="text-xs text-neutral-400">Not connected</div></div>' +
                '</div>' +
                '<button class="text-xs text-primary-600 hover:text-primary-700 font-medium">Connect</button>' +
              '</div>' +
            '</div>' +
          '</div>' +
        '</div>';
      },
      appearance: function () {
        return '<div class="space-y-5">' +
          '<div class="pb-4 border-b border-neutral-100">' +
            '<div class="text-sm font-medium text-neutral-900 mb-1">Theme</div>' +
            '<div class="text-xs text-neutral-400">Select your preferred color scheme.</div>' +
          '</div>' +
          '<div class="grid grid-cols-3 gap-3">' +
            '<button class="flex flex-col items-center gap-2 p-3 border-2 border-primary-500 rounded-lg bg-primary-50/50">' +
              '<div class="w-full h-12 rounded-md bg-white border border-neutral-200 flex items-center justify-center"><div class="w-6 h-1 bg-neutral-300 rounded"></div></div>' +
              '<span class="text-xs font-medium text-primary-700">Light</span>' +
            '</button>' +
            '<button class="flex flex-col items-center gap-2 p-3 border border-neutral-200 rounded-lg hover:border-neutral-300">' +
              '<div class="w-full h-12 rounded-md bg-neutral-900 border border-neutral-700 flex items-center justify-center"><div class="w-6 h-1 bg-neutral-600 rounded"></div></div>' +
              '<span class="text-xs text-neutral-600">Dark</span>' +
            '</button>' +
            '<button class="flex flex-col items-center gap-2 p-3 border border-neutral-200 rounded-lg hover:border-neutral-300">' +
              '<div class="w-full h-12 rounded-md bg-gradient-to-r from-white to-neutral-900 border border-neutral-200 flex items-center justify-center"><div class="w-6 h-1 bg-neutral-400 rounded"></div></div>' +
              '<span class="text-xs text-neutral-600">System</span>' +
            '</button>' +
          '</div>' +
          '<div class="pt-4 border-t border-neutral-100">' +
            fieldHtml('Language', 'Cestina (CZ)', { type: 'select', options: ['Cestina (CZ)', 'English (EN)'] }) +
          '</div>' +
        '</div>';
      },
      notifications: function () {
        return '<div class="space-y-1">' +
          '<div class="pb-3 mb-2 border-b border-neutral-100">' +
            '<div class="text-sm font-medium text-neutral-900 mb-1">Email Notifications</div>' +
            '<div class="text-xs text-neutral-400">Choose what emails you want to receive.</div>' +
          '</div>' +
          toggleHtml('n1', 'Agent errors & failures', true, 'Get notified when an agent encounters an error or stops unexpectedly.') +
          '<div class="border-t border-neutral-50"></div>' +
          toggleHtml('n2', 'Invitation emails', true, 'Receive emails when you are invited to an organization.') +
          '<div class="border-t border-neutral-50"></div>' +
          toggleHtml('n3', 'Weekly digest', false, 'Summary of agent activity, runs, and usage stats every Monday.') +
          '<div class="border-t border-neutral-50"></div>' +
          toggleHtml('n4', 'Crew completion', true, 'Notified when a crew pipeline finishes running.') +
          '<div class="border-t border-neutral-50"></div>' +
          toggleHtml('n5', 'Security alerts', true, 'Credential expiration, failed login attempts, new device logins.') +
        '</div>';
      },
      // === ORG TABS ===
      general: function () {
        return '<div class="space-y-5">' +
          '<div class="flex items-center gap-4 pb-4 border-b border-neutral-100">' +
            '<div class="w-14 h-14 rounded-xl bg-primary-600 flex items-center justify-center text-white text-lg font-bold">U</div>' +
            '<div>' +
              '<div class="text-sm font-medium text-neutral-900">Unify Technology</div>' +
              '<div class="text-xs text-neutral-400 mb-2">Created Dec 2025</div>' +
              '<button class="flex items-center gap-1.5 text-xs text-primary-600 hover:text-primary-700 font-medium">' + sIcons.upload + ' Change logo</button>' +
            '</div>' +
          '</div>' +
          fieldHtml('Organization Name', 'Unify Technology') +
          fieldHtml('Slug', 'unify-technology', { readonly: true }) +
          fieldHtml('Description', 'AI agent orchestration for our internal workflows.', { type: 'textarea' }) +
          fieldHtml('Default Container TTL', '24 hours', { type: 'select', options: ['1 hour', '4 hours', '12 hours', '24 hours', '48 hours', '7 days', 'Never'] }) +
          '<button class="px-4 py-2 text-sm font-medium text-white bg-primary-600 rounded-lg hover:bg-primary-700">Save Changes</button>' +
        '</div>';
      },
      members: function () {
        var members = [
          { name: 'Pavel Srba', email: 'pavel@unifylab.cz', role: 'Owner', initials: 'PS', joined: 'Dec 2025' },
          { name: 'Jan Novak', email: 'jan@unifylab.cz', role: 'Admin', initials: 'JN', joined: 'Jan 2026' },
          { name: 'Marie Kralova', email: 'marie@unifylab.cz', role: 'Member', initials: 'MK', joined: 'Feb 2026' }
        ];
        var roleCls = { Owner: 'bg-warning-50 text-warning-700', Admin: 'bg-primary-100 text-primary-700', Manager: 'bg-brand-teal/10 text-teal-700', Member: 'bg-neutral-100 text-neutral-600', Viewer: 'bg-neutral-50 text-neutral-400' };
        var rows = '';
        members.forEach(function (m) {
          rows += '<div class="flex items-center justify-between py-3 border-b border-neutral-50">' +
            '<div class="flex items-center gap-3">' +
              '<div class="w-8 h-8 rounded-full bg-primary-600 flex items-center justify-center text-white text-[10px] font-semibold">' + m.initials + '</div>' +
              '<div><div class="text-sm text-neutral-800">' + m.name + '</div><div class="text-xs text-neutral-400">' + m.email + '</div></div>' +
            '</div>' +
            '<div class="flex items-center gap-3">' +
              '<span class="text-[10px] px-2 py-0.5 rounded-full font-medium ' + (roleCls[m.role] || '') + '">' + m.role + '</span>' +
              '<span class="text-[10px] text-neutral-400">' + m.joined + '</span>' +
              (m.role !== 'Owner' ? '<button class="text-neutral-400 hover:text-neutral-600">' + sIcons.x + '</button>' : '') +
            '</div>' +
          '</div>';
        });

        return '<div class="space-y-4">' +
          '<div class="flex items-center justify-between">' +
            '<div><div class="text-sm font-medium text-neutral-900">Members</div><div class="text-xs text-neutral-400">3 of 5 seats used</div></div>' +
            '<button class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-primary-600 rounded-lg hover:bg-primary-700">' + sIcons.users + ' Invite</button>' +
          '</div>' +
          '<div class="border border-neutral-200 rounded-lg overflow-hidden">' +
            '<div class="px-3">' + rows + '</div>' +
          '</div>' +
          '<div class="pt-3 border-t border-neutral-100">' +
            '<div class="text-xs font-medium text-neutral-600 mb-2">Pending Invitations</div>' +
            '<div class="flex items-center justify-between p-3 border border-dashed border-neutral-300 rounded-lg bg-neutral-50">' +
              '<div><div class="text-sm text-neutral-600">tomas@unifylab.cz</div><div class="text-[10px] text-neutral-400">Invited as Manager — 2 days ago</div></div>' +
              '<div class="flex items-center gap-2">' +
                '<button class="text-xs text-primary-600 hover:text-primary-700 font-medium">Resend</button>' +
                '<button class="text-xs text-error-600 hover:text-error-700 font-medium">Revoke</button>' +
              '</div>' +
            '</div>' +
          '</div>' +
        '</div>';
      },
      roles: function () {
        var perms = ['See all teams', 'Create agents', 'Manage creds', 'Audit access', 'Manage members', 'Billing'];
        var roles = [
          { name: 'Owner', perms: [true, true, true, 'All', true, true] },
          { name: 'Admin', perms: [true, true, true, 'All', true, true] },
          { name: 'Manager', perms: [false, true, 'Team', 'Team', false, false] },
          { name: 'Member', perms: [false, false, false, 'Own', false, false] },
          { name: 'Viewer', perms: [false, false, false, false, false, false] }
        ];
        var thead = '<th class="text-left text-[10px] font-medium text-neutral-400 pb-2 pr-2 w-24">Role</th>';
        perms.forEach(function (p) { thead += '<th class="text-center text-[10px] font-medium text-neutral-400 pb-2 px-1">' + p + '</th>'; });
        var tbody = '';
        roles.forEach(function (r) {
          var roleCls = r.name === 'Owner' ? 'text-warning-700 font-medium' : r.name === 'Admin' ? 'text-primary-700 font-medium' : 'text-neutral-700';
          tbody += '<tr class="border-t border-neutral-50"><td class="py-2.5 pr-2 text-xs ' + roleCls + '">' + r.name + '</td>';
          r.perms.forEach(function (v) {
            var cell;
            if (v === true) cell = '<span class="text-success-600">' + sIcons.check + '</span>';
            else if (v === false) cell = '<span class="text-neutral-300">' + sIcons.x + '</span>';
            else cell = '<span class="text-[10px] text-neutral-500">' + v + '</span>';
            tbody += '<td class="py-2.5 px-1 text-center"><div class="flex justify-center">' + cell + '</div></td>';
          });
          tbody += '</tr>';
        });
        return '<div class="space-y-4">' +
          '<div class="pb-3 border-b border-neutral-100">' +
            '<div class="text-sm font-medium text-neutral-900 mb-1">Permission Matrix</div>' +
            '<div class="text-xs text-neutral-400">Reference of what each role can do. Roles are assigned per member.</div>' +
          '</div>' +
          '<div class="overflow-x-auto">' +
            '<table class="w-full"><thead><tr>' + thead + '</tr></thead><tbody>' + tbody + '</tbody></table>' +
          '</div>' +
        '</div>';
      },
      billing: function () {
        return '<div class="space-y-5">' +
          '<div class="px-4 py-2.5 bg-warning-50 border border-warning-500/20 rounded-lg flex items-center gap-2">' +
            sIcons.alertTriangle.replace('w-4 h-4', 'w-3.5 h-3.5 text-warning-700 flex-shrink-0') +
            '<span class="text-xs text-warning-700 font-medium">Billing is controlled by the <code class="bg-warning-100 px-1 rounded">billing_enabled</code> feature flag. Currently disabled.</span>' +
          '</div>' +
          '<div class="p-4 border border-neutral-200 rounded-lg">' +
            '<div class="flex items-center justify-between mb-3">' +
              '<div><div class="text-sm font-medium text-neutral-900">Current Plan</div><div class="text-xs text-neutral-400">Unify Technology</div></div>' +
              '<span class="text-xs bg-primary-100 text-primary-700 px-2.5 py-1 rounded-full font-semibold">FREE</span>' +
            '</div>' +
            '<div class="space-y-2">' +
              '<div class="flex items-center justify-between text-xs"><span class="text-neutral-500">Agents</span><span class="text-neutral-800 font-medium">5 / 5</span></div>' +
              '<div class="w-full h-1.5 bg-neutral-100 rounded-full"><div class="h-1.5 bg-primary-500 rounded-full" style="width:100%"></div></div>' +
              '<div class="flex items-center justify-between text-xs"><span class="text-neutral-500">Teams</span><span class="text-neutral-800 font-medium">1 / 2</span></div>' +
              '<div class="w-full h-1.5 bg-neutral-100 rounded-full"><div class="h-1.5 bg-primary-500 rounded-full" style="width:50%"></div></div>' +
              '<div class="flex items-center justify-between text-xs"><span class="text-neutral-500">Members</span><span class="text-neutral-800 font-medium">3 / 5</span></div>' +
              '<div class="w-full h-1.5 bg-neutral-100 rounded-full"><div class="h-1.5 bg-primary-500 rounded-full" style="width:60%"></div></div>' +
            '</div>' +
          '</div>' +
          '<div class="text-xs font-medium text-neutral-600 mb-2">Available Plans</div>' +
          '<div class="grid grid-cols-2 gap-2.5">' +
            '<div class="p-3 border border-primary-300 bg-primary-50/30 rounded-lg"><div class="text-xs font-semibold text-primary-700 mb-1">Free</div><div class="text-lg font-bold text-neutral-900">$0<span class="text-xs font-normal text-neutral-400">/mo</span></div><div class="text-[10px] text-neutral-500 mt-1">5 agents, 2 teams</div><div class="mt-2 text-[10px] text-primary-600 font-medium">Current plan</div></div>' +
            '<div class="p-3 border border-neutral-200 rounded-lg hover:border-primary-300 cursor-pointer"><div class="text-xs font-semibold text-neutral-800 mb-1">Pro</div><div class="text-lg font-bold text-neutral-900">$29<span class="text-xs font-normal text-neutral-400">/mo</span></div><div class="text-[10px] text-neutral-500 mt-1">20 agents, 5 teams</div><button class="mt-2 text-[10px] text-primary-600 font-medium hover:text-primary-700">Upgrade</button></div>' +
            '<div class="p-3 border border-neutral-200 rounded-lg hover:border-primary-300 cursor-pointer"><div class="text-xs font-semibold text-neutral-800 mb-1">Team</div><div class="text-lg font-bold text-neutral-900">$79<span class="text-xs font-normal text-neutral-400">/mo</span></div><div class="text-[10px] text-neutral-500 mt-1">100 agents, unlimited</div><button class="mt-2 text-[10px] text-primary-600 font-medium hover:text-primary-700">Upgrade</button></div>' +
            '<div class="p-3 border border-neutral-200 rounded-lg hover:border-primary-300 cursor-pointer"><div class="text-xs font-semibold text-neutral-800 mb-1">Enterprise</div><div class="text-lg font-bold text-neutral-900">Custom</div><div class="text-[10px] text-neutral-500 mt-1">Unlimited everything</div><button class="mt-2 text-[10px] text-primary-600 font-medium hover:text-primary-700">Contact Sales</button></div>' +
          '</div>' +
        '</div>';
      },
      apikeys: function () {
        return '<div class="space-y-4">' +
          '<div class="flex items-center justify-between">' +
            '<div><div class="text-sm font-medium text-neutral-900">API Keys</div><div class="text-xs text-neutral-400">Programmatic access to the Crewship API.</div></div>' +
            '<button class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-primary-600 rounded-lg hover:bg-primary-700">' + sIcons.key + ' Create Key</button>' +
          '</div>' +
          '<div class="border border-neutral-200 rounded-lg overflow-hidden">' +
            '<div class="px-3">' +
              '<div class="flex items-center justify-between py-3 border-b border-neutral-50">' +
                '<div class="flex items-center gap-3">' +
                  '<div class="w-8 h-8 rounded-lg bg-neutral-100 flex items-center justify-center">' + sIcons.key + '</div>' +
                  '<div><div class="text-sm text-neutral-800">Production API Key</div><div class="text-[10px] text-neutral-400 font-mono">ck_live_a1b2...x9z0</div></div>' +
                '</div>' +
                '<div class="flex items-center gap-3">' +
                  '<span class="text-[10px] text-neutral-400">Last used 2h ago</span>' +
                  '<button class="text-xs text-error-600 hover:text-error-700 font-medium">Revoke</button>' +
                '</div>' +
              '</div>' +
              '<div class="flex items-center justify-between py-3">' +
                '<div class="flex items-center gap-3">' +
                  '<div class="w-8 h-8 rounded-lg bg-neutral-100 flex items-center justify-center">' + sIcons.key + '</div>' +
                  '<div><div class="text-sm text-neutral-800">CI/CD Pipeline</div><div class="text-[10px] text-neutral-400 font-mono">ck_live_c3d4...w7v8</div></div>' +
                '</div>' +
                '<div class="flex items-center gap-3">' +
                  '<span class="text-[10px] text-neutral-400">Last used 5d ago</span>' +
                  '<button class="text-xs text-error-600 hover:text-error-700 font-medium">Revoke</button>' +
                '</div>' +
              '</div>' +
            '</div>' +
          '</div>' +
          '<div class="p-3 bg-neutral-50 border border-neutral-200 rounded-lg">' +
            '<div class="text-xs text-neutral-500">API keys provide full access to your organization. Treat them like passwords. Keys are shown only once at creation.</div>' +
          '</div>' +
        '</div>';
      },
      webhooks: function () {
        return '<div class="space-y-5">' +
          '<div class="pb-4 border-b border-neutral-100">' +
            '<div class="text-sm font-medium text-neutral-900 mb-1">Webhook Configuration</div>' +
            '<div class="text-xs text-neutral-400">Global webhook settings for external integrations. Per-agent webhooks are configured in agent settings.</div>' +
          '</div>' +
          fieldHtml('Default Webhook URL', 'https://hooks.example.com/crewship') +
          '<div class="mb-4">' +
            '<label class="block text-xs font-medium text-neutral-600 mb-1.5">Secret Token</label>' +
            '<div class="flex gap-2">' +
              '<input type="password" value="whsec_a1b2c3d4e5f6" class="flex-1 px-3 py-2 text-sm border border-neutral-200 rounded-lg bg-white font-mono focus:outline-none focus:ring-2 focus:ring-primary-500">' +
              '<button class="px-3 py-2 text-xs border border-neutral-200 rounded-lg hover:bg-neutral-50">' + sIcons.copy + '</button>' +
            '</div>' +
          '</div>' +
          '<div class="mb-4">' +
            '<label class="block text-xs font-medium text-neutral-600 mb-2">Events to Send</label>' +
            '<div class="space-y-1.5">' +
              toggleHtml('we1', 'agent.started', true, '') +
              toggleHtml('we2', 'agent.stopped', true, '') +
              toggleHtml('we3', 'agent.error', true, '') +
              toggleHtml('we4', 'run.completed', false, '') +
              toggleHtml('we5', 'crew.pipeline.finished', false, '') +
              toggleHtml('we6', 'member.invited', false, '') +
            '</div>' +
          '</div>' +
          '<button class="px-4 py-2 text-sm font-medium text-white bg-primary-600 rounded-lg hover:bg-primary-700">Save Webhook Settings</button>' +
        '</div>';
      },
      flags: function () {
        var flags = [
          { key: 'billing_enabled', desc: 'Enable Stripe billing (enterprise only)', on: false },
          { key: 'marketplace_enabled', desc: 'Enable skills marketplace', on: false },
          { key: 'orchestration', desc: 'Enable multi-agent orchestration', on: true },
          { key: 'task_mode', desc: 'Enable async task mode', on: false },
          { key: 'config_history', desc: 'Enable agent config versioning', on: true },
          { key: 'advanced_audit', desc: 'Enable advanced audit log UI + export', on: false }
        ];
        var flagsHtml = '';
        flags.forEach(function (f) {
          flagsHtml += '<div class="flex items-center justify-between py-3 border-b border-neutral-50">' +
            '<div class="flex-1 min-w-0">' +
              '<div class="text-sm font-mono text-neutral-800">' + f.key + '</div>' +
              '<div class="text-xs text-neutral-400 mt-0.5">' + f.desc + '</div>' +
            '</div>' +
            '<div class="flex items-center gap-2 ml-3 flex-shrink-0">' +
              '<span class="text-[10px] ' + (f.on ? 'text-success-600' : 'text-neutral-400') + '">' + (f.on ? 'ON' : 'OFF') + '</span>' +
              '<div class="w-9 h-5 rounded-full ' + (f.on ? 'bg-primary-600' : 'bg-neutral-300') + ' cursor-pointer relative">' +
                '<div class="absolute top-0.5 ' + (f.on ? 'left-4' : 'left-0.5') + ' w-4 h-4 rounded-full bg-white shadow-sm"></div>' +
              '</div>' +
            '</div>' +
          '</div>';
        });
        return '<div class="space-y-3">' +
          '<div class="pb-3 border-b border-neutral-100">' +
            '<div class="flex items-center gap-2 mb-1"><div class="text-sm font-medium text-neutral-900">Feature Flags</div><span class="text-[9px] bg-warning-50 text-warning-700 border border-warning-500/30 px-1.5 py-0.5 rounded font-medium">OWNER ONLY</span></div>' +
            '<div class="text-xs text-neutral-400">Toggle platform features for this organization. Changes take effect immediately.</div>' +
          '</div>' +
          flagsHtml +
        '</div>';
      },
      danger: function () {
        return '<div class="space-y-5">' +
          '<div class="p-4 border-2 border-error-200 rounded-lg bg-error-50/30">' +
            '<div class="flex items-center gap-2 mb-2">' +
              '<span class="text-error-600">' + sIcons.alertTriangle + '</span>' +
              '<div class="text-sm font-semibold text-error-800">Danger Zone</div>' +
            '</div>' +
            '<div class="text-xs text-error-700/80 mb-4">These actions are irreversible. Proceed with extreme caution.</div>' +
            '<div class="space-y-4">' +
              '<div class="flex items-center justify-between p-3 bg-white border border-error-200 rounded-lg">' +
                '<div>' +
                  '<div class="text-sm font-medium text-neutral-900">Delete Organization</div>' +
                  '<div class="text-xs text-neutral-500">Permanently delete Unify Technology, all agents, teams, credentials, and data.</div>' +
                  '<div class="text-[10px] text-neutral-400 mt-1">Rate limit: 1 deletion per 24 hours</div>' +
                '</div>' +
                '<button class="ml-4 px-3 py-1.5 text-xs font-medium text-white bg-error-600 rounded-lg hover:bg-error-700 flex-shrink-0">Delete Org</button>' +
              '</div>' +
              '<div class="flex items-center justify-between p-3 bg-white border border-neutral-200 rounded-lg">' +
                '<div>' +
                  '<div class="text-sm font-medium text-neutral-900">Export All Data</div>' +
                  '<div class="text-xs text-neutral-500">Download a ZIP archive of all organization data (agents, configs, audit logs, conversations).</div>' +
                  '<div class="text-[10px] text-neutral-400 mt-1">Rate limit: 3 exports per hour</div>' +
                '</div>' +
                '<button class="ml-4 px-3 py-1.5 text-xs font-medium text-neutral-700 bg-white border border-neutral-300 rounded-lg hover:bg-neutral-50 flex-shrink-0 flex items-center gap-1.5">' + sIcons.download + ' Export</button>' +
              '</div>' +
            '</div>' +
          '</div>' +
        '</div>';
      }
    };

    return { sIcons: sIcons, userTabs: userTabs, orgTabs: orgTabs, tabContent: tabContent };
})();
