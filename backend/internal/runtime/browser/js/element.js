// Nexus element helpers. Evaluated as an expression with arguments
// substituted by Go: (op, ref, arg). Returns a JSON string.
//   op "describe": labels and field metadata used for classification
//   op "prepare":  scroll into view, drop target=_blank; returns visibility
//   op "click":    element.click() fallback
//   op "select":   choose an option by value or visible text (arg)
//   op "extract":  innerText of the element (ref > 0) or the body
//   op "focus":    focus the element
(function (op, ref, arg) {
  const clean = (s) => String(s == null ? '' : s).replace(/\s+/g, ' ').trim();
  const el = ref > 0 ? document.querySelector('[data-nexus-ref="' + ref + '"]') : null;
  if (ref > 0 && !el) {
    return JSON.stringify({ found: false });
  }
  const visible = (e) => {
    if (!e || !e.isConnected) return false;
    const st = getComputedStyle(e);
    if (st.display === 'none' || st.visibility === 'hidden') return false;
    const r = e.getBoundingClientRect();
    return r.width >= 1 && r.height >= 1;
  };
  const labelsOf = (e) => {
    const out = [];
    const push = (s) => { s = clean(s); if (s && !out.includes(s)) out.push(s.slice(0, 200)); };
    push(e.getAttribute('aria-label'));
    const lb = e.getAttribute('aria-labelledby');
    if (lb) push(lb.split(/\s+/).map((id) => { const x = document.getElementById(id); return x ? x.textContent : ''; }).join(' '));
    if (e.labels) Array.from(e.labels).forEach((l) => push(l.innerText || l.textContent));
    if (e.tagName === 'INPUT' && ['button', 'submit', 'reset', 'image'].includes(e.type)) push(e.value);
    push(e.getAttribute('title'));
    if (e.tagName !== 'INPUT' && e.tagName !== 'SELECT' && e.tagName !== 'TEXTAREA') push(e.innerText || e.textContent);
    e.querySelectorAll && e.querySelectorAll('img[alt]').forEach((i) => push(i.alt));
    if (e.tagName === 'IMG') push(e.alt);
    return out;
  };
  switch (op) {
    case 'describe': {
      const d = {
        found: true,
        visible: visible(el),
        tag: el.tagName.toLowerCase(),
        type: (el.getAttribute('type') || '').toLowerCase(),
        role: el.getAttribute('role') || '',
        labels: labelsOf(el),
        autocomplete: el.getAttribute('autocomplete') || '',
        name_attr: el.getAttribute('name') || '',
        id: el.id || '',
        disabled: !!el.disabled || el.getAttribute('aria-disabled') === 'true',
        submit_labels: [],
      };
      // What pressing Enter in a field would submit.
      const form = el.form || (el.closest && el.closest('form'));
      if (form && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) {
        form.querySelectorAll('button:not([type=button]):not([type=reset]), input[type=submit], input[type=image]')
          .forEach((b) => labelsOf(b).forEach((l) => d.submit_labels.push(l)));
      }
      return JSON.stringify(d);
    }
    case 'prepare': {
      if (el.tagName === 'A' && el.getAttribute('target')) el.removeAttribute('target');
      const f = el.closest && el.closest('form[target]');
      if (f) f.removeAttribute('target');
      el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
      return JSON.stringify({ found: true, visible: visible(el) });
    }
    case 'click': {
      el.click();
      return JSON.stringify({ found: true });
    }
    case 'focus': {
      // Focus and select the current contents so typing replaces them
      // (arg === 'keep' appends instead).
      el.focus();
      if (arg !== 'keep') {
        if (typeof el.select === 'function') el.select();
        else if (el.isContentEditable) {
          const r = document.createRange();
          r.selectNodeContents(el);
          const s = window.getSelection();
          s.removeAllRanges();
          s.addRange(r);
        }
      }
      return JSON.stringify({ found: true, focused: document.activeElement === el });
    }
    case 'select': {
      if (el.tagName !== 'SELECT') return JSON.stringify({ found: true, error: 'element is not a <select>' });
      const want = clean(arg).toLowerCase();
      let opt = Array.from(el.options).find((o) => o.value === arg);
      if (!opt) opt = Array.from(el.options).find((o) => clean(o.textContent).toLowerCase() === want);
      if (!opt) opt = Array.from(el.options).find((o) => clean(o.textContent).toLowerCase().includes(want));
      if (!opt) {
        return JSON.stringify({ found: true, error: 'no option matches', options: Array.from(el.options).map((o) => clean(o.textContent)).slice(0, 30) });
      }
      el.value = opt.value;
      opt.selected = true;
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
      return JSON.stringify({ found: true, selected: clean(opt.textContent), value: opt.value });
    }
    case 'extract': {
      const src = el || document.body;
      let t = (src ? src.innerText || src.textContent || '' : '');
      t = t.replace(/[ \t\f\v]+/g, ' ').replace(/\s*\n\s*/g, '\n').trim();
      return JSON.stringify({ found: true, text: t });
    }
  }
  return JSON.stringify({ found: true, error: 'unknown op ' + op });
})
