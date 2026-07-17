'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {
  mergeDirectoryPage,
  deliveryLabel,
  agentDisplayName,
  browserBreadcrumbParts,
  filterSortDirectoryEntries,
} = require('./logic.js');

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

test('agent display names prefer operator metadata then hostname then id', () => {
  assert.equal(agentDisplayName({ display_name: 'Web Server', hostname: 'host', id: '1234567890' }), 'Web Server');
  assert.equal(agentDisplayName({ hostname: 'host', id: '1234567890' }), 'host');
  assert.equal(agentDisplayName({ id: '1234567890' }), 'Agent 12345678');
});

test('breadcrumbs support unix and windows paths', () => {
  assert.deepEqual(browserBreadcrumbParts('/var/log', '/'), [
    { label: '/', path: '/' },
    { label: 'var', path: '/var' },
    { label: 'log', path: '/var/log' },
  ]);
  assert.deepEqual(browserBreadcrumbParts('C:\\Windows\\System32', '\\'), [
    { label: 'C:\\', path: 'C:\\' },
    { label: 'Windows', path: 'C:\\Windows' },
    { label: 'System32', path: 'C:\\Windows\\System32' },
  ]);
});

test('directory filtering deduplicates and sorts folders before files', () => {
  const entries = [
    { name: 'z.txt', path: '/z', size: 4, is_dir: false },
    { name: 'Folder', path: '/folder', is_dir: true },
    { name: 'a.txt', path: '/a', size: 2, is_dir: false },
    { name: 'a.txt duplicate', path: '/a', size: 2, is_dir: false },
  ];
  const sorted = filterSortDirectoryEntries(entries, '', 'name', 'asc');
  assert.deepEqual(sorted.map(item => item.path), ['/folder', '/a', '/z']);
  assert.deepEqual(filterSortDirectoryEntries(entries, 'z.', 'name', 'asc').map(item => item.path), ['/z']);
});

test('global action confirmation is not hidden with the Agents workspace', () => {
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const agentsWorkspaceEnd = html.lastIndexOf('</main>');
  const actionModal = html.indexOf('id="action-confirm-modal"');
  const toast = html.indexOf('id="app-toast"');
  assert.ok(agentsWorkspaceEnd >= 0);
  assert.ok(actionModal > agentsWorkspaceEnd, 'action confirmation must be outside the hidden Agents workspace');
  assert.ok(actionModal < toast, 'action confirmation should remain in the global app layer');
});

test('fleet-wide modals remain available from Overview', () => {
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const agentsWorkspaceEnd = html.lastIndexOf('</main>');
  const activeJobsModal = html.indexOf('id="active-jobs-modal"');
  const editInfoModal = html.indexOf('id="edit-info-modal"');
  assert.ok(activeJobsModal > agentsWorkspaceEnd, 'active jobs modal must not be hidden with the Agents workspace');
  assert.ok(editInfoModal > agentsWorkspaceEnd, 'edit info modal must not be hidden with the Agents workspace');
  assert.match(html, /id="active-jobs-list"/);
  assert.match(fs.readFileSync(path.join(__dirname, 'dashboard.js'), 'utf8'), /No jobs are active\./);
});

test('Task Builder uses explicit current and selected target controls', () => {
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  assert.match(html, /id="target-current-btn"[^>]*>Current agent<\/button>/);
  assert.match(html, /id="target-selected-btn"[^>]*>Selected agents \(0\)<\/button>/);
  assert.doesNotMatch(html, /id="send-options-btn"/);
  assert.doesNotMatch(html, /id="send-options-menu"/);
  assert.match(html, /id="exit-interactive-btn" type="button" hidden>Exit<\/button>/);
});

test('Needs attention has its own constrained scroll container', () => {
  const css = fs.readFileSync(path.join(__dirname, 'style.css'), 'utf8');
  assert.match(css, /\.attention-list\s*\{[^}]*flex:\s*1 1 auto;[^}]*overflow-y:\s*auto;/s);
  assert.match(css, /\.attention-panel\s*\{[^}]*display:\s*flex;[^}]*flex-direction:\s*column;/s);
});
