// Row menus (<details class="menu">) open as a layer over the page, so the
// scrolling table around them does not cut them off.

/** ontoggle handler: place the open menu next to its button, upwards near the bottom. */
export function placeMenu(e) {
  const details = e.currentTarget;
  if (!details.open) return;
  for (const other of document.querySelectorAll('details.menu[open]')) {
    if (other !== details) other.open = false;
  }
  const list = details.querySelector('.menu-list');
  const r = details.querySelector('summary').getBoundingClientRect();
  const h = list.offsetHeight || 220;
  const up = r.bottom + 4 + h > window.innerHeight && r.top - 4 - h > 0;
  list.style.top = `${up ? r.top - 4 - h : r.bottom + 4}px`;
  list.style.right = `${Math.max(8, window.innerWidth - r.right)}px`;
}

function closeAll(except) {
  for (const d of document.querySelectorAll('details.menu[open]')) {
    if (d !== except) d.open = false;
  }
}

// A click outside, a scroll or a resize closes open menus (they stay where
// they were placed).
if (typeof window !== 'undefined') {
  document.addEventListener('click', (e) => closeAll(e.target.closest?.('details.menu')));
  window.addEventListener('resize', () => closeAll());
  document.addEventListener('scroll', (e) => !e.target.closest?.('.menu-list') && closeAll(), true);
  document.addEventListener('keydown', (e) => e.key === 'Escape' && closeAll());
}
