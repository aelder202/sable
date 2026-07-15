'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { mergeDirectoryPage, deliveryLabel } = require('./logic.js');

test('directory pages append without mutating prior state', () => {
  const previous = { path: '/tmp', entries: [{ name: 'a' }] };
  const page = { path: '/tmp', offset: 1, more: false, entries: [{ name: 'b' }] };
  const merged = mergeDirectoryPage(previous, page);
  assert.deepEqual(merged.entries.map(item => item.name), ['a', 'b']);
  assert.equal(merged.next_offset, 2);
  assert.equal(previous.entries.length, 1);
  assert.equal(page.entries.length, 1);
});

test('a different directory replaces the previous page', () => {
  const merged = mergeDirectoryPage(
    { path: '/tmp', entries: [{ name: 'a' }] },
    { path: '/var', offset: 0, more: true, entries: [{ name: 'b' }] },
  );
  assert.deepEqual(merged.entries.map(item => item.name), ['b']);
  assert.equal(merged.next_offset, 1);
});

test('delivery labels distinguish first delivery from acknowledgment wait', () => {
  assert.equal(deliveryLabel({ status: 'queued' }), 'QUEUED');
  assert.equal(deliveryLabel({ status: 'in_flight' }), 'IN FLIGHT');
});
