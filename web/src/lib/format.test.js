// node --test: display helpers.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { relative } from './format.js';

test('relative: within a day in hours, within an hour in minutes, then days', () => {
  const now = Date.parse('2026-11-20T22:30:00Z');
  const ago = (ms) => relative(new Date(now - ms).toISOString(), now);
  // the exact words depend on the locale; the numbers and units do not
  assert.match(ago(13.5 * 3600e3), /14|13/); // not "yesterday"
  assert.doesNotMatch(ago(13.5 * 3600e3), /day/i);
  assert.match(ago(20 * 60e3), /20/);
  assert.match(ago(3 * 86400e3), /3/);
  assert.match(relative(new Date(now + 12 * 86400e3).toISOString(), now), /12/);
});
