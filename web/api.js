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
