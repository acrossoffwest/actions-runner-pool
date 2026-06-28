// Portal theme toggle — shared by all pages. Pre-paint theme is set by a tiny
// inline script in each page <head> (avoids FOUC); this wires the toggle button
// and keeps the icon in sync.
(function () {
  var root = document.documentElement;
  function icon(t) {
    return t === 'dark'
      ? '<path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z"/>'
      : '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>';
  }
  function set(t) {
    root.dataset.theme = t;
    try { localStorage.setItem('gharp_theme', t); } catch (e) {}
    document.querySelectorAll('.themebtn svg').forEach(function (s) { s.innerHTML = icon(t); });
  }
  document.addEventListener('DOMContentLoaded', function () {
    var cur = root.dataset.theme || 'dark';
    document.querySelectorAll('.themebtn svg').forEach(function (s) { s.innerHTML = icon(cur); });
    document.querySelectorAll('.themebtn').forEach(function (b) {
      b.addEventListener('click', function () {
        set(root.dataset.theme === 'dark' ? 'light' : 'dark');
      });
    });
  });
})();
