'use strict';

(function exposeSableLogic(root) {
  function mergeDirectoryPage(previous, page) {
    if (!page || !Array.isArray(page.entries)) return page;
    const next = { ...page, entries: page.entries.slice() };
    const offset = Number(next.offset || 0);
    next.next_offset = offset + next.entries.length;
    if (offset > 0 && previous && previous.path === next.path && Array.isArray(previous.entries)) {
      next.entries = previous.entries.concat(next.entries);
    }
    return next;
  }

  function deliveryLabel(job) {
    return job && job.status === 'in_flight' ? 'IN FLIGHT' : 'QUEUED';
  }

  function agentDisplayName(agent) {
    if (!agent) return 'Unknown agent';
    return String(agent.display_name || agent.hostname || (agent.id ? 'Agent ' + agent.id.slice(0, 8) : 'Unknown agent'));
  }

  function browserBreadcrumbParts(path, separator) {
    const value = String(path || '.');
    const windows = separator === '\\' || /^[a-zA-Z]:[\\/]/.test(value) || value.startsWith('\\\\');
    const sep = windows ? '\\' : '/';
    if (!windows) {
      const pieces = value.split('/').filter(Boolean);
      const parts = value.startsWith('/') ? [{ label: '/', path: '/' }] : [];
      let current = '';
      pieces.forEach(piece => {
        current = current ? current + '/' + piece : (value.startsWith('/') ? '/' + piece : piece);
        parts.push({ label: piece, path: current });
      });
      return parts.length ? parts : [{ label: value, path: value }];
    }
    if (value.startsWith('\\\\')) {
      const pieces = value.split(/[\\/]+/).filter(Boolean);
      if (pieces.length >= 2) {
        let current = '\\\\' + pieces[0] + '\\' + pieces[1] + '\\';
        const parts = [{ label: '\\\\' + pieces[0] + '\\' + pieces[1], path: current }];
        pieces.slice(2).forEach(piece => {
          current += piece;
          parts.push({ label: piece, path: current });
          current += '\\';
        });
        return parts;
      }
    }
    const driveMatch = value.match(/^([a-zA-Z]:)[\\/]?/);
    const drive = driveMatch ? driveMatch[1] : '';
    const rest = drive ? value.slice(driveMatch[0].length) : value;
    const parts = drive ? [{ label: drive + '\\', path: drive + '\\' }] : [];
    let current = drive ? drive + '\\' : '';
    rest.split(/[\\/]+/).filter(Boolean).forEach(piece => {
      current += piece;
      parts.push({ label: piece, path: current });
      current += sep;
    });
    return parts.length ? parts : [{ label: value, path: value }];
  }

  function filterSortDirectoryEntries(entries, query, sortKey, sortDirection) {
    const value = String(query || '').trim().toLowerCase();
    const direction = sortDirection === 'desc' ? -1 : 1;
    const seen = new Set();
    return (Array.isArray(entries) ? entries : [])
      .filter(entry => {
        const key = entry && entry.path ? entry.path : JSON.stringify(entry);
        if (seen.has(key)) return false;
        seen.add(key);
        return !value || String(entry.name || '').toLowerCase().includes(value);
      })
      .sort((left, right) => {
        if (left.is_dir !== right.is_dir) return left.is_dir ? -1 : 1;
        let delta = 0;
        if (sortKey === 'size') delta = Number(left.size || 0) - Number(right.size || 0);
        else if (sortKey === 'modified') delta = String(left.mod_time || '').localeCompare(String(right.mod_time || ''));
        else delta = String(left.name || '').localeCompare(String(right.name || ''), undefined, { sensitivity: 'base' });
        if (!delta) delta = String(left.name || '').localeCompare(String(right.name || ''), undefined, { sensitivity: 'base' });
        return delta * direction;
      });
  }

  const api = { mergeDirectoryPage, deliveryLabel, agentDisplayName, browserBreadcrumbParts, filterSortDirectoryEntries };
  root.SableLogic = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof globalThis !== 'undefined' ? globalThis : window);
