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

  const api = { mergeDirectoryPage, deliveryLabel };
  root.SableLogic = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof globalThis !== 'undefined' ? globalThis : window);
