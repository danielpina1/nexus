// Nexus page snapshot. Evaluated as an expression with two arguments
// substituted by Go: (maxElements, maxText). Returns a JSON string:
// {url, title, scroll_y, scroll_height, viewport_height, total, truncated,
//  elements: [{ref, role, name, text, value, href, type, disabled, checked,
//              expanded, offscreen}], text}
// Interactive, visible elements get a stable data-nexus-ref attribute that
// browser.click/type/select use to find them again.
(function (maxElements, maxText) {
  const seqKey = '__nexusRefSeq';
  if (typeof window[seqKey] !== 'number') window[seqKey] = 0;
  const clean = (s) => String(s == null ? '' : s).replace(/\s+/g, ' ').trim();
  const cap = (s, n) => (s.length > n ? s.slice(0, n - 1) + '…' : s);
  const vw = window.innerWidth || document.documentElement.clientWidth;
  const vh = window.innerHeight || document.documentElement.clientHeight;

  const visible = (el) => {
    if (!el.isConnected) return false;
    if (el.closest('[hidden],[aria-hidden="true"],[inert]')) return false;
    const st = getComputedStyle(el);
    if (st.display === 'none' || st.visibility === 'hidden' || st.visibility === 'collapse') return false;
    if (parseFloat(st.opacity) === 0) return false;
    const r = el.getBoundingClientRect();
    return r.width >= 1 && r.height >= 1;
  };

  const inputRoles = {
    button: 'button', submit: 'button', reset: 'button', image: 'button', file: 'button',
    checkbox: 'checkbox', radio: 'radio', range: 'slider', number: 'spinbutton',
    search: 'searchbox', email: 'textbox', tel: 'textbox', url: 'textbox', text: 'textbox',
    password: 'textbox',
  };
  const roleOf = (el) => {
    const r = el.getAttribute('role');
    if (r) return r;
    const t = el.tagName.toLowerCase();
    if (t === 'a') return 'link';
    if (t === 'button' || t === 'summary') return 'button';
    if (t === 'select') return el.multiple ? 'listbox' : 'combobox';
    if (t === 'textarea') return 'textbox';
    if (t === 'input') return inputRoles[(el.type || 'text').toLowerCase()] || 'textbox';
    if (el.isContentEditable) return 'textbox';
    return 'clickable';
  };
  const isField = (el) => ['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName);
  const nameOf = (el) => {
    let n = el.getAttribute('aria-label') || '';
    if (!n) {
      const lb = el.getAttribute('aria-labelledby');
      if (lb) n = lb.split(/\s+/).map((id) => { const e = document.getElementById(id); return e ? e.textContent : ''; }).join(' ');
    }
    if (!n && el.labels && el.labels.length) n = Array.from(el.labels).map((l) => l.innerText || l.textContent).join(' ');
    if (!n && el.tagName === 'INPUT' && ['button', 'submit', 'reset'].includes(el.type)) n = el.value;
    if (!n) n = el.getAttribute('placeholder') || '';
    if (!n) n = el.getAttribute('title') || '';
    if (!n && el.tagName === 'IMG') n = el.alt || '';
    if (!n && !isField(el)) n = el.innerText || el.textContent || '';
    if (!n) { const img = el.querySelector && el.querySelector('img[alt]'); if (img) n = img.alt; }
    if (!n) n = el.getAttribute('name') || '';
    return cap(clean(n), 100);
  };

  const sel = [
    'a[href]', 'button', 'input:not([type=hidden])', 'select', 'textarea', 'summary',
    '[role=button]', '[role=link]', '[role=checkbox]', '[role=radio]', '[role=switch]', '[role=tab]',
    '[role=menuitem]', '[role=option]', '[role=combobox]', '[role=searchbox]', '[role=textbox]',
    '[role=spinbutton]', '[role=slider]', '[contenteditable=""]', '[contenteditable=true]', '[onclick]',
  ].join(',');

  const native = ['A', 'BUTTON', 'INPUT', 'SELECT', 'TEXTAREA', 'SUMMARY'];
  const all = [];
  const seen = new Set();
  for (const el of document.querySelectorAll(sel)) {
    if (seen.has(el)) continue;
    seen.add(el);
    if (!visible(el)) continue;
    // Skip non-native clickables nested in another interactive element
    // (e.g. <span onclick> inside <button>); native controls are kept.
    if (!native.includes(el.tagName)) {
      const parent = el.parentElement && el.parentElement.closest(sel);
      if (parent && visible(parent)) continue;
    }
    const r = el.getBoundingClientRect();
    const offscreen = r.bottom < 0 || r.top > vh || r.right < 0 || r.left > vw;
    all.push({ el, offscreen });
  }

  let chosen = all;
  if (all.length > maxElements) {
    const onscreen = all.filter((x) => !x.offscreen);
    const rest = all.filter((x) => x.offscreen);
    const keep = new Set(onscreen.concat(rest).slice(0, maxElements));
    chosen = all.filter((x) => keep.has(x));
  }

  const elements = chosen.map(({ el, offscreen }) => {
    let ref = el.getAttribute('data-nexus-ref');
    if (!ref) {
      window[seqKey] += 1;
      ref = String(window[seqKey]);
      el.setAttribute('data-nexus-ref', ref);
    }
    const e = { ref: Number(ref), role: roleOf(el), name: nameOf(el) };
    if (!isField(el)) {
      const t = cap(clean(el.innerText || el.textContent), 120);
      if (t && t !== e.name) e.text = t;
    }
    if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
      const ty = (el.type || '').toLowerCase();
      if (el.tagName === 'INPUT') e.type = ty;
      if (ty === 'checkbox' || ty === 'radio') e.checked = !!el.checked;
      else if (ty === 'password') e.value = el.value ? '[hidden]' : '';
      else if (el.value) e.value = cap(clean(el.value), 80);
    } else if (el.tagName === 'SELECT') {
      const o = el.selectedOptions && el.selectedOptions[0];
      if (o) e.value = cap(clean(o.textContent), 80);
      e.options = Array.from(el.options).slice(0, 15).map((o) => cap(clean(o.textContent), 40));
    } else if (el.isContentEditable) {
      e.value = cap(clean(el.innerText), 80);
    }
    if (el.tagName === 'A') {
      const h = el.getAttribute('href') || '';
      try {
        const u = new URL(h, location.href);
        e.href = cap(u.origin === location.origin ? u.pathname + u.search : u.href, 100);
      } catch (_) {
        e.href = cap(h, 100);
      }
    }
    if (el.disabled || el.getAttribute('aria-disabled') === 'true') e.disabled = true;
    const ac = el.getAttribute('aria-checked');
    if (ac === 'true' || ac === 'false') e.checked = ac === 'true';
    const ax = el.getAttribute('aria-expanded');
    if (ax === 'true' || ax === 'false') e.expanded = ax === 'true';
    if (offscreen) e.offscreen = true;
    return e;
  });

  const body = document.body ? document.body.innerText || '' : '';
  let text = body.replace(/[ \t\f\v]+/g, ' ').replace(/\s*\n\s*/g, '\n').trim();
  const textTruncated = text.length > maxText;
  if (textTruncated) text = text.slice(0, maxText);

  return JSON.stringify({
    url: location.href,
    title: document.title || '',
    scroll_y: Math.round(window.scrollY || 0),
    scroll_height: Math.round(document.documentElement.scrollHeight || 0),
    viewport_height: Math.round(vh || 0),
    total: all.length,
    truncated: all.length > chosen.length || textTruncated,
    elements,
    text,
  });
})
