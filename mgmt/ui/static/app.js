// twd admin UI: progressive enhancement over server-rendered pages.
//  - every 5 s, re-fetch the current page and swap the [data-live] regions
//    whose data changed (the server's templates stay the single renderer).
//    Relative times (<time data-ago>) are computed here, in the browser, so
//    the passage of time alone never counts as a change: a quiet page is
//    never re-rendered and nothing jumps under the reader. A region with
//    text selected in it is left alone until the selection is cleared;
//  - forms with data-confirm ask before they submit. The submit itself is
//    a plain form POST to the UI's /ui/... action, which makes the /v1 call
//    server-side: the browser never talks to the API directly.
// Nothing is stored client-side: no cookies, no localStorage, no tokens
// (the connection's tailnet identity is the credential).
(function () {
  'use strict';
  var REFRESH_MS = 5000;
  var $ = function (sel, el) { return (el || document).querySelector(sel); };
  var $$ = function (sel, el) { return Array.prototype.slice.call((el || document).querySelectorAll(sel)); };

  // ---- relative times ----
  // Same buckets as the server's ago() in mgmt/ui/views.go.
  function ago(ms, now) {
    var s = Math.max(0, Math.floor((now - ms) / 1000));
    if (s < 60) return s + 's ago';
    if (s < 3600) return Math.floor(s / 60) + 'm ago';
    if (s < 48 * 3600) return Math.floor(s / 3600) + 'h ago';
    return Math.floor(s / 86400) + 'd ago';
  }
  function renderTimes(root, now) {
    $$('time[data-ago]', root).forEach(function (el) {
      var ms = Date.parse(el.getAttribute('datetime'));
      if (isNaN(ms)) return;
      var txt = ago(ms, now);
      if (el.textContent !== txt) el.textContent = txt;
    });
  }

  // ---- live refresh ----
  var state = $('#live-state');
  var busy = false;
  var shown = '';
  function setState(kind, text) {
    if (!state || shown === kind + text) return;
    shown = kind + text;
    state.className = 'live ' + kind;
    state.textContent = text;
  }

  function selectionIn(el) {
    var sel = window.getSelection && window.getSelection();
    return !!(sel && !sel.isCollapsed && sel.anchorNode && el.contains(sel.anchorNode));
  }

  function refreshPage() {
    return fetch(location.href, { headers: { 'Accept': 'text/html' }, cache: 'no-store', credentials: 'same-origin' })
      .then(function (res) {
        if (!res.ok) throw new Error('HTTP ' + res.status);
        return res.text();
      })
      .then(function (html) {
        var doc = new DOMParser().parseFromString(html, 'text/html');
        var now = Date.now();
        renderTimes(doc, now);
        renderTimes(document, now);
        $$('[data-live]').forEach(function (el) {
          var fresh = doc.querySelector('[data-live="' + el.getAttribute('data-live') + '"]');
          if (fresh && fresh.innerHTML !== el.innerHTML && !selectionIn(el)) el.innerHTML = fresh.innerHTML;
        });
      });
  }

  function tick() {
    if (document.hidden || busy) return;
    if (document.activeElement && /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName)) return;
    busy = true;
    refreshPage()
      .then(function () { setState('ok', 'Live'); })
      .catch(function (e) { setState('bad', 'Can\'t reach mgmt (' + e.message + '), retrying'); })
      .then(function () { busy = false; });
  }
  renderTimes(document, Date.now());
  setState('ok', 'Live');
  setInterval(tick, REFRESH_MS);
  document.addEventListener('visibilitychange', function () { if (!document.hidden) tick(); });

  // ---- mutations: confirm, then let the form POST to /ui/... ----
  document.addEventListener('submit', function (ev) {
    var form = ev.target;
    // The clicked button may carry its own message (Approve / Reject).
    var sub = ev.submitter;
    var msg = (sub && sub.getAttribute('data-confirm')) || (form && form.getAttribute && form.getAttribute('data-confirm'));
    if (!msg) return;
    if (!window.confirm(msg)) { ev.preventDefault(); return; }
    // No double submit. Disable after this tick: a button disabled now
    // would drop its name=value (the decision) from the submitted form.
    setTimeout(function () { $$('button[type=submit]', form).forEach(function (b) { b.disabled = true; }); }, 0);
  });
})();
