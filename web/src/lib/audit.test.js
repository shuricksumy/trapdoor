import { test } from 'node:test';
import assert from 'node:assert/strict';
import { actionText, actionTone, actionGroups, periodFrom } from './audit.js';

test('actions in words', () => {
  assert.equal(actionText('cert.issue'), 'added a client');
  assert.equal(actionText('login.failed'), 'sign-in failed');
  assert.equal(actionText('tool.traceroute'), 'ran traceroute');
  assert.equal(actionText('something.new'), 'something.new');
  assert.equal(actionTone('login.failed'), 'bad');
  assert.equal(actionTone('cert.revoke'), 'warn');
  assert.equal(actionTone('admin.disable'), 'warn');
  assert.equal(actionTone('cert.issue'), '');
  assert.equal(actionGroups[0][0], '');
});

test('period presets', () => {
  const now = new Date(2026, 10, 22, 15, 0);
  assert.equal(periodFrom('today', now), '2026-11-22');
  assert.equal(periodFrom('7d', now), '2026-11-16');
  assert.equal(periodFrom('30d', now), '2026-10-24');
  assert.equal(periodFrom('all', now), '');
});
