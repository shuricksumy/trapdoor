import { test } from 'node:test';
import assert from 'node:assert/strict';
import { paginate, pageSizes, pageList, savedSize } from './pager.js';

test('page sizes start at 15', () => {
  assert.deepEqual(pageSizes, [15, 25, 50, 100]);
  assert.equal(savedSize(), 15); // no localStorage in node: the default
});

test('paginate', () => {
  const list = Array.from({ length: 42 }, (_, i) => i + 1);
  assert.deepEqual(paginate(list, 1, 15), { rows: list.slice(0, 15), page: 1, pages: 3, total: 42, from: 1, to: 15 });
  const last = paginate(list, 3, 15);
  assert.deepEqual([last.rows.length, last.from, last.to], [12, 31, 42]);
  assert.equal(paginate(list, 9, 15).page, 3); // past the end: the last page
  assert.equal(paginate(list, 0, 15).page, 1);
  assert.equal(paginate(list, 2, 50).page, 1); // a bigger size: fewer pages
  assert.deepEqual(paginate([], 1, 15), { rows: [], page: 1, pages: 1, total: 0, from: 0, to: 0 });
});

test('pageList', () => {
  assert.deepEqual(pageList(1, 3), [1, 2, 3]);
  assert.deepEqual(pageList(5, 20), [1, '…', 3, 4, 5, 6, 7, '…', 20]);
});
