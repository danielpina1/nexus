// SF-Symbol-like icons (hand-drawn approximations, 24×24) used by the sketch.
// Usage: <i class="sf" data-i="house.fill"></i>  — replaced on load.
(function () {
  const S = (d, w = 1.8) =>
    `<g fill="none" stroke="currentColor" stroke-width="${w}" stroke-linecap="round" stroke-linejoin="round">${d}</g>`;
  const F = (d) => `<g fill="currentColor">${d}</g>`;
  const FE = (d) => `<path fill="currentColor" fill-rule="evenodd" d="${d}"/>`;

  const gear =
    "M12 8.6a3.4 3.4 0 1 0 0 6.8 3.4 3.4 0 0 0 0-6.8Z M10.3 2.5h3.4l.5 2.4c.6.2 1.2.5 1.7.9l2.3-.8 1.7 2.9-1.8 1.6c.1.6.1 1.3 0 1.9l1.8 1.6-1.7 2.9-2.3-.8c-.5.4-1.1.7-1.7.9l-.5 2.4h-3.4l-.5-2.4c-.6-.2-1.2-.5-1.7-.9l-2.3.8-1.7-2.9 1.8-1.6a6 6 0 0 1 0-1.9L4.2 8.9l1.7-2.9 2.3.8c.5-.4 1.1-.7 1.7-.9Z";

  const I = {
    // tabs
    "house.fill": F('<path d="M12 2.8 1.9 11.3a.9.9 0 0 0 1.2 1.4L4.6 11.5V20a1.2 1.2 0 0 0 1.2 1.2h4.1v-6.1h4.2v6.1h4.1a1.2 1.2 0 0 0 1.2-1.2v-8.5l1.5 1.2a.9.9 0 0 0 1.2-1.4Z"/>'),
    "bubble.left.and.bubble.right.fill": F('<path d="M3.2 3h10.6A2.2 2.2 0 0 1 16 5.2v6.1a2.2 2.2 0 0 1-2.2 2.2H8.2L4.6 16.4v-2.9H3.2A2.2 2.2 0 0 1 1 11.3V5.2A2.2 2.2 0 0 1 3.2 3Z"/><path d="M17.6 7.4h3.2A2.2 2.2 0 0 1 23 9.6v6.1a2.2 2.2 0 0 1-2.2 2.2h-1.3v2.9l-3.6-2.9h-5.6A2.2 2.2 0 0 1 8.1 15.7v-.6h5.7a3.8 3.8 0 0 0 3.8-3.8Z"/>'),
    "list.bullet.rectangle.portrait.fill": FE("M7 1.8h10a3 3 0 0 1 3 3v14.4a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3V4.8a3 3 0 0 1 3-3Z M7.4 6.6h1.8v1.8H7.4Z M10.8 6.9h5.8v1.3h-5.8Z M7.4 10.6h1.8v1.8H7.4Z M10.8 10.9h5.8v1.3h-5.8Z M7.4 14.6h1.8v1.8H7.4Z M10.8 14.9h5.8v1.3h-5.8Z"),
    "books.vertical.fill": F('<rect x="2.5" y="3.5" width="4.6" height="17" rx="1.1"/><rect x="8.3" y="3.5" width="4.6" height="17" rx="1.1"/><path d="m14.6 5.2 3.6-1a1 1 0 0 1 1.2.7l3.9 14a1 1 0 0 1-.7 1.2l-3.6 1a1 1 0 0 1-1.2-.7l-3.9-14a1 1 0 0 1 .7-1.2Z"/>'),
    desktopcomputer: S('<rect x="2.4" y="3.2" width="19.2" height="13.2" rx="2"/><path d="M9.2 20.6h5.6M12 16.4v4.2"/>'),
    gearshape: `<path fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" d="${gear}"/>`,
    "gearshape.2": `<path fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" transform="translate(-1 1) scale(.78)" d="${gear}"/><path fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" transform="translate(12.2 1) scale(.5)" d="${gear}"/>`,

    // status
    "sun.max.fill": F('<circle cx="12" cy="12" r="4.6"/>') + S('<path d="M12 1.8v2.4M12 19.8v2.4M1.8 12h2.4M19.8 12h2.4M4.8 4.8l1.7 1.7M17.5 17.5l1.7 1.7M4.8 19.2l1.7-1.7M17.5 6.5l1.7-1.7"/>', 2),
    faceid: S('<path d="M3 8V5.5A2.5 2.5 0 0 1 5.5 3H8M16 3h2.5A2.5 2.5 0 0 1 21 5.5V8M21 16v2.5a2.5 2.5 0 0 1-2.5 2.5H16M8 21H5.5A2.5 2.5 0 0 1 3 18.5V16M8.5 8.6v1.6M15.5 8.6v1.6M12.2 8.6v4.6h-1M9 16.2c1.8 1.5 4.2 1.5 6 0"/>'),
    "hand.raised.fill": F('<path d="M7.6 11.3V5.2a1.5 1.5 0 0 1 3 0v5.3h.4V3.6a1.5 1.5 0 0 1 3 0v6.9h.4V4.6a1.5 1.5 0 0 1 3 0v7.6h.3V8.4a1.5 1.5 0 0 1 3 0v6.4c0 4.6-3.2 7.8-7.5 7.8-3.4 0-5.3-1.7-6.9-4.6L3.5 14a1.5 1.5 0 0 1 2.4-1.7Z"/>'),
    "questionmark.bubble.fill": FE("M4.5 2.8h15A2.5 2.5 0 0 1 22 5.3v10a2.5 2.5 0 0 1-2.5 2.5H11l-5 3.8v-3.8H4.5A2.5 2.5 0 0 1 2 15.3v-10a2.5 2.5 0 0 1 2.5-2.5Z M12 5.8c-2 0-3.3 1.2-3.4 2.9h2c.1-.7.6-1.2 1.4-1.2s1.3.5 1.3 1.1c0 .6-.3.9-1 1.4-.9.6-1.4 1.2-1.3 2.3v.4h1.9v-.3c0-.6.3-.9 1-1.4.9-.6 1.5-1.3 1.5-2.4 0-1.6-1.4-2.8-3.4-2.8Z M11 13.6h2v2h-2Z"),
    "exclamationmark.triangle.fill": FE("M10.3 3.2a2 2 0 0 1 3.4 0l8.6 15a2 2 0 0 1-1.7 3H3.4a2 2 0 0 1-1.7-3Z M11 8.3h2l-.2 6.4h-1.6Z M12 16.2a1.2 1.2 0 1 1 0 2.4 1.2 1.2 0 0 1 0-2.4Z"),
    "checkmark.seal.fill": FE("M12 1.6l2.5 1.9 3.1-.3 1 3 2.8 1.4-.6 3.1 1.6 2.7-2.2 2.2-.2 3.1-3.1.6-1.7 2.6-2.9-1.2-2.9 1.2-1.7-2.6-3.1-.6-.2-3.1L1.7 13.4l1.6-2.7-.6-3.1 2.8-1.4 1-3 3.1.3Z M16.6 8.5l-1.4-1.3-4.6 5-2.4-2.3-1.3 1.4 3.8 3.6Z"),
    "checkmark.seal": S('<path d="M12 2.2l2.4 1.8 3-.3 1 2.9 2.7 1.3-.6 3 1.5 2.6-2.1 2.1-.2 3-3 .6-1.6 2.5-2.8-1.2-2.8 1.2-1.6-2.5-3-.6-.2-3-2.1-2.1 1.5-2.6-.6-3 2.7-1.3 1-2.9 3 .3Z"/><path d="m8.2 12 2.6 2.5 5-5.2"/>', 1.6),
    "play.circle.fill": FE("M12 1.8a10.2 10.2 0 1 1 0 20.4 10.2 10.2 0 0 1 0-20.4Z M10 7.6v8.8l7-4.4Z"),
    clock: S('<circle cx="12" cy="12" r="9.6"/><path d="M12 6.6V12l3.6 2.2"/>'),
    "checkmark.circle.fill": FE("M12 1.8a10.2 10.2 0 1 1 0 20.4 10.2 10.2 0 0 1 0-20.4Z M17 8.6l-1.3-1.2-5.3 5.7-2.6-2.5-1.3 1.3 3.9 3.8Z"),
    "checkmark.circle": S('<circle cx="12" cy="12" r="9.6"/><path d="m7.6 12.2 3 2.9 5.8-6.2"/>'),
    "xmark.circle": S('<circle cx="12" cy="12" r="9.6"/><path d="m8.6 8.6 6.8 6.8M15.4 8.6l-6.8 6.8"/>'),
    "xmark.octagon.fill": FE("M8 2h8l6 6v8l-6 6H8l-6-6V8Z M9 7.6 7.6 9l3 3-3 3L9 16.4l3-3 3 3 1.4-1.4-3-3 3-3L15 7.6l-3 3Z"),
    "stop.circle.fill": FE("M12 1.8a10.2 10.2 0 1 1 0 20.4 10.2 10.2 0 0 1 0-20.4Z M8.4 8.4h7.2v7.2H8.4Z"),
    "stop.circle": S('<circle cx="12" cy="12" r="9.6"/><rect x="8.6" y="8.6" width="6.8" height="6.8" rx="1"/>'),
    "stop.fill": F('<rect x="5" y="5" width="14" height="14" rx="2.2"/>'),
    "circle.hexagongrid.fill": FE("M12 1.8a10.2 10.2 0 1 1 0 20.4 10.2 10.2 0 0 1 0-20.4Z M12 5.6l-2.2 1.3v2.5L12 10.7l2.2-1.3V6.9Z M8.2 12l-2.2 1.3v2.5l2.2 1.3 2.2-1.3v-2.5Z M15.8 12l-2.2 1.3v2.5l2.2 1.3 2.2-1.3v-2.5Z"),

    // agents
    "cart.fill": F('<path d="M1.6 3.4h2.6c.6 0 1 .4 1.2.9l.4 1.6h14.6c.7 0 1.2.7 1 1.3l-1.6 6.1c-.2.7-.8 1.2-1.5 1.2H8.1l.4 1.6h10.2a.9.9 0 0 1 0 1.8H7.8c-.6 0-1-.4-1.2-.9L3.6 5.2h-2a.9.9 0 0 1 0-1.8Z"/><circle cx="8.6" cy="20" r="1.7"/><circle cx="17.4" cy="20" r="1.7"/>'),
    cart: S('<path d="M1.8 3.4h2.4l2.8 11.2h11.4l2-7.8H5.3"/><circle cx="8.6" cy="19.6" r="1.4"/><circle cx="17.2" cy="19.6" r="1.4"/>'),
    "hammer.fill": F('<path d="M13.2 2.6c2.6-.6 5 .1 6.8 1.6l-1.3 1.3 2.6 2.6-2.4 2.4-2.6-2.6-1.4 1.4-1.9-1.9a3.7 3.7 0 0 0-2.4-4.2Z"/><path d="m11.7 8.9 3.4 3.4L5.4 22a2.4 2.4 0 0 1-3.4-3.4Z"/>'),
    hammer: S('<path d="M13.2 3c2.4-.5 4.6.1 6.3 1.5l-1.2 1.2 2.4 2.4-2.2 2.2-2.4-2.4-1.3 1.3-1.8-1.8a3.4 3.4 0 0 0-2.2-3.9Z"/><path d="m11.8 9.1 3.1 3.1-8.8 8.8a2.2 2.2 0 0 1-3.1-3.1Z"/>', 1.6),
    sparkles: F('<path d="M10 2.5c.4 3.9 2.2 5.8 6 6.3-3.8.5-5.6 2.4-6 6.3-.4-3.9-2.2-5.8-6-6.3 3.8-.5 5.6-2.4 6-6.3Z"/><path d="M18 12.5c.3 2.3 1.3 3.4 3.5 3.7-2.2.3-3.2 1.4-3.5 3.7-.3-2.3-1.3-3.4-3.5-3.7 2.2-.3 3.2-1.4 3.5-3.7Z"/><path d="M5.5 15.5c.2 1.6.9 2.3 2.4 2.5-1.5.2-2.2.9-2.4 2.5-.2-1.6-.9-2.3-2.4-2.5 1.5-.2 2.2-.9 2.4-2.5Z"/>'),
    "server.rack": S('<rect x="2.6" y="3.4" width="18.8" height="5.2" rx="1.4"/><rect x="2.6" y="9.4" width="18.8" height="5.2" rx="1.4"/><rect x="2.6" y="15.4" width="18.8" height="5.2" rx="1.4"/><path d="M6 6h.01M6 12h.01M6 18h.01M9 6h.01M9 12h.01M9 18h.01"/>', 1.7),
    magnifyingglass: S('<circle cx="10.3" cy="10.3" r="6.8"/><path d="m15.3 15.3 5.6 5.6"/>', 2.1),
    "wrench.and.screwdriver.fill": F('<path d="M14.9 2.3a5 5 0 0 0-4.3 6.8L2.9 16.8a2.1 2.1 0 0 0 3 3l7.7-7.7a5 5 0 0 0 6.8-4.3l-2.9 2.9-2.8-.9-.9-2.8 2.9-2.9a5 5 0 0 0-1.8-.8Z"/><path d="m15.2 14.8 1.3-1.3 5.5 5.5c.6.6.6 1.6 0 2.2s-1.6.6-2.2 0Z"/>'),
    globe: S('<circle cx="12" cy="12" r="9.6"/><path d="M2.4 12h19.2M12 2.4c2.6 2.6 3.9 5.8 3.9 9.6S14.6 19 12 21.6M12 2.4C9.4 5 8.1 8.2 8.1 12s1.3 7 3.9 9.6"/>', 1.6),

    // actions
    "qrcode.viewfinder": S('<path d="M3 7.5V5a2 2 0 0 1 2-2h2.5M16.5 3H19a2 2 0 0 1 2 2v2.5M21 16.5V19a2 2 0 0 1-2 2h-2.5M7.5 21H5a2 2 0 0 1-2-2v-2.5"/>') + F('<rect x="6.5" y="6.5" width="4.5" height="4.5" rx=".6"/><rect x="13" y="6.5" width="4.5" height="4.5" rx=".6"/><rect x="6.5" y="13" width="4.5" height="4.5" rx=".6"/><rect x="13" y="13" width="2" height="2"/><rect x="15.5" y="15.5" width="2" height="2"/>'),
    "play.rectangle": S('<rect x="2.4" y="4.4" width="19.2" height="15.2" rx="3"/><path d="m10 9 5 3-5 3Z"/>', 1.6),
    "house.and.flag": S('<path d="M2.5 12.5 10 6l7.5 6.5M4.6 10.8V20h10.8v-9.2M8.4 20v-5h3.2v5"/><path d="M18.5 2.6v6.4"/>', 1.7) + F('<path d="M18.5 2.6h3.6l-1 1.6 1 1.6h-3.6Z"/>'),
    "wifi.slash": S('<path d="M2.2 8.8a14.2 14.2 0 0 1 4.6-3M10.6 4.7A14 14 0 0 1 21.8 8.8M5.4 12.2a9.6 9.6 0 0 1 3.6-2.1M13.8 9.9a9.6 9.6 0 0 1 4.8 2.3M8.8 15.6a4.6 4.6 0 0 1 6.4 0M3 3l18 18"/>') + F('<circle cx="12" cy="19.2" r="1.5"/>'),
    "tray.and.arrow.up": S('<path d="M3 13.5h4.6l1.4 2.6h6l1.4-2.6H21V18a2.4 2.4 0 0 1-2.4 2.4H5.4A2.4 2.4 0 0 1 3 18Z"/><path d="M3 13.5 5.6 6.8M21 13.5 18.4 6.8M12 11.6V2.8M8.6 6 12 2.6 15.4 6"/>', 1.6),
    "tray.full.fill": F('<path d="M5.3 3.5h13.4l3 9.2h-5.3l-1.6 2.8H9.2l-1.6-2.8H2.3Z"/><path d="M2.1 14.2h4.7l1.5 2.7h7.4l1.5-2.7h4.7v4a2.4 2.4 0 0 1-2.4 2.4H4.5a2.4 2.4 0 0 1-2.4-2.4Z"/>'),
    checkmark: S('<path d="m4.5 12.6 4.6 4.6L19.5 6.8"/>', 2.3),
    xmark: S('<path d="m6 6 12 12M18 6 6 18"/>', 2.2),
    timer: S('<circle cx="12" cy="13.2" r="8.2"/><path d="M12 13.2V8.6M9.6 2.4h4.8M18.6 5.4l1.4-1.4"/>', 1.8),
    "checkmark.shield.fill": FE("M12 1.8 20.2 5v6.2c0 5.1-3.4 9.3-8.2 11-4.8-1.7-8.2-5.9-8.2-11V5Z M16.4 9 15 7.7l-4.4 4.8-2.2-2.1L7.1 11.8l3.6 3.4Z"),
    "exclamationmark.shield.fill": FE("M12 1.8 20.2 5v6.2c0 5.1-3.4 9.3-8.2 11-4.8-1.7-8.2-5.9-8.2-11V5Z M11 6.6h2l-.2 7h-1.6Z M12 15.2a1.2 1.2 0 1 1 0 2.4 1.2 1.2 0 0 1 0-2.4Z"),
    "lock.shield.fill": FE("M12 1.8 20.2 5v6.2c0 5.1-3.4 9.3-8.2 11-4.8-1.7-8.2-5.9-8.2-11V5Z M12 6.4a2.8 2.8 0 0 0-2.8 2.8v1.4h-.6v5.6h6.8v-5.6h-.6V9.2A2.8 2.8 0 0 0 12 6.4Z M12 7.8c.8 0 1.4.6 1.4 1.4v1.4h-2.8V9.2c0-.8.6-1.4 1.4-1.4Z"),
    "arrowshape.turn.up.left": S('<path d="M9.4 4.6 2.8 10.8l6.6 6.2v-3.8c5.2 0 8.6 1.4 11.8 5.6-.8-5.8-4-10.2-11.8-10.8Z"/>', 1.6),
    "bolt.fill": F('<path d="M13.6 1.8 4.4 13.4h6.4l-1.8 8.8 9.6-12h-6.6Z"/>'),
    doc: S('<path d="M6 2.6h8l4.8 4.8v12.2a1.8 1.8 0 0 1-1.8 1.8H6a1.8 1.8 0 0 1-1.8-1.8V4.4A1.8 1.8 0 0 1 6 2.6Z"/><path d="M13.8 2.8v4.8h4.8"/>', 1.6),
    "doc.text.magnifyingglass": S('<path d="M14 21.4H6a1.8 1.8 0 0 1-1.8-1.8V4.4A1.8 1.8 0 0 1 6 2.6h8l4.8 4.8v3.2"/><path d="M13.8 2.8v4.8h4.8M7.6 9.4h4M7.6 12.6h3"/><circle cx="16.2" cy="16.2" r="3.2"/><path d="m18.6 18.6 2.6 2.6"/>', 1.6),
    "photo.on.rectangle.angled": S('<rect x="6.4" y="6.6" width="15" height="12" rx="2"/><path d="M6.4 15.4l3.8-3.6 3.2 3 2.2-2 5.8 5.2M17.2 3.2 4 5.2a1.8 1.8 0 0 0-1.5 2.1l1.4 9.2"/><circle cx="17" cy="10.2" r="1.2"/>', 1.6),
    "arrow.up.circle.fill": FE("M12 1.4a10.6 10.6 0 1 1 0 21.2 10.6 10.6 0 0 1 0-21.2Z M12 6.2 6.9 11.3l1.4 1.4 2.7-2.7v7.8h2V10l2.7 2.7 1.4-1.4Z"),
    "chevron.right": S('<path d="m9 4.5 7.5 7.5L9 19.5"/>', 2.6),
    "chevron.left": S('<path d="M15 4.5 7.5 12l7.5 7.5"/>', 2.8),
    "chevron.down": S('<path d="m4.5 9 7.5 7.5L19.5 9"/>', 2.6),
    link: S('<path d="M10.2 13.8a4.2 4.2 0 0 0 6 0l3-3a4.2 4.2 0 0 0-6-6l-1.3 1.3"/><path d="M13.8 10.2a4.2 4.2 0 0 0-6 0l-3 3a4.2 4.2 0 0 0 6 6l1.3-1.3"/>', 1.8),
    livephoto: S('<circle cx="12" cy="12" r="2.6"/><circle cx="12" cy="12" r="6"/><circle cx="12" cy="12" r="9.6" stroke-dasharray="1.2 2.4"/>', 1.6),
    folder: S('<path d="M2.6 6.4a2 2 0 0 1 2-2h4.6l2 2.2h8.2a2 2 0 0 1 2 2v9.6a2 2 0 0 1-2 2H4.6a2 2 0 0 1-2-2Z"/>', 1.6),
    "folder.fill": F('<path d="M2.6 6.4a2 2 0 0 1 2-2h4.6l2 2.2h8.2a2 2 0 0 1 2 2v9.6a2 2 0 0 1-2 2H4.6a2 2 0 0 1-2-2Z"/>'),
    power: S('<path d="M12 2.6v8.6M7 5.4a8 8 0 1 0 10 0"/>', 2),
    "antenna.radiowaves.left.and.right": S('<circle cx="12" cy="10" r="1.6"/><path d="M12 11.6v9.8M8.4 6.4a5 5 0 0 0 0 7.2M15.6 6.4a5 5 0 0 1 0 7.2M5.4 3.6a9 9 0 0 0 0 12.8M18.6 3.6a9 9 0 0 1 0 12.8"/>', 1.7),
    "questionmark.circle": S('<circle cx="12" cy="12" r="9.6"/><path d="M9.4 9.4a2.7 2.7 0 0 1 5.2.9c0 1.8-2.6 2.2-2.6 4"/>', 1.7) + F('<circle cx="12" cy="17.2" r="1.1"/>'),
    "square.and.pencil": S('<path d="M11.6 4.4H6a2.2 2.2 0 0 0-2.2 2.2V18a2.2 2.2 0 0 0 2.2 2.2h11.4a2.2 2.2 0 0 0 2.2-2.2v-5.6"/><path d="m18.2 2.8 3 3-8.6 8.6-3.8.8.8-3.8Z"/>', 1.6),
    waveform: S('<path d="M3 10.5v3M6.6 7.5v9M10.2 4v16M13.8 8v8M17.4 5.5v13M21 10v4"/>', 2),
    "arrow.up.forward.app": S('<path d="M10.6 4.4H6a2.2 2.2 0 0 0-2.2 2.2V18a2.2 2.2 0 0 0 2.2 2.2h11.4a2.2 2.2 0 0 0 2.2-2.2v-4.6M14.4 3.6h6v6M20 4l-8.4 8.4"/>', 1.7),
    "bell.badge": S('<path d="M12 3.2a6 6 0 0 0-6 6v4.2l-1.8 3h15.6l-1.8-3V11M9.6 19.4a2.6 2.6 0 0 0 4.8 0"/>', 1.6) + F('<circle cx="18.2" cy="5.6" r="3"/>'),
    "bell.fill": F('<path d="M12 2.4a6.4 6.4 0 0 0-6.4 6.4v4.6l-2 3.4h16.8l-2-3.4V8.8A6.4 6.4 0 0 0 12 2.4Z"/><path d="M9.4 18.6h5.2a2.6 2.6 0 0 1-5.2 0Z"/>'),
    "iphone.slash": S('<path d="M8.6 2.6h6.8a2 2 0 0 1 2 2v9.6M17.4 19.4a2 2 0 0 1-2 2H8.6a2 2 0 0 1-2-2V6.6M3 3l18 18"/>', 1.6),
    "lightbulb.fill": F('<path d="M12 2a6.6 6.6 0 0 0-3.9 11.9c.6.5 1 1.2 1 2h5.8c0-.8.4-1.5 1-2A6.6 6.6 0 0 0 12 2Z"/><rect x="9.2" y="17" width="5.6" height="1.8" rx=".9"/><rect x="10" y="19.8" width="4" height="1.8" rx=".9"/>'),
    "brain.head.profile": S('<path d="M12.5 2.8c-4.6 0-8 3.2-8 7.4 0 2.2 1 3.6 1.8 4.6v6.4h7.2v-2.6h2.8a2 2 0 0 0 2-2v-2.4l1.6-.6-1.6-3.2c0-4.4-2.6-7.6-5.8-7.6Z"/>', 1.6),
    "arrow.clockwise": S('<path d="M19.8 12a7.8 7.8 0 1 1-2.3-5.5M19.8 3.8v4.6h-4.6"/>', 2),
    "arrow.triangle.2.circlepath": S('<path d="M4 11a8 8 0 0 1 13.6-5.4M20 13a8 8 0 0 1-13.6 5.4M17.8 2.4v3.4h-3.4M6.2 21.6v-3.4h3.4"/>', 1.8),
    "lock.fill": FE("M12 2.2a4.6 4.6 0 0 0-4.6 4.6v3H6.2a1.6 1.6 0 0 0-1.6 1.6v8.8a1.6 1.6 0 0 0 1.6 1.6h11.6a1.6 1.6 0 0 0 1.6-1.6v-8.8a1.6 1.6 0 0 0-1.6-1.6h-1.2v-3A4.6 4.6 0 0 0 12 2.2Z M12 4.2a2.6 2.6 0 0 1 2.6 2.6v3H9.4v-3A2.6 2.6 0 0 1 12 4.2Z"),
    flashlight: F('<path d="M8 2h8v4.4l-2 3.6v11a1 1 0 0 1-1 1h-2a1 1 0 0 1-1-1V10L8 6.4Z"/>'),
    airplane: F('<path d="M21.2 13.6v-1.8l-8-4.8V3.2a1.2 1.2 0 0 0-2.4 0V7l-8 4.8v1.8l8-2.4v5.2l-2.4 1.8v1.4l3.6-1 3.6 1v-1.4l-2.4-1.8v-5.2Z"/>'),
    wifi: F('<path d="M12 18a1.8 1.8 0 1 1 0 3.6 1.8 1.8 0 0 1 0-3.6Z"/>') + S('<path d="M2.6 8.8a13.6 13.6 0 0 1 18.8 0M5.6 12.2a9.2 9.2 0 0 1 12.8 0M8.6 15.4a4.8 4.8 0 0 1 6.8 0"/>', 2.1),
    bluetooth: S('<path d="m6.6 7.2 10.4 9.4-5 4.4V3l5 4.4L6.6 16.8"/>', 1.9),
    "antenna.radiowaves.cell": F('<rect x="3" y="14" width="3" height="6" rx="1"/><rect x="8" y="11" width="3" height="9" rx="1"/><rect x="13" y="7.5" width="3" height="12.5" rx="1"/><rect x="18" y="4" width="3" height="16" rx="1"/>'),
    "moon.fill": F('<path d="M14.6 2.6a9.6 9.6 0 1 0 6.8 13.6A8 8 0 0 1 14.6 2.6Z"/>'),
    "camera.fill": F('<path d="M8.4 4.2h7.2l1.4 2.2h3a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V8.4a2 2 0 0 1 2-2h3Z"/>') + `<circle cx="12" cy="13" r="3.6" fill="none" stroke="#fff" stroke-width="1.6"/>`,
    "network": S('<circle cx="12" cy="5" r="2.4"/><circle cx="5" cy="18" r="2.4"/><circle cx="19" cy="18" r="2.4"/><path d="M12 7.4v4.2M12 11.6l-5.6 4.4M12 11.6l5.6 4.4"/>', 1.6),
    "message.fill": F('<path d="M12 3c-5.5 0-10 3.7-10 8.2 0 2.5 1.4 4.8 3.6 6.3l-.9 3.4 3.9-2.1c1.1.3 2.2.5 3.4.5 5.5 0 10-3.7 10-8.1S17.5 3 12 3Z"/>'),
    "safari": S('<circle cx="12" cy="12" r="9.6"/>') + F('<path d="m15.8 8.2-2.4 5.2-5.2 2.4 2.4-5.2Z"/>'),
    "phone.fill": F('<path d="M6.6 2.8 9 2.4l1.8 4.4-2 1.6a12 12 0 0 0 6.8 6.8l1.6-2 4.4 1.8-.4 2.4a2.4 2.4 0 0 1-2.4 2C10.8 19.4 4.6 13.2 4.6 5.2a2.4 2.4 0 0 1 2-2.4Z"/>'),
    "battery": `<svg viewBox="0 0 27 13"><rect x="0.5" y="0.5" width="23" height="12" rx="3.6" fill="none" stroke="currentColor" opacity=".4"/><rect x="2" y="2" width="17" height="9" rx="2.2" fill="currentColor"/><path d="M25 4.5v4a2 2 0 0 0 0-4Z" fill="currentColor" opacity=".45"/></svg>`,
  };

  // Aliases used by agent notes / SwiftUI code.
  I["books.vertical"] = I["books.vertical.fill"];
  I["server"] = I["server.rack"];
  I["tray.fill"] = I["tray.full.fill"];

  function render(root) {
    (root || document).querySelectorAll("i.sf[data-i]").forEach((el) => {
      const name = el.getAttribute("data-i");
      const body = I[name];
      if (!body) {
        el.textContent = "?";
        console.warn("missing icon", name);
        return;
      }
      el.innerHTML = body.startsWith("<svg") ? body : `<svg viewBox="0 0 24 24" aria-hidden="true">${body}</svg>`;
    });
  }
  window.SF = { icons: I, render };
  document.addEventListener("DOMContentLoaded", () => render());
})();
