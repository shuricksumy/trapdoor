// Pages of a table: 15, 25, 50 or 100 rows (15 by default), the size
// remembered per browser for every table.

export const pageSizes = [15, 25, 50, 100];
const sizeKey = 'td-page-size';

/** The rows per page this browser chose (15 when none or not allowed). */
export function savedSize() {
  try {
    const n = Number(localStorage.getItem(sizeKey));
    return pageSizes.includes(n) ? n : pageSizes[0];
  } catch {
    return pageSizes[0];
  }
}

export function saveSize(n) {
  try {
    localStorage.setItem(sizeKey, String(n));
  } catch {
    /* private mode: only this page keeps it */
  }
}

/**
 * One page of a list: the rows, the page (kept within 1..pages) and what the
 * pager shows ("16–30 of 42").
 */
export function paginate(list, page, size) {
  const total = list.length;
  const pages = Math.max(1, Math.ceil(total / size));
  const p = Math.min(Math.max(1, Math.floor(page) || 1), pages);
  return { rows: list.slice((p - 1) * size, p * size), page: p, pages, total, from: total ? (p - 1) * size + 1 : 0, to: Math.min(p * size, total) };
}

/** Page buttons: all of them up to 7, else the first, the last and the ones around the current, with '…'. */
export function pageList(current, pages) {
  if (pages <= 7) return Array.from({ length: pages }, (_, i) => i + 1);
  const out = [1];
  // a fixed width (nine items for long lists), so the buttons do not jump
  const lo = Math.max(2, Math.min(current - 2, pages - 6));
  const hi = Math.min(pages - 1, Math.max(current + 2, 7));
  if (lo > 2) out.push('…');
  for (let p = lo; p <= hi; p++) out.push(p);
  if (hi < pages - 1) out.push('…');
  out.push(pages);
  return out;
}
