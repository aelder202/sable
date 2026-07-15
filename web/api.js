'use strict';

async function apiFetch(path, opts = {}) {
  const headers = { ...(opts.headers || {}) };
  if (opts.body && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
  if (token) headers.Authorization = 'Bearer ' + token;

  const resp = await fetch(path, { ...opts, headers });
  if (resp.status === 401 && token) {
    setLoggedOutState('Session expired. Sign in again.');
  }
  return resp;
}

async function apiFetchAll(path) {
  const values = [];
  let offset = 0;
  const limit = 500;
  for (;;) {
    const separator = path.includes('?') ? '&' : '?';
    const resp = await apiFetch(path + separator + 'limit=' + limit + '&offset=' + offset);
    if (!resp.ok) throw new Error('request failed (' + resp.status + ')');
    const page = await resp.json();
    if (!Array.isArray(page)) return [];
    values.push(...page);
    const total = Number.parseInt(resp.headers.get('X-Total-Count') || '', 10);
    if (page.length < limit || (Number.isFinite(total) && values.length >= total)) return values;
    offset += page.length;
  }
}

async function readResponseMessage(resp, fallback) {
  try {
    const text = (await resp.text()).trim();
    if (text) {
      return text.length > 180 ? text.slice(0, 177) + '...' : text;
    }
  } catch (_) {
    // Ignore response body parsing failures and fall back to the default text.
  }
  return fallback;
}
