'use strict';

let token = null;
let activeAgentID = null;
let activeAgent = null;
let seenTaskIDs = new Set();
let currentOutputs = [];
let pendingUploadFile = null;
let taskHistory = [];
let taskHistoryIndex = -1;
let taskDrafts = new Map();
let interactiveHistory = [];
let interactiveHistoryIndex = -1;
let interactiveMode = false;
let interactiveReady = false;
let sseReader = null;
let sseStreamID = 0;
let outputsRequestID = 0;
let agentsPollTimer = null;
let authExpiryTimer = null;
let allAgents = [];
let selectedTaskType = 'shell';
let taskRequestInFlight = false;
let killConfirmTimer = null;
let armedKillAgentID = null;
let pendingClearAgentID = '';
let hasHydratedOutputs = false;
let followOutput = true;
let pendingPathCompletion = null;
let queuedCompletionPath = '';
let pathCompletionTimer = null;
let pathBrowseStates = new Map();
let deferredPathCompletionOutputs = new Map();
let deferredPathBrowseOutputs = new Map();
let artifactLibrary = [];
let auditLog = [];
let activeSessionPanel = 'all';
let fileBrowserPath = '';
let fileBrowserResult = null;
let fileBrowserMode = 'browse';
let pendingKillAgentID = '';
let outputSearchExpanded = false;
let downloadTasks = new Map();

const MAX_LOGIN_BODY_BYTES = 4096;
const MAX_UPLOAD_BYTES = 50 * 1024 * 1024;
const MAX_SLEEP_SECONDS = 24 * 60 * 60;
const MAX_REMOTE_PATH = 4096;
const POLL_INTERVAL_MS = 5000;
const KILL_CONFIRM_WINDOW_MS = 10000;
const OUTPUT_FOLLOW_THRESHOLD_PX = 40;
const PATH_COMPLETION_DEBOUNCE_MS = 150;
const PATH_BROWSE_RENEW_MS = 60 * 1000;
const PATH_BROWSE_FAST_WINDOW_MS = 2 * 60 * 1000;
const PENDING_TASK_ID = '__pending__';

const TASK_TYPES = {
  shell: {
    buttonLabel: 'Queue Shell',
    help: 'Queue a single shell command for the selected session.',
    note: 'Shell payloads stay in memory only and are still server-side validated before queueing.',
    placeholder: 'Enter a shell command',
    requiresPayload: true,
    inputMode: 'text',
  },
  ps: {
    buttonLabel: 'Queue PS',
    help: 'List running processes for the selected session.',
    note: 'Process listing is a read-only, one-shot situational awareness task.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  screenshot: {
    buttonLabel: 'Take Screenshot',
    help: 'Capture one bounded screenshot from the selected session.',
    note: 'Screenshots are operator-initiated, downsampled, and delivered as bounded chunks.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  snapshot: {
    buttonLabel: 'Queue Host Info',
    help: 'Collect a host information report from the selected session.',
    note: 'Host Info returns identity, network, route, disk, and environment basics as a text artifact.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  persistence: {
    buttonLabel: 'Check Persistence',
    help: 'List common persistence locations for defensive review.',
    note: 'Persistence detection reads common autorun locations and does not modify them.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  peas: {
    buttonLabel: 'Run PEAS',
    help: 'Run LinPEAS or winPEAS based on the selected session OS.',
    note: 'PEAS output is captured as a text artifact and returned through chunked results.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  download: {
    buttonLabel: 'Queue Download',
    help: 'Request a remote file path and receive the result as a browser download.',
    note: 'Path suggestions are prepared automatically. Use Browse to open the remote file browser and download files directly.',
    placeholder: 'Enter a remote file path',
    requiresPayload: true,
    inputMode: 'text',
  },
  upload: {
    buttonLabel: 'Queue Upload',
    help: 'Send a local file to a remote destination path on the selected session.',
    note: 'Path suggestions are prepared automatically. Pick a file with Choose File or Browse remote directories to set the destination.',
    placeholder: 'Enter a remote destination path',
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
const taskTypeSelect = $('task-type-select');
const taskTypeMenu = $('task-type-menu');
const taskTypeButton = $('task-type-button');
const taskTypeButtonLabel = $('task-type-button-label');
const taskTypeList = $('task-type-list');
const sessionPanelTabs = Array.from(document.querySelectorAll('.session-tab'));
const sessionPanelFilter = $('session-panel-filter');
const outputShellEl = $('output-shell');
const outputResizerEl = $('output-resizer');
const outputEmptyTitle = $('output-empty').querySelector('h3');
const outputEmptyText = $('output-empty').querySelector('p');


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
  resetAllPathBrowserStates(true);
  stopSSEStream();
  exitInteractiveMode(false);

  token = null;
  activeAgentID = null;
  activeAgent = null;
  allAgents = [];
  seenTaskIDs = new Set();
  currentOutputs = [];
  outputsRequestID++;
  pendingUploadFile = null;
  taskHistory = [];
  taskHistoryIndex = -1;
  taskDrafts = new Map();
  interactiveHistory = [];
  interactiveHistoryIndex = -1;
  selectedTaskType = 'shell';
  clearKillConfirmation();
  setTaskStatus('', '');
  hasHydratedOutputs = false;
  followOutput = true;
  pendingPathCompletion = null;
  queuedCompletionPath = '';
  pathBrowseStates = new Map();
  deferredPathCompletionOutputs = new Map();
  deferredPathBrowseOutputs = new Map();
  artifactLibrary = [];
  auditLog = [];
  activeSessionPanel = 'all';
  fileBrowserPath = '';
  fileBrowserResult = null;
  outputSearchExpanded = false;
  clearPathCompletionTimer();
  hidePathSuggestions();
  hideFileBrowser();

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
  $('output-toolbar').hidden = true;
  $('clear-btn').hidden = true;
  $('clear-btn').disabled = false;
  $('save-output-btn').hidden = true;
  $('save-output-btn').disabled = false;
  $('session-details-btn').hidden = true;
  $('console-meta').hidden = true;
  $('console-title').textContent = 'Select a session';
  closeSessionDetailsModal();
  closeFileBrowserModal();
  closeClearConfirmModal();
  closeKillConfirmModal();
  $('output-resizer').hidden = true;
  $('session-count').textContent = '0 sessions';
  $('refresh-indicator').textContent = 'Signed out';
  $('count-online').textContent = '0';
  $('count-stale').textContent = '0';
  $('count-offline').textContent = '0';
  $('meta-state').className = 'meta-chip meta-state-chip';
  $('session-warning').hidden = true;
  $('session-warning').textContent = '';
  $('jobs-list').textContent = '';
  $('artifact-list').textContent = '';
  $('audit-list').textContent = '';
  $('tag-input').value = '';
  $('notes-input').value = '';
  $('output-search').value = '';
  updateOutputSearchUI(false);
  clearPendingUpload();
  closeClearConfirmModal();
  closeKillConfirmModal();
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
  const pathBrowserWaiting = pathBrowserTaskSelected() && !activePathBrowseReady();

  taskTypeButtons.forEach(button => {
    button.disabled = isBusy || interactiveMode;
  });
  if (taskTypeSelect) taskTypeSelect.disabled = isBusy || interactiveMode;
  if (taskTypeButton) taskTypeButton.disabled = isBusy || interactiveMode;
  if (isBusy || interactiveMode) closeTaskTypeMenu();

  $('send-btn').disabled = isBusy || pathBrowserWaiting || (selectedTaskType === 'upload' && !pendingUploadFile);
  $('choose-file-btn').disabled = isBusy || !activeAgentID || pathBrowserWaiting;
  $('browse-path-btn').disabled = isBusy || !activeAgentID || pathBrowserWaiting || (selectedTaskType === 'upload' && !pendingUploadFile);
  $('cancel-task-select').disabled = isBusy;
  $('cancel-task-btn').disabled = isBusy;
  $('clear-btn').disabled = isBusy;
  $('save-output-btn').disabled = isBusy;

  if (interactiveMode) {
    $('task-input').disabled = isBusy || !interactiveReady;
  } else {
    $('task-input').disabled = isBusy || !TASK_TYPES[selectedTaskType].requiresPayload || pathBrowserWaiting;
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
    warmOnlinePathBrowsers(allAgents);
    syncActiveAgent();
    renderAgentList();
    renderSessionPanels();
    loadAudit();
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
  ensurePathBrowserForAgent(activeAgent);
  updateSessionHeader();
}

function clearActiveSession() {
  saveActiveTaskDraft();
  exitInteractiveMode(false);
  resetActivePathCompletion();
  stopSSEStream();
  activeAgentID = null;
  activeAgent = null;
  currentOutputs = [];
  seenTaskIDs = new Set();
  outputsRequestID++;
  fileBrowserPath = '';
  fileBrowserResult = null;
  clearKillConfirmation();
  hasHydratedOutputs = false;
  followOutput = true;
  $('output').textContent = '';
  $('input-area').hidden = true;
  $('clear-btn').hidden = true;
  $('save-output-btn').hidden = true;
  $('session-details-btn').hidden = true;
  $('console-meta').hidden = true;
  $('console-title').textContent = 'Select a session';
  closeSessionDetailsModal();
  closeFileBrowserModal();
  closeClearConfirmModal();
  $('output-resizer').hidden = true;
  $('session-warning').hidden = true;
  $('session-warning').textContent = '';
  artifactLibrary = [];
  hideFileBrowser();
  activeSessionPanel = 'all';
  updateSessionPanelTabs();
  renderSessionPanels();
  clearPendingUpload();
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

  const card = document.createElement('article');
  card.className = 'agent-card';
  card.title = agent.id;
  card.tabIndex = taskRequestInFlight ? -1 : 0;
  card.setAttribute('role', 'button');
  card.setAttribute('aria-disabled', taskRequestInFlight ? 'true' : 'false');
  card.addEventListener('click', () => {
    if (!taskRequestInFlight) selectAgent(agent);
  });
  card.addEventListener('keydown', e => {
    if (taskRequestInFlight || (e.key !== 'Enter' && e.key !== ' ')) return;
    e.preventDefault();
    selectAgent(agent);
  });

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

  const actions = document.createElement('div');
  actions.className = 'agent-actions';

  const killButton = document.createElement('button');
  killButton.type = 'button';
  killButton.className = 'agent-kill-btn';
  killButton.textContent = 'Kill';
  killButton.disabled = taskRequestInFlight;
  killButton.addEventListener('click', e => {
    e.stopPropagation();
    openKillConfirmModal(agent);
  });
  actions.appendChild(killButton);

  card.appendChild(topRow);
  card.appendChild(bottomRow);
  card.appendChild(idLabel);
  card.appendChild(actions);
  li.appendChild(card);

  return li;
}

function formatAgentPlatform(agent) {
  const os = agent.os || 'unknown';
  const arch = agent.arch || 'unknown';
  const transport = agent.transport ? ' / ' + String(agent.transport).toUpperCase() : '';
  return os + ' / ' + arch + transport;
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
  saveActiveTaskDraft();
  exitInteractiveMode(false);
  resetActivePathCompletion();
  closeClearConfirmModal();
  activeAgentID = agent.id;
  activeAgent = agent;
  seenTaskIDs = new Set();
  currentOutputs = [];
  outputsRequestID++;
  clearKillConfirmation();
  hasHydratedOutputs = false;
  followOutput = true;
  $('output').textContent = '';
  $('output-search').value = '';
  updateOutputSearchUI(false);
  clearPendingUpload();
  stopSSEStream();
  renderAgentList();
  updateSessionHeader();
  updateTaskContextStatus();
  updateOutputControls();
  updateOutputEmptyState();
  startSSEStream(agent.id, false);
  ensurePathBrowserForAgent(agent);
  loadOutputs();
  restoreActiveTaskDraft();
  focusPrimaryInput(false);
}

function updateSessionHeader() {
  if (!activeAgent) {
    $('console-title').textContent = 'Select a session';
    $('console-meta').hidden = true;
    $('input-area').hidden = true;
    $('clear-btn').hidden = true;
    $('save-output-btn').hidden = true;
    $('session-details-btn').hidden = true;
    closeSessionDetailsModal();
    closeFileBrowserModal();
    closeClearConfirmModal();
    closeKillConfirmModal();
    $('output-toolbar').hidden = true;
    $('output-resizer').hidden = true;
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
  $('save-output-btn').hidden = false;
  $('session-details-btn').hidden = false;
  $('output-toolbar').hidden = false;
  $('output-resizer').hidden = false;
  $('tag-input').value = (activeAgent.tags || []).join(', ');
  $('notes-input').value = activeAgent.notes || '';
  updateSessionWarning();

  if (!interactiveMode) applyTaskTypeUI();
  renderSessionPanels();
}

function applyOutputSearch() {
  const input = $('output-search');
  const query = input ? input.value.trim().toLowerCase() : '';
  Array.from($('output').children).forEach(child => {
    if (!query) {
      child.hidden = false;
      return;
    }
    const haystack = child.dataset.searchText || child.textContent.toLowerCase();
    child.hidden = !haystack.includes(query);
  });
}

function updateOutputSearchUI(expanded) {
  outputSearchExpanded = Boolean(expanded);
  const toggle = $('output-search-toggle');
  const panel = $('output-search-panel');
  if (!toggle || !panel) return;

  toggle.setAttribute('aria-expanded', outputSearchExpanded ? 'true' : 'false');
  toggle.classList.toggle('active', outputSearchExpanded);
  panel.hidden = !outputSearchExpanded;

  if (!outputSearchExpanded) {
    $('output-search').value = '';
    applyOutputSearch();
    return;
  }

  window.requestAnimationFrame(() => $('output-search').focus());
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

  const agentID = activeAgentID;
  const requestID = ++outputsRequestID;
  try {
    const resp = await apiFetch('/api/agents/' + agentID + '/tasks');
    if (!resp.ok) return;

    const hydrateOnly = !hasHydratedOutputs;
    const outputs = await resp.json();
    if (agentID !== activeAgentID || requestID !== outputsRequestID) return;
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

  if (output.type === 'pathbrowse') {
    handlePathBrowseOutput(output);
    return;
  }

  if (output.type === 'complete') {
    handlePathCompletionOutput(output);
    return;
  }

  if (output.type === 'download_progress') {
    const baseID = baseDownloadTaskID(output.task_id);
    const state = downloadTasks.get(baseID);
    if (state) {
      state.status = 'progress';
      if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
    }
    if (output.output && output.output.trim()) appendOutput(output.output.trimEnd());
    renderSessionPanels();
    return;
  }

  if (output.type === 'ls' && output.output) {
    if (handleFileBrowserOutput(output)) {
      renderSessionPanels();
      return;
    }
  }

  if (output.error) {
    if (output.type === 'ls' && !$('file-browser-modal').hidden) {
      renderFileBrowserError(output.error);
    }
    appendOutput('[err ' + short + ' ' + ts + '] ' + output.error);
    renderSessionPanels();
    return;
  }

  if (output.type === 'download' && output.output) {
    const payload = output.output.trim();
    if (!payload) {
      appendOutput('[err ' + short + ' ' + ts + '] invalid download payload');
      return;
    }

    appendDownloadResult(output.task_id, short, ts, payload, historical);
    renderSessionPanels();
    return;
  }

  if ((output.type === 'screenshot' || output.type === 'peas' || output.type === 'snapshot') && output.output) {
    appendArtifactResult(
      short,
      ts,
      output.output.trim(),
      output.type === 'peas'
        ? 'PEAS output ready'
        : output.type === 'snapshot'
          ? 'host info ready'
          : 'screenshot ready',
      output.type === 'screenshot' ? 'Save Screenshot' : 'Save Output',
      historical,
    );
    renderSessionPanels();
    return;
  }

  if (output.output && output.output.trim()) {
    if (interactiveMode && output.type === 'shell') {
      appendOutput(output.output.trimEnd(), 'interactive-out');
    } else {
      appendOutput('[' + short + ' ' + ts + ']\n' + output.output.trimEnd());
    }
  }
  renderSessionPanels();
}

taskTypeButtons.forEach(button => {
  button.addEventListener('click', () => setTaskType(button.dataset.taskType));
});
if (taskTypeSelect) {
  taskTypeSelect.addEventListener('change', () => setTaskType(taskTypeSelect.value));
}
initTaskTypeMenu();

sessionPanelTabs.forEach(button => {
  button.addEventListener('click', () => setSessionPanel(button.dataset.panel));
});
if (sessionPanelFilter) {
  sessionPanelFilter.addEventListener('change', () => setSessionPanel(sessionPanelFilter.value));
}
$('output-search-toggle').addEventListener('click', () => updateOutputSearchUI(!outputSearchExpanded));
$('session-details-btn').addEventListener('click', openSessionDetailsModal);
$('session-details-close-btn').addEventListener('click', closeSessionDetailsModal);
document.querySelector('[data-close-session-details]').addEventListener('click', closeSessionDetailsModal);
$('file-browser-close-btn').addEventListener('click', closeFileBrowserModal);
document.querySelector('[data-close-file-browser]').addEventListener('click', closeFileBrowserModal);
$('clear-cancel-btn').addEventListener('click', closeClearConfirmModal);
document.querySelector('[data-close-clear-confirm]').addEventListener('click', closeClearConfirmModal);
$('clear-confirm-btn').addEventListener('click', confirmClearOutput);
$('kill-cancel-btn').addEventListener('click', closeKillConfirmModal);
document.querySelector('[data-close-kill-confirm]').addEventListener('click', closeKillConfirmModal);
$('kill-confirm-btn').addEventListener('click', confirmKillSession);
initDraggableModals();

function initDraggableModals() {
  [$('session-rail'), $('file-browser-panel')].forEach(panel => {
    if (!panel) return;
    const header = panel.querySelector('.rail-header');
    if (!header) return;

    let dragging = false, ox = 0, oy = 0;

    header.addEventListener('mousedown', e => {
      if (e.target.closest('button, select, input')) return;
      dragging = true;
      if (!panel.classList.contains('panel-dragged')) {
        const rect = panel.getBoundingClientRect();
        panel.style.position = 'absolute';
        panel.style.margin = '0';
        panel.style.left = rect.left + 'px';
        panel.style.top = rect.top + 'px';
        panel.classList.add('panel-dragged');
      }
      ox = e.clientX - panel.getBoundingClientRect().left;
      oy = e.clientY - panel.getBoundingClientRect().top;
      e.preventDefault();
    });

    document.addEventListener('mousemove', e => {
      if (!dragging) return;
      panel.style.left = (e.clientX - ox) + 'px';
      panel.style.top = (e.clientY - oy) + 'px';
    });

    document.addEventListener('mouseup', () => { dragging = false; });
  });
}

function initTaskTypeMenu() {
  if (!taskTypeSelect || !taskTypeButton || !taskTypeList) return;

  taskTypeList.textContent = '';
  Array.from(taskTypeSelect.options).forEach(option => {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'task-type-option';
    item.role = 'option';
    item.dataset.value = option.value;
    item.textContent = option.textContent;
    item.addEventListener('click', () => {
      setTaskType(option.value);
      closeTaskTypeMenu();
      taskTypeButton.focus();
    });
    taskTypeList.appendChild(item);
  });

  taskTypeButton.addEventListener('click', () => {
    if (taskTypeButton.disabled) return;
    setTaskTypeMenuOpen(taskTypeList.hidden);
  });
  taskTypeButton.addEventListener('keydown', event => {
    if (event.key === 'ArrowDown' || event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      setTaskTypeMenuOpen(true);
      focusTaskTypeOption(selectedTaskType);
      return;
    }
    if (event.key === 'Escape') {
      closeTaskTypeMenu();
    }
  });
  taskTypeList.addEventListener('keydown', event => {
    const options = taskTypeOptions();
    const index = options.indexOf(document.activeElement);
    if (event.key === 'Escape') {
      event.preventDefault();
      closeTaskTypeMenu();
      taskTypeButton.focus();
      return;
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      (options[index + 1] || options[0])?.focus();
      return;
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault();
      (options[index - 1] || options[options.length - 1])?.focus();
    }
  });
  document.addEventListener('click', event => {
    if (!taskTypeMenu || taskTypeMenu.contains(event.target)) return;
    closeTaskTypeMenu();
  });
  syncTaskTypeMenu();
}

function setTaskTypeMenuOpen(isOpen) {
  if (!taskTypeButton || !taskTypeList) return;
  if (isOpen && taskTypeButton.disabled) return;
  taskTypeButton.setAttribute('aria-expanded', isOpen ? 'true' : 'false');
  taskTypeList.hidden = !isOpen;
}

function closeTaskTypeMenu() {
  setTaskTypeMenuOpen(false);
}

function taskTypeOptions() {
  return Array.from(taskTypeList ? taskTypeList.querySelectorAll('.task-type-option') : []);
}

function focusTaskTypeOption(value) {
  const option = taskTypeOptions().find(item => item.dataset.value === value) || taskTypeOptions()[0];
  if (option) option.focus();
}

function syncTaskTypeMenu() {
  if (!taskTypeSelect || !taskTypeButtonLabel || !taskTypeButton) return;
  const option = taskTypeSelect.options[taskTypeSelect.selectedIndex];
  taskTypeButtonLabel.textContent = option ? option.textContent : selectedTaskType;
  taskTypeOptions().forEach(item => {
    const active = item.dataset.value === selectedTaskType;
    item.setAttribute('aria-selected', active ? 'true' : 'false');
  });
}

function setTaskType(type) {
  if (!TASK_TYPES[type]) return;
  saveActiveTaskDraft();
  selectedTaskType = type;
  pendingPathCompletion = null;
  queuedCompletionPath = '';
  clearPathCompletionTimer();
  hidePathSuggestions();
  taskHistoryIndex = -1;
  if (type !== 'kill') clearKillConfirmation();
  clearTaskInputError();
  applyTaskTypeUI();
  restoreActiveTaskDraft();
  if (pathSuggestionTaskSelected()) schedulePathCompletion();
  focusPrimaryInput(true);
}

function applyTaskTypeUI() {
  const config = TASK_TYPES[selectedTaskType];
  const pathTaskSelected = pathSuggestionTaskSelected();
  const pathBrowserSelected = pathBrowserTaskSelected();
  const pathBrowserWaiting = pathBrowserSelected && !activePathBrowseReady();
  const agentOnline = activeAgent && getAgentState(activeAgent) === 'online';

  taskTypeButtons.forEach(button => {
    const active = button.dataset.taskType === selectedTaskType;
    button.classList.toggle('active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
    button.disabled = taskRequestInFlight;
  });
  if (taskTypeSelect) {
    taskTypeSelect.value = selectedTaskType;
    taskTypeSelect.disabled = taskRequestInFlight;
  }
  if (taskTypeButton) {
    taskTypeButton.disabled = taskRequestInFlight;
    syncTaskTypeMenu();
  }

  if (interactiveMode) {
    if (taskTypeSelect) taskTypeSelect.hidden = true;
    if (taskTypeMenu) taskTypeMenu.hidden = true;
    closeTaskTypeMenu();
    document.querySelector('.command-line').hidden = false;
    hidePathSuggestions();
    return;
  }

  if (taskTypeSelect) taskTypeSelect.hidden = false;
  if (taskTypeMenu) taskTypeMenu.hidden = false;
  document.querySelector('.command-line').hidden = !config.requiresPayload;
  $('task-help').classList.remove('error-copy');
  $('composer-note').classList.remove('error-note');
  $('task-help').textContent = config.help;
  $('composer-note').textContent = pathBrowserSelected
    ? pathBrowserComposerNote(agentOnline)
    : config.note;
  $('task-input').classList.remove('input-error');
  $('task-input').disabled = taskRequestInFlight || !config.requiresPayload || pathBrowserWaiting;
  $('task-input').placeholder = pathTaskSelected
    ? pathBrowserPlaceholder(agentOnline)
    : config.placeholder;
  $('task-input').inputMode = config.inputMode;
  $('task-input').maxLength = pathTaskSelected
    ? MAX_REMOTE_PATH
    : selectedTaskType === 'sleep'
      ? String(MAX_SLEEP_SECONDS).length
      : 48000;
  const uploadSelected = selectedTaskType === 'upload';
  const downloadSelected = selectedTaskType === 'download';
  $('send-btn').textContent = selectedTaskType === 'kill' && killConfirmationActive()
    ? 'Confirm Kill'
    : config.buttonLabel;
  $('send-btn').hidden = false;
  $('send-btn').disabled = taskRequestInFlight || pathBrowserWaiting || (uploadSelected && !pendingUploadFile);
  $('send-btn').classList.toggle('warn-button', selectedTaskType === 'interactive');
  $('send-btn').classList.toggle('danger-button', selectedTaskType === 'kill');
  $('exit-interactive-btn').hidden = true;
  $('interactive-prompt').hidden = true;

  $('choose-file-btn').hidden = !uploadSelected;
  $('choose-file-btn').disabled = taskRequestInFlight || !activeAgentID || pathBrowserWaiting;
  $('browse-path-btn').hidden = !(uploadSelected || downloadSelected);
  $('browse-path-btn').disabled = taskRequestInFlight || !activeAgentID || pathBrowserWaiting || (uploadSelected && !pendingUploadFile);
  $('browse-path-btn').title = uploadSelected && !pendingUploadFile
    ? 'Choose a local file before browsing for an upload destination'
    : downloadSelected
      ? 'Browse remote files and download directly'
      : 'Browse remote directories to choose a destination';
  updateUploadFilenameLabel();

  if (!config.requiresPayload) {
    $('task-input').value = '';
  }
  if (!pathTaskSelected) hidePathSuggestions();
  if (pathBrowserSelected && agentOnline) ensurePathBrowserForAgent(activeAgent);

  updateTaskContextStatus();
}

function updateUploadFilenameLabel() {
  const label = $('upload-filename-label');
  if (selectedTaskType === 'upload' && pendingUploadFile) {
    label.textContent = pendingUploadFile.name + ' ->';
    label.hidden = false;
  } else {
    label.textContent = '';
    label.hidden = true;
  }
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

function taskDraftKey(agentID, taskType) {
  return agentID + ':' + taskType;
}

function saveActiveTaskDraft() {
  if (!activeAgentID || interactiveMode) return;
  const config = TASK_TYPES[selectedTaskType];
  if (!config || !config.requiresPayload) return;

  const key = taskDraftKey(activeAgentID, selectedTaskType);
  const value = $('task-input').value;
  if (value) taskDrafts.set(key, value);
  else taskDrafts.delete(key);
}

function restoreActiveTaskDraft() {
  if (!activeAgentID || interactiveMode) return;
  const config = TASK_TYPES[selectedTaskType];
  if (!config || !config.requiresPayload) return;

  const key = taskDraftKey(activeAgentID, selectedTaskType);
  $('task-input').value = taskDrafts.get(key) || '';
  clearTaskInputError();
  if (pathSuggestionTaskSelected()) schedulePathCompletion();
}

function clearActiveTaskDraft(taskType) {
  if (!activeAgentID) return;
  taskDrafts.delete(taskDraftKey(activeAgentID, taskType));
}

function pathBrowserComposerNote(agentOnline) {
  if (!agentOnline) return 'Path browser starts automatically once the selected session is online.';
  if (!activePathBrowseReady()) {
    return 'Preparing the remote path browser. Input unlocks when the session confirms fast browsing.';
  }
  return TASK_TYPES[selectedTaskType].note;
}

function pathBrowserPlaceholder(agentOnline) {
  if (!agentOnline) return 'Waiting for online session...';
  if (!activePathBrowseReady()) return 'Preparing remote path browser...';
  return TASK_TYPES[selectedTaskType].placeholder;
}

function pathSuggestionTaskSelected() {
  return selectedTaskType === 'download' || selectedTaskType === 'upload';
}

function pathBrowserTaskSelected() {
  return selectedTaskType === 'download' || selectedTaskType === 'upload';
}

$('task-input').addEventListener('input', () => {
  clearTaskInputError();
  saveActiveTaskDraft();
  if (pathSuggestionTaskSelected() && !interactiveMode) {
    schedulePathCompletion();
  } else {
    hidePathSuggestions();
  }
});

$('send-btn').addEventListener('click', sendTask);
$('cancel-task-btn').addEventListener('click', () => {
  const taskID = $('cancel-task-select').value;
  if (taskID) queueCancelTask(taskID);
});
$('cancel-task-select').addEventListener('change', updateCancellationControls);

$('save-metadata-btn').addEventListener('click', async () => {
  if (!activeAgentID) return;
  const tags = $('tag-input').value.split(',').map(tag => tag.trim()).filter(Boolean);
  try {
    const resp = await apiFetch('/api/agents/' + activeAgentID + '/metadata', {
      method: 'PUT',
      body: JSON.stringify({ notes: $('notes-input').value, tags }),
    });
    if (!resp.ok) {
      appendOutput('[-] save notes failed (' + resp.status + ')');
      return;
    }
    activeAgent = await resp.json();
    allAgents = allAgents.map(agent => agent.id === activeAgentID ? { ...agent, ...activeAgent } : agent);
    appendOutput('[>] notes saved');
    renderAgentList();
    renderSessionPanels();
  } catch (err) {
    appendOutput('[-] save notes error: ' + err.message);
  }
});

$('output-search').addEventListener('input', applyOutputSearch);

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

function schedulePathCompletion() {
  clearPathCompletionTimer();

  const path = $('task-input').value.trim();
  if (!activePathBrowseReady() || !path || !activeAgentID || taskRequestInFlight || !pathSuggestionTaskSelected() || hasInvalidPathChars(path)) {
    hidePathSuggestions();
    return;
  }

  queuedCompletionPath = path;
  pathCompletionTimer = window.setTimeout(() => {
    pathCompletionTimer = null;
    requestPathCompletion(path);
  }, PATH_COMPLETION_DEBOUNCE_MS);
}

function clearPathCompletionTimer() {
  if (pathCompletionTimer === null) return;
  window.clearTimeout(pathCompletionTimer);
  pathCompletionTimer = null;
}

function warmOnlinePathBrowsers(agents) {
  const liveIDs = new Set();

  for (const agent of agents) {
    if (!agent || !agent.id) continue;
    liveIDs.add(agent.id);

    if (getAgentState(agent) === 'online') {
      ensurePathBrowserForAgent(agent);
    } else {
      markPathBrowserOffline(agent.id);
    }
  }

  for (const agentID of Array.from(pathBrowseStates.keys())) {
    if (!liveIDs.has(agentID)) resetPathBrowserState(agentID, false);
  }
}

function getPathBrowseState(agentID, create) {
  if (!agentID) return null;
  let state = pathBrowseStates.get(agentID);
  if (!state && create) {
    state = {
      ready: false,
      readyAt: 0,
      startTaskID: '',
      renewTimer: null,
    };
    pathBrowseStates.set(agentID, state);
  }
  return state || null;
}

function activePathBrowseReady() {
  if (!activeAgentID || !activeAgent || getAgentState(activeAgent) !== 'online') return false;
  const state = getPathBrowseState(activeAgentID, false);
  return pathBrowseStateReady(state);
}

function pathBrowseStateReady(state) {
  return Boolean(state && state.ready && Date.now() - state.readyAt < PATH_BROWSE_FAST_WINDOW_MS);
}

function ensurePathBrowserForAgent(agent) {
  if (!token || !agent || !agent.id || interactiveMode) return;
  if (getAgentState(agent) !== 'online') {
    markPathBrowserOffline(agent.id);
    return;
  }

  const state = getPathBrowseState(agent.id, true);
  if (pathBrowseStateReady(state) || state.startTaskID) return;
  state.ready = false;

  queuePathBrowseStart(agent.id, false);
}

async function queuePathBrowseStart(agentID, isRenewal) {
  const state = getPathBrowseState(agentID, true);
  if (!token || !agentID || state.startTaskID) return;

  const agent = allAgents.find(item => item.id === agentID) || (agentID === activeAgentID ? activeAgent : null);
  if (!agent || getAgentState(agent) !== 'online') {
    markPathBrowserOffline(agentID);
    return;
  }

  if (!isRenewal) state.ready = false;
  state.startTaskID = PENDING_TASK_ID;
  applyPathBrowserUI(agentID);

  try {
    const data = await submitTask(agentID, { type: 'pathbrowse', payload: 'start' });
    if (!data) {
      state.ready = false;
      state.startTaskID = '';
      applyPathBrowserUI(agentID);
      return;
    }

    state.startTaskID = data.task_id;
    if (isRenewal) {
      state.ready = true;
      state.readyAt = Date.now();
      state.startTaskID = '';
      schedulePathBrowseRenewal(agentID);
      applyPathBrowserUI(agentID);
      return;
    }

    const deferred = deferredPathBrowseOutputs.get(data.task_id);
    if (deferred) {
      deferredPathBrowseOutputs.delete(data.task_id);
      handlePathBrowseOutput(deferred);
    }
  } catch (err) {
    state.ready = false;
    state.startTaskID = '';
    if (agentID === activeAgentID) {
      renderPathSuggestions('', [], 'Path browser failed to start: ' + err.message);
    }
    applyPathBrowserUI(agentID);
  }
}

function handlePathBrowseOutput(output) {
  const state = getPathBrowseState(activeAgentID, false);
  if (!state || !state.startTaskID) return;

  if (state.startTaskID === PENDING_TASK_ID) {
    deferredPathBrowseOutputs.set(output.task_id, output);
    return;
  }
  if (output.task_id !== state.startTaskID) return;

  state.startTaskID = '';

  if (output.error) {
    state.ready = false;
    renderPathSuggestions('', [], output.error);
    applyPathBrowserUI(activeAgentID);
    return;
  }

  state.ready = Boolean(output.output && output.output.includes('ready'));
  if (state.ready) {
    const outputTime = output.timestamp ? new Date(output.timestamp).getTime() : NaN;
    state.readyAt = Number.isFinite(outputTime) ? outputTime : Date.now();
    if (!pathBrowseStateReady(state)) {
      state.ready = false;
      queuePathBrowseStart(activeAgentID, false);
      applyPathBrowserUI(activeAgentID);
      return;
    }
    schedulePathBrowseRenewal(activeAgentID);
    if (pathSuggestionTaskSelected() && $('task-input').value.trim()) schedulePathCompletion();
  }
  applyPathBrowserUI(activeAgentID);
}

function schedulePathBrowseRenewal(agentID) {
  const state = getPathBrowseState(agentID, false);
  if (!state) return;
  clearPathBrowseRenewal(agentID);
  state.renewTimer = window.setTimeout(() => {
    state.renewTimer = null;
    const agent = allAgents.find(item => item.id === agentID);
    if (state.ready && agent && getAgentState(agent) === 'online') {
      queuePathBrowseStart(agentID, true);
    }
  }, PATH_BROWSE_RENEW_MS);
}

function clearPathBrowseRenewal(agentID) {
  const state = getPathBrowseState(agentID, false);
  if (!state || state.renewTimer === null) return;
  window.clearTimeout(state.renewTimer);
  state.renewTimer = null;
}

function markPathBrowserOffline(agentID) {
  const state = getPathBrowseState(agentID, false);
  if (!state) return;
  clearPathBrowseRenewal(agentID);
  state.ready = false;
  state.readyAt = 0;
  state.startTaskID = '';
  if (agentID === activeAgentID) applyPathBrowserUI(agentID);
}

function resetPathBrowserState(agentID, sendStop) {
  const state = getPathBrowseState(agentID, false);
  if (state) clearPathBrowseRenewal(agentID);
  pathBrowseStates.delete(agentID);

  if (sendStop && agentID && token) {
    apiFetch('/api/agents/' + agentID + '/task', {
      method: 'POST',
      body: JSON.stringify({ type: 'pathbrowse', payload: 'stop' }),
    }).catch(() => {});
  }
}

function resetAllPathBrowserStates(sendStop) {
  for (const agentID of Array.from(pathBrowseStates.keys())) {
    resetPathBrowserState(agentID, sendStop);
  }
}

function resetActivePathCompletion() {
  pendingPathCompletion = null;
  queuedCompletionPath = '';
  deferredPathCompletionOutputs = new Map();
  clearPathCompletionTimer();
  hidePathSuggestions();
}

function applyPathBrowserUI(agentID) {
  if (agentID === activeAgentID && pathBrowserTaskSelected() && !interactiveMode) applyTaskTypeUI();
}

async function requestPathCompletion(path) {
  if (!activePathBrowseReady() || !activeAgentID || taskRequestInFlight || !pathSuggestionTaskSelected()) return;
  if (!path || hasInvalidPathChars(path)) return;

  if (pendingPathCompletion) {
    queuedCompletionPath = path;
    return;
  }

  const targetAgentID = activeAgentID;
  pendingPathCompletion = {
    agentID: targetAgentID,
    taskID: PENDING_TASK_ID,
    input: path,
  };
  showPathSuggestionsLoading(path);

  try {
    const data = await submitTask(targetAgentID, { type: 'complete', payload: path });
    if (!data) {
      pendingPathCompletion = null;
      hidePathSuggestions();
      return;
    }
    if (!pendingPathCompletion || targetAgentID !== activeAgentID) return;
    pendingPathCompletion.taskID = data.task_id;
    const deferred = deferredPathCompletionOutputs.get(data.task_id);
    if (deferred) {
      deferredPathCompletionOutputs.delete(data.task_id);
      handlePathCompletionOutput(deferred);
    }
  } catch (err) {
    pendingPathCompletion = null;
    renderPathSuggestions(path, [], 'Path completion failed: ' + err.message);
  }
}

function handlePathCompletionOutput(output) {
  if (!pendingPathCompletion) return;
  if (pendingPathCompletion.taskID === PENDING_TASK_ID) {
    deferredPathCompletionOutputs.set(output.task_id, output);
    return;
  }
  if (output.task_id !== pendingPathCompletion.taskID) return;

  const pending = pendingPathCompletion;
  pendingPathCompletion = null;

  if (pending.agentID !== activeAgentID) return;

  const currentPath = $('task-input').value.trim();
  const nextPath = queuedCompletionPath;
  queuedCompletionPath = '';

  if (nextPath && nextPath !== pending.input && nextPath === currentPath) {
    requestPathCompletion(nextPath);
  }

  if (currentPath !== pending.input) return;

  if (output.error) {
    renderPathSuggestions(pending.input, [], output.error);
    return;
  }

  let result;
  try {
    result = JSON.parse(output.output || '{}');
  } catch (_) {
    renderPathSuggestions(pending.input, [], 'Path completion returned an invalid response.');
    return;
  }

  const items = Array.isArray(result.items) ? result.items : [];
  renderPathSuggestions(pending.input, items, '');
}

function showPathSuggestionsLoading(path) {
  const panel = $('path-suggestions');
  panel.textContent = '';
  panel.hidden = false;

  const note = document.createElement('div');
  note.className = 'path-suggestion-note';
  note.textContent = 'Searching ' + path + '...';
  panel.appendChild(note);
}

function renderPathSuggestions(input, items, message) {
  const panel = $('path-suggestions');
  panel.textContent = '';
  panel.hidden = false;

  const header = document.createElement('div');
  header.className = 'path-suggestion-header';
  header.textContent = message || ('Suggestions for ' + input);
  panel.appendChild(header);

  const parentPath = parentDirectoryPath(input);
  if (parentPath) {
    const parentButton = document.createElement('button');
    parentButton.type = 'button';
    parentButton.className = 'path-suggestion-item path-suggestion-parent';
    parentButton.textContent = '...  ' + parentPath;
    parentButton.title = 'Go up to ' + parentPath;
    parentButton.addEventListener('click', () => {
      $('task-input').value = parentPath;
      clearTaskInputError();
      saveActiveTaskDraft();
      focusPrimaryInput(false, true);
      requestPathCompletion(parentPath);
    });
    panel.appendChild(parentButton);
  }

  if (!items.length) {
    const empty = document.createElement('div');
    empty.className = 'path-suggestion-note';
    empty.textContent = message ? '' : 'No matching remote paths.';
    if (empty.textContent) panel.appendChild(empty);
    return;
  }

  for (const item of items) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'path-suggestion-item';
    button.textContent = item;
    button.title = 'Use ' + item;
    button.addEventListener('click', () => {
      $('task-input').value = item;
      clearTaskInputError();
      saveActiveTaskDraft();
      focusPrimaryInput(false, true);
      if (isDirectorySuggestion(item)) {
        requestPathCompletion(item);
      } else {
        hidePathSuggestions();
      }
    });
    panel.appendChild(button);
  }
}

function isDirectorySuggestion(path) {
  return path.endsWith('/') || path.endsWith('\\');
}

function parentDirectoryPath(path) {
  if (!isDirectorySuggestion(path)) return '';
  const separator = path.endsWith('\\') ? '\\' : '/';
  const trimmed = path.slice(0, -1);
  if (!trimmed) return '';

  const idx = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'));
  if (idx < 0) return '';
  if (idx === 0 && separator === '/') return '/';

  return trimmed.slice(0, idx + 1);
}

function hidePathSuggestions() {
  const panel = $('path-suggestions');
  if (!panel) return;
  panel.hidden = true;
  panel.textContent = '';
}

function handleFileBrowserOutput(output) {
  let result;
  try {
    result = JSON.parse(output.output || '{}');
  } catch (_) {
    return false;
  }
  if (!result || !Array.isArray(result.entries) || typeof result.path !== 'string') return false;

  fileBrowserResult = result;
  fileBrowserPath = result.path;
  if (!$('file-browser-modal').hidden) {
    renderFileBrowser(result);
  }
  return true;
}

function renderFileBrowserPlaceholder() {
  const panel = $('file-browser');
  if (!panel) return;
  panel.hidden = false;
  panel.textContent = '';
  const empty = document.createElement('div');
  empty.className = 'file-browser-empty';
  empty.textContent = 'Loading remote file browser...';
  panel.appendChild(empty);
}

function renderFileBrowserLoading(path) {
  const panel = $('file-browser');
  if (!panel) return;
  panel.hidden = false;
  panel.textContent = '';
  const empty = document.createElement('div');
  empty.className = 'file-browser-empty';
  empty.textContent = 'Loading ' + path + '...';
  panel.appendChild(empty);
}

function renderFileBrowserError(message) {
  const panel = $('file-browser');
  if (!panel) return;
  panel.hidden = false;
  panel.textContent = '';
  const empty = document.createElement('div');
  empty.className = 'file-browser-empty error-copy';
  empty.textContent = message || 'Unable to browse this directory.';
  panel.appendChild(empty);
}

function hideFileBrowser() {
  const panel = $('file-browser');
  if (!panel) return;
  panel.textContent = '';
}

function openFileBrowserModal(mode) {
  if (!activeAgentID || taskRequestInFlight) return;
  fileBrowserMode = mode === 'select-upload' ? 'select-upload' : 'browse';
  $('file-browser-title').textContent = fileBrowserMode === 'select-upload'
    ? 'Choose Upload Destination'
    : 'Remote Files';
  $('file-browser-modal').hidden = false;
  ensurePathBrowserForAgent(activeAgent);
  if (fileBrowserResult) {
    renderFileBrowser(fileBrowserResult);
  } else {
    renderFileBrowserPlaceholder();
    queueFileBrowserPath(fileBrowserPath || '.');
  }
  window.requestAnimationFrame(() => $('file-browser-close-btn').focus());
}

function closeFileBrowserModal() {
  const modal = $('file-browser-modal');
  if (!modal) return;
  modal.hidden = true;
  fileBrowserMode = 'browse';
}

function openClearConfirmModal() {
  if (!activeAgentID || taskRequestInFlight) return;
  pendingClearAgentID = activeAgentID;
  const label = activeAgent && activeAgent.hostname ? activeAgent.hostname : ('Session ' + activeAgentID.slice(0, 8));
  $('clear-confirm-copy').textContent = 'Clear output history for ' + label + '? This removes persisted output from the server for this session.';
  $('clear-confirm-modal').hidden = false;
  window.requestAnimationFrame(() => $('clear-cancel-btn').focus());
}

function closeClearConfirmModal() {
  const modal = $('clear-confirm-modal');
  if (!modal) return;
  modal.hidden = true;
  pendingClearAgentID = '';
}

async function confirmClearOutput() {
  if (!pendingClearAgentID || taskRequestInFlight) return;
  const targetAgentID = pendingClearAgentID;
  closeClearConfirmModal();
  await clearOutputHistory(targetAgentID);
}

function openKillConfirmModal(agent) {
  if (!agent || !agent.id || taskRequestInFlight) return;
  pendingKillAgentID = agent.id;
  const label = agent.hostname || ('Session ' + agent.id.slice(0, 8));
  $('kill-confirm-copy').textContent = 'Queue a kill task for ' + label + '? This only takes effect after that session checks in and processes the task.';
  $('kill-confirm-modal').hidden = false;
  window.requestAnimationFrame(() => $('kill-cancel-btn').focus());
}

function closeKillConfirmModal() {
  const modal = $('kill-confirm-modal');
  if (!modal) return;
  modal.hidden = true;
  pendingKillAgentID = '';
}

async function confirmKillSession() {
  if (!pendingKillAgentID || taskRequestInFlight) return;
  const targetAgentID = pendingKillAgentID;
  closeKillConfirmModal();
  setQueueBusy(true, 'Queueing kill task...');

  try {
    const data = await submitTask(targetAgentID, { type: 'kill', payload: '' });
    if (data) {
      appendOutput('[>] kill queued  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
      if (targetAgentID === activeAgentID) await refreshActiveAgent();
      else await loadAgents();
    }
  } catch (err) {
    appendOutput('[-] kill request error: ' + err.message, '', targetAgentID);
  } finally {
    setQueueBusy(false, '');
    renderAgentList();
  }
}

function renderFileBrowser(result) {
  const panel = $('file-browser');
  if (!panel) return;
  panel.hidden = false;
  panel.textContent = '';

  const toolbar = document.createElement('div');
  toolbar.className = 'file-browser-toolbar';

  const pathLabel = document.createElement('div');
  pathLabel.className = 'file-browser-path';
  pathLabel.textContent = result.path;
  pathLabel.title = result.path;
  toolbar.appendChild(pathLabel);

  const actions = document.createElement('div');
  actions.className = 'file-browser-actions';
  if (result.parent) {
    actions.appendChild(fileBrowserButton('Up', () => queueFileBrowserPath(result.parent)));
  }
  actions.appendChild(fileBrowserButton('Refresh', () => queueFileBrowserPath(result.path)));
  if (fileBrowserMode === 'select-upload') {
    actions.appendChild(fileBrowserButton('Select This Folder', () => selectUploadDestination(result.path, true)));
  }
  toolbar.appendChild(actions);
  panel.appendChild(toolbar);

  const table = document.createElement('div');
  table.className = 'file-browser-table';
  panel.appendChild(table);

  const header = document.createElement('div');
  header.className = 'file-browser-row file-browser-header';
  header.appendChild(fileBrowserCell('Name'));
  header.appendChild(fileBrowserCell('Size'));
  header.appendChild(fileBrowserCell('Modified'));
  header.appendChild(fileBrowserCell('Actions'));
  table.appendChild(header);

  if (!result.entries.length) {
    const empty = document.createElement('div');
    empty.className = 'file-browser-empty';
    empty.textContent = 'This directory is empty.';
    panel.appendChild(empty);
    return;
  }

  result.entries.forEach(entry => {
    const row = document.createElement('div');
    row.className = 'file-browser-row';
    if (entry.is_dir) row.classList.add('directory');

    const nameCell = fileBrowserCell('');
    const nameButton = document.createElement('button');
    nameButton.type = 'button';
    nameButton.className = 'file-browser-name';
    nameButton.textContent = (entry.is_dir ? '[dir] ' : '[file] ') + entry.name;
    nameButton.title = entry.path;
    nameButton.addEventListener('click', () => {
      if (entry.is_dir) queueFileBrowserPath(entry.path);
      else queueDownloadFromBrowser(entry.path);
    });
    nameCell.appendChild(nameButton);
    row.appendChild(nameCell);

    row.appendChild(fileBrowserCell(entry.is_dir ? '' : formatFileSize(entry.size)));
    row.appendChild(fileBrowserCell(formatBrowserTime(entry.mod_time)));

    const actionCell = fileBrowserCell('');
    actionCell.classList.add('file-browser-actions-cell');
    if (entry.error) {
      actionCell.textContent = entry.error;
    } else if (entry.is_dir) {
      actionCell.appendChild(fileBrowserButton('Open', () => queueFileBrowserPath(entry.path)));
      if (fileBrowserMode === 'select-upload') {
        actionCell.appendChild(fileBrowserButton('Select', () => selectUploadDestination(entry.path, true)));
      }
    } else {
      actionCell.appendChild(fileBrowserDownloadButton(entry));
      if (fileBrowserMode === 'select-upload') {
        actionCell.appendChild(fileBrowserButton('Select', () => selectUploadDestination(entry.path, false)));
      }
    }
    row.appendChild(actionCell);
    table.appendChild(row);
  });
}

function fileBrowserDownloadButton(entry) {
  const state = downloadStateForPath(entry.path);
  if (state && state.status === 'done') {
    return fileBrowserButton('See Download', () => openArtifactsPanel(state.artifactKey));
  }
  if (state && state.status === 'progress') {
    const button = fileBrowserButton('Download in progress', () => {});
    button.disabled = true;
    return button;
  }
  return fileBrowserButton('Download', () => queueDownloadFromBrowser(entry.path));
}

function downloadStateForPath(path) {
  for (const state of downloadTasks.values()) {
    if (state.path === path) return state;
  }
  return null;
}

function openArtifactsPanel(artifactKey) {
  closeFileBrowserModal();
  activeSessionPanel = 'artifacts';
  openSessionDetailsModal();
  if (artifactKey) {
    const row = Array.from(document.querySelectorAll('[data-artifact-key]'))
      .find(item => item.dataset.artifactKey === artifactKey);
    if (row) {
      row.classList.add('panel-item-highlight');
      row.scrollIntoView({ block: 'nearest' });
      window.setTimeout(() => row.classList.remove('panel-item-highlight'), 1800);
    }
  }
}

function selectUploadDestination(entryPath, isDir) {
  const value = uploadDestinationPath(entryPath, isDir);
  $('task-input').value = value;
  clearTaskInputError();
  saveActiveTaskDraft();
  closeFileBrowserModal();
  hidePathSuggestions();
  focusPrimaryInput(false, true);
}

function uploadDestinationPath(entryPath, isDir) {
  if (!isDir || !pendingUploadFile) return entryPath;
  const separator = entryPath.includes('\\') ? '\\' : '/';
  const basePath = isDirectorySuggestion(entryPath) ? entryPath : entryPath + separator;
  return basePath + pendingUploadFile.name;
}

function fileBrowserCell(text) {
  const cell = document.createElement('div');
  cell.className = 'file-browser-cell';
  cell.textContent = text || '';
  return cell;
}

function fileBrowserButton(label, onClick) {
  const button = document.createElement('button');
  button.type = 'button';
  button.textContent = label;
  button.addEventListener('click', onClick);
  return button;
}

async function queueFileBrowserPath(path) {
  if (!activeAgentID || taskRequestInFlight) return;
  fileBrowserPath = path;
  clearTaskInputError();
  hidePathSuggestions();
  renderFileBrowserLoading(path);

  const targetAgentID = activeAgentID;
  setQueueBusy(true, 'Browsing directory...');
  try {
    const data = await submitTask(targetAgentID, { type: 'ls', payload: path });
    if (!data) renderFileBrowserError('Browse request failed.');
  } catch (err) {
    renderFileBrowserError(err.message);
    appendOutput('[-] browse error: ' + err.message, '', targetAgentID);
  } finally {
    setQueueBusy(false, '');
  }
}

async function queueDownloadFromBrowser(path) {
  if (!activeAgentID || taskRequestInFlight) return;
  const targetAgentID = activeAgentID;
  setQueueBusy(true, 'Queueing download...');
  try {
    const data = await submitTask(targetAgentID, { type: 'download', payload: path });
    if (data) {
      downloadTasks.set(data.task_id, {
        path,
        filename: basenameFromPath(path),
        status: 'progress',
        artifactKey: '',
      });
      if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
      appendOutput('[>] download ' + path + '  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
      refreshActiveAgent();
    }
  } catch (err) {
    appendOutput('[-] download request error: ' + err.message, '', targetAgentID);
  } finally {
    setQueueBusy(false, '');
  }
}

function formatFileSize(size) {
  if (!Number.isFinite(size) || size < 0) return '';
  if (size < 1024) return String(size) + ' B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let value = size / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return value.toFixed(value >= 10 ? 0 : 1) + ' ' + units[unit];
}

function formatBrowserTime(value) {
  if (!value) return '';
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  return date.toLocaleString();
}

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

  if (!$('clear-confirm-modal').hidden) {
    e.preventDefault();
    closeClearConfirmModal();
    return;
  }

  if (!$('kill-confirm-modal').hidden) {
    e.preventDefault();
    closeKillConfirmModal();
    return;
  }

  if (!$('session-details-modal').hidden) {
    e.preventDefault();
    closeSessionDetailsModal();
    $('session-details-btn').focus();
    return;
  }

  if (!$('file-browser-modal').hidden) {
    e.preventDefault();
    closeFileBrowserModal();
    $('send-btn').focus();
    return;
  }

  if (outputSearchExpanded && document.activeElement === $('output-search')) {
    e.preventDefault();
    updateOutputSearchUI(false);
    $('output-search-toggle').focus();
    return;
  }

  if (selectedTaskType === 'upload' && pendingUploadFile) {
    e.preventDefault();
    clearPendingUpload();
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
      saveActiveTaskDraft();
      return;
    }
  }

  const entry = taskHistory[taskHistoryIndex];
  setTaskType(entry.type);
  $('task-input').value = entry.payload;
  saveActiveTaskDraft();
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

$('save-output-btn').addEventListener('click', saveRenderedOutputArtifact);

$('clear-btn').addEventListener('click', openClearConfirmModal);

async function clearOutputHistory(agentID) {
  if (!agentID || taskRequestInFlight) return;

  $('clear-btn').disabled = true;
  $('save-output-btn').disabled = true;
  outputsRequestID++;

  try {
    const resp = await apiFetch('/api/agents/' + agentID + '/tasks', {
      method: 'DELETE',
    });
    if (!resp.ok) {
      const message = await readResponseMessage(resp, 'clear output failed (' + resp.status + ')');
      appendOutput('[-] ' + message, '', agentID);
      return;
    }
    if (agentID !== activeAgentID) return;
    stopSSEStream();
    $('output').textContent = '';
    currentOutputs = [];
    seenTaskIDs = new Set();
    followOutput = true;
    hasHydratedOutputs = true;
    startSSEStream(agentID, interactiveMode);
    renderSessionPanels();
    updateOutputControls();
    updateOutputEmptyState();
  } catch (err) {
    appendOutput('[-] clear output error: ' + err.message, '', agentID);
  } finally {
    if (agentID === activeAgentID && !taskRequestInFlight) {
      $('clear-btn').disabled = false;
      $('save-output-btn').disabled = false;
    }
  }
}

$('exit-interactive-btn').addEventListener('click', () => exitInteractiveMode(true));

async function sendTask() {
  if (!activeAgentID || taskRequestInFlight) return;

  if (interactiveMode) {
    await sendInteractiveCommand();
    return;
  }

  if (selectedTaskType === 'upload') {
    if (!pendingUploadFile) {
      setTaskInputError('Choose a file with Choose File before queueing the upload.');
      return;
    }
    await queueUploadTask();
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
  hidePathSuggestions();

  const targetAgentID = activeAgentID;
  const restoreFocus = shouldPersistCommandFocus() && TASK_TYPES[selectedTaskType].requiresPayload;
  setQueueBusy(true, 'Submitting ' + task.type + ' task...');

  try {
    const data = await submitTask(targetAgentID, task);
    if (!data) return;
    recordTaskHistory(task.type, task.payload);
    if (task.type === 'download') {
      downloadTasks.set(data.task_id, {
        path: task.payload,
        filename: basenameFromPath(task.payload),
        status: 'progress',
        artifactKey: '',
      });
    }
    $('task-input').value = '';
    clearActiveTaskDraft(task.type);
    clearTaskInputError();
    appendOutput('[>] ' + task.type + formatTaskPayloadEcho(task) + '  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
    refreshActiveAgent();
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

async function deleteQueuedTask(taskID) {
  if (!activeAgentID || !taskID) return;
  try {
    const resp = await apiFetch('/api/agents/' + activeAgentID + '/tasks/' + encodeURIComponent(taskID), {
      method: 'DELETE',
    });
    if (!resp.ok) {
      appendOutput('[-] remove queued task failed (' + resp.status + ')');
      return;
    }
    appendOutput('[>] removed queued task ' + taskID.slice(0, 8));
    await refreshActiveAgent();
  } catch (err) {
    appendOutput('[-] remove queued task error: ' + err.message);
  }
}

async function queueCancelTask(taskID) {
  if (!activeAgentID || !taskID || taskRequestInFlight) return;
  const targetAgentID = activeAgentID;
  setQueueBusy(true, 'Queueing cancellation request...');
  try {
    const data = await submitTask(targetAgentID, { type: 'cancel', payload: taskID });
    if (data) {
      appendOutput('[>] cancel ' + taskID.slice(0, 8) + '  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
      await refreshActiveAgent();
    }
  } catch (err) {
    appendOutput('[-] cancel request error: ' + err.message, '', targetAgentID);
  } finally {
    setQueueBusy(false, '');
    updateCancellationControls();
  }
}

async function refreshActiveAgent() {
  if (!activeAgentID) return;
  const resp = await apiFetch('/api/agents/' + activeAgentID);
  if (!resp.ok) return;
  activeAgent = await resp.json();
  allAgents = allAgents.map(agent => agent.id === activeAgentID ? { ...agent, ...activeAgent } : agent);
  renderAgentList();
  renderSessionPanels();
}

async function loadAudit() {
  if (!token) return;
  try {
    const resp = await apiFetch('/api/audit');
    if (!resp.ok) return;
    const data = await resp.json();
    auditLog = Array.isArray(data) ? data : [];
    renderAuditList();
  } catch (_) {
    // Audit is supporting context; ignore transient refresh failures.
  }
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
    case 'ps':
      return { type: 'ps', payload: '' };
    case 'screenshot':
      return { type: 'screenshot', payload: '' };
    case 'snapshot':
      return { type: 'snapshot', payload: '' };
    case 'persistence':
      return { type: 'persistence', payload: '' };
    case 'peas':
      return { type: 'peas', payload: '' };
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
  document.querySelector('.command-line').hidden = false;
  if (taskTypeSelect) taskTypeSelect.hidden = true;
  $('send-btn').hidden = true;
  $('choose-file-btn').hidden = true;
  $('browse-path-btn').hidden = true;
  $('upload-filename-label').hidden = true;
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
  startSSEStream(agentID, true);
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

async function startSSEStream(agentID, reportErrors) {
  stopSSEStream();
  const streamID = ++sseStreamID;

  try {
    const resp = await apiFetch('/api/agents/' + agentID + '/terminal/stream', {
      headers: { Accept: 'text/event-stream' },
    });
    if (streamID !== sseStreamID || agentID !== activeAgentID) return;

    if (!resp.ok || !resp.body) {
      const message = await readResponseMessage(resp, 'interactive stream unavailable');
      if (reportErrors) appendOutput('[!] ' + message, '', agentID);
      return;
    }

    const reader = resp.body.getReader();
    sseReader = reader;
    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (streamID !== sseStreamID || agentID !== activeAgentID) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop();

      for (const line of lines) {
        if (!line.startsWith('data: ')) continue;

        try {
          if (streamID === sseStreamID && agentID === activeAgentID) {
            handleTaskOutput(JSON.parse(line.slice(6)));
          }
        } catch (_) {
          // Ignore malformed SSE fragments and keep the stream alive.
        }
      }
    }
  } catch (err) {
    if (reportErrors && interactiveMode) appendOutput('[!] stream disconnected: ' + err.message, '', agentID);
  } finally {
    if (streamID === sseStreamID) sseReader = null;
  }
}

function stopSSEStream() {
  sseStreamID++;
  if (!sseReader) return;
  sseReader.cancel().catch(() => {});
  sseReader = null;
}

$('choose-file-btn').addEventListener('click', () => {
  if (selectedTaskType !== 'upload' || !activeAgentID || taskRequestInFlight) return;
  $('upload-file-input').click();
});

$('upload-file-input').addEventListener('change', () => {
  const file = $('upload-file-input').files[0];
  if (file) acceptUploadFile(file);
  $('upload-file-input').value = '';
});

$('browse-path-btn').addEventListener('click', () => {
  if ((selectedTaskType !== 'upload' && selectedTaskType !== 'download') || !activeAgentID || taskRequestInFlight) return;
  if (selectedTaskType === 'upload' && !pendingUploadFile) {
    setTaskInputError('Choose a local file before browsing for an upload destination.');
    return;
  }
  if (!activePathBrowseReady()) {
    setTaskInputError('The remote path browser is still preparing. Try again when the session confirms readiness.');
    return;
  }
  openFileBrowserModal(selectedTaskType === 'upload' ? 'select-upload' : 'browse');
});

const outputEl = $('output');

$('jump-latest-btn').addEventListener('click', () => {
  scrollOutputToBottom();
});

if (outputResizerEl) {
  let resizeStartY = 0;
  let resizeStartHeight = 0;

  outputResizerEl.addEventListener('pointerdown', event => {
    if (!activeAgentID) return;
    event.preventDefault();
    resizeStartY = event.clientY;
    resizeStartHeight = outputShellEl.getBoundingClientRect().height;
    outputResizerEl.classList.add('dragging');
    outputResizerEl.setPointerCapture(event.pointerId);
  });

  outputResizerEl.addEventListener('pointermove', event => {
    if (!outputResizerEl.classList.contains('dragging')) return;
    const nextHeight = resizeStartHeight + (event.clientY - resizeStartY);
    setOutputPaneHeight(nextHeight);
  });

  outputResizerEl.addEventListener('pointerup', event => {
    outputResizerEl.classList.remove('dragging');
    try {
      outputResizerEl.releasePointerCapture(event.pointerId);
    } catch (_) {
      // Pointer capture may already be released by the browser.
    }
  });

  outputResizerEl.addEventListener('pointercancel', () => {
    outputResizerEl.classList.remove('dragging');
  });

  outputResizerEl.addEventListener('dblclick', () => {
    outputShellEl.style.flex = '';
    outputShellEl.style.height = '';
  });

  window.addEventListener('resize', () => {
    if (!outputShellEl.style.height) return;
    setOutputPaneHeight(outputShellEl.getBoundingClientRect().height);
  });
}

function setOutputPaneHeight(height) {
  const inputArea = $('input-area');
  const primaryEl = $('session-primary') || $('console');
  const minHeight = 180;
  const maxHeight = Math.max(
    minHeight,
    primaryEl.clientHeight - inputArea.offsetHeight - 36,
  );
  const nextHeight = Math.min(maxHeight, Math.max(minHeight, height));
  outputShellEl.style.flex = '0 0 auto';
  outputShellEl.style.height = nextHeight + 'px';
  updateOutputControls();
}

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
  if (file) acceptUploadFile(file);
});

function acceptUploadFile(file) {
  if (interactiveMode || !activeAgentID) return;

  if (file.size > MAX_UPLOAD_BYTES) {
    appendOutput('[-] file too large: ' + file.name + ' (' + formatFileSize(file.size) + '). Max is ' + formatFileSize(MAX_UPLOAD_BYTES) + '.');
    return;
  }

  pendingUploadFile = file;
  if (selectedTaskType !== 'upload') {
    setTaskType('upload');
  } else {
    applyTaskTypeUI();
    focusPrimaryInput(false);
  }
}

function clearPendingUpload() {
  pendingUploadFile = null;
  if (!interactiveMode) updateUploadFilenameLabel();
  if (!interactiveMode && selectedTaskType === 'upload') applyTaskTypeUI();
}

async function queueUploadTask() {
  if (!pendingUploadFile || !activeAgentID || taskRequestInFlight) return;

  const remotePath = $('task-input').value.trim();
  if (!remotePath) {
    setTaskInputError('Enter a remote destination path before sending the upload.');
    return;
  }

  if (hasInvalidPathChars(remotePath)) {
    setTaskInputError('Upload paths cannot contain line breaks or null bytes.');
    return;
  }
  if (activeAgent && activeAgent.transport === 'dns' && pendingUploadFile.size > 6 * 1024) {
    setTaskInputError('This session last checked in over DNS. Large uploads require HTTPS transport.');
    appendOutput('[-] upload requires HTTPS transport for files larger than a few KB; reconnect the session over HTTPS and try again.', '', activeAgentID);
    return;
  }

  const file = pendingUploadFile;
  const targetAgentID = activeAgentID;
  hidePathSuggestions();
  setQueueBusy(true, 'Submitting upload...');

  const reader = new FileReader();
  reader.onload = async e => {
    const dataURL = String(e.target.result || '');
    const comma = dataURL.indexOf(',');
    if (comma < 0) {
      appendOutput('[-] upload read error: unable to encode local file', '', targetAgentID);
      setQueueBusy(false, '');
      return;
    }
    const payload = remotePath + ':' + dataURL.slice(comma + 1);

    try {
      const data = await submitTask(targetAgentID, { type: 'upload', payload });
      if (!data) return;
      appendOutput('[>] upload ' + file.name + ' -> ' + remotePath + '  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
      pendingUploadFile = null;
      $('task-input').value = '';
      clearActiveTaskDraft('upload');
      updateUploadFilenameLabel();
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

  reader.readAsDataURL(file);
}

function basenameFromPath(path) {
  const value = String(path || '').replace(/[\\/]+$/, '');
  const idx = Math.max(value.lastIndexOf('/'), value.lastIndexOf('\\'));
  return sanitizeArchiveEntryName(idx >= 0 ? value.slice(idx + 1) : value) || 'download.bin';
}

function hasInvalidPathChars(value) {
  return !value || value.length > MAX_REMOTE_PATH || /[\u0000\r\n]/.test(value);
}

function saveRenderedOutputArtifact() {
  if (!activeAgentID) return;

  const text = renderedOutputText();
  if (!text.trim()) {
    appendOutput('[-] no output to save', '', activeAgentID);
    return;
  }

  const now = new Date();
  const stamp = timestampForFilename(now);
  const host = activeAgent && activeAgent.hostname ? activeAgent.hostname : activeAgentID.slice(0, 8);
  const filename = 'session-output-' + sanitizeArchiveEntryName(host) + '-' + stamp + '.txt';
  const artifact = rememberArtifact({
    short: activeAgentID.slice(0, 8),
    ts: now.toLocaleTimeString(),
    base64Value: textToBase64(text.endsWith('\n') ? text : text + '\n'),
    filename,
    archiveFilename: filename,
    mime: 'text/plain;charset=utf-8',
    label: 'saved output',
  });
  renderSessionPanels();
  if (artifact) openArtifactsPanel(artifact.key);
}

function renderedOutputText() {
  return Array.from($('output').children)
    .map(child => {
      if (child.classList.contains('output-download')) {
        const label = child.querySelector('span');
        return label ? label.textContent : child.textContent;
      }
      return child.textContent;
    })
    .map(text => String(text || '').trimEnd())
    .filter(Boolean)
    .join('\n\n');
}

function timestampForFilename(date) {
  const pad = value => String(value).padStart(2, '0');
  return [
    date.getFullYear(),
    pad(date.getMonth() + 1),
    pad(date.getDate()),
  ].join('') + '-' + [
    pad(date.getHours()),
    pad(date.getMinutes()),
    pad(date.getSeconds()),
  ].join('');
}

function textToBase64(text) {
  return bytesToBase64(new TextEncoder().encode(text));
}

function bytesToBase64(bytes) {
  let binary = '';
  const chunkSize = 0x8000;
  for (let i = 0; i < bytes.length; i += chunkSize) {
    const chunk = bytes.subarray(i, i + chunkSize);
    binary += String.fromCharCode.apply(null, chunk);
  }
  return btoa(binary);
}

function appendOutput(text, cssClass, targetAgentID) {
  if (targetAgentID && targetAgentID !== activeAgentID) return;

  const shouldScroll = followOutput || isOutputNearBottom();
  const line = document.createElement('div');
  if (cssClass) line.classList.add(cssClass);
  line.textContent = text;
  line.dataset.searchText = text.toLowerCase();
  $('output').appendChild(line);
  applyOutputSearch();
  if (shouldScroll) scrollOutputToBottom();
  else updateOutputControls();
  updateOutputEmptyState();
}

function appendDownloadResult(taskID, short, ts, base64Value, historical) {
  const state = downloadTasks.get(taskID);
  const sourceName = state && state.filename ? state.filename : 'download.bin';
  const artifact = appendFileResult({
    short,
    ts,
    base64Value,
    filename: sourceName,
    archiveFilename: 'download-' + short + '.zip',
    mime: 'application/zip',
    buttonLabel: 'Save Compressed',
    label: 'download ready',
    historical,
    compress: true,
  });
  if (state) {
    state.status = 'done';
    state.artifactKey = artifact ? artifact.key : '';
    if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
  }
}

function appendArtifactResult(short, ts, payload, label, buttonLabel, historical) {
  let result;
  try {
    result = JSON.parse(payload);
  } catch (_) {
    appendOutput('[err ' + short + ' ' + ts + '] invalid artifact payload');
    return;
  }

  if (!result || !result.data || !result.filename) {
    appendOutput('[err ' + short + ' ' + ts + '] invalid artifact payload');
    return;
  }

  appendFileResult({
    short,
    ts,
    base64Value: result.data,
    filename: result.filename,
    mime: result.mime || 'application/octet-stream',
    buttonLabel,
    label,
    historical,
  });
}

function appendFileResult(options) {
  const label = '[' + options.short + ' ' + options.ts + '] ' + options.label;
  const shouldScroll = followOutput || isOutputNearBottom();
  const wrap = document.createElement('div');
  wrap.className = 'output-download';
  wrap.dataset.searchText = (options.label + ' ' + options.filename).toLowerCase();

  const text = document.createElement('span');
  text.textContent = options.historical
    ? label + ' from session history. Click to save it locally.'
    : label + '. Click to save it locally.';

  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'output-download-btn';
  button.textContent = options.buttonLabel;
  button.addEventListener('click', async () => {
    try {
      await saveArtifact(options);
    } catch (err) {
      appendOutput('[err ' + options.short + ' ' + options.ts + '] invalid file payload');
    }
  });

  wrap.appendChild(text);
  wrap.appendChild(button);
  $('output').appendChild(wrap);
  const artifact = rememberArtifact(options);
  applyOutputSearch();
  if (shouldScroll) scrollOutputToBottom();
  else updateOutputControls();
  updateOutputEmptyState();
  return artifact;
}

function rememberArtifact(options) {
  if (!options || !options.filename || !options.base64Value) return;
  const key = options.short + ':' + (options.archiveFilename || options.filename);
  const existing = artifactLibrary.find(item => item.key === key);
  if (existing) return existing;
  const artifact = {
    key,
    taskID: options.short,
    label: options.label,
    filename: options.filename,
    archiveFilename: options.archiveFilename || options.filename,
    mime: options.mime || 'application/octet-stream',
    base64Value: options.base64Value,
    compress: Boolean(options.compress),
    timestamp: options.ts,
  };
  artifactLibrary.unshift(artifact);
  if (artifactLibrary.length > 64) artifactLibrary.pop();
  renderArtifactList();
  return artifact;
}

function renderSessionPanels() {
  updateSessionPanelTabs();
  updateSessionPanelCounts();
  updateCancellationControls();
  renderJobsList();
  renderArtifactList();
  renderAuditList();
}

function updateCancellationControls() {
  const bar = $('cancel-task-bar');
  if (!bar) return;

  const tasks = activeAgentID ? cancellableTasks() : [];
  bar.hidden = tasks.length === 0;
  if (!tasks.length) {
    $('cancel-task-select').textContent = '';
    return;
  }

  const select = $('cancel-task-select');
  const previous = select.value;
  select.textContent = '';
  tasks.forEach(task => {
    const option = document.createElement('option');
    option.value = task.id;
    option.textContent = cancelTaskLabel(task);
    select.appendChild(option);
  });
  if (tasks.some(task => task.id === previous)) select.value = previous;

  const selected = tasks.find(task => task.id === select.value) || tasks[0];
  $('cancel-task-title').textContent = tasks.length === 1 ? selected.label + ' running' : tasks.length + ' cancellable tasks running';
  $('cancel-task-text').textContent = tasks.length === 1
    ? 'Task ' + selected.id.slice(0, 8) + ' can be cancelled from here.'
    : 'Choose the running task to cancel.';
  select.hidden = tasks.length === 1;
  select.disabled = taskRequestInFlight;
  $('cancel-task-btn').textContent = tasks.length === 1 ? 'Cancel ' + selected.label : 'Cancel Selected';
  $('cancel-task-btn').disabled = taskRequestInFlight;
}

function cancellableTasks() {
  return runningPEASJobs();
}

function cancelTaskLabel(task) {
  return task.label + ' ' + task.id.slice(0, 8);
}

function openSessionDetailsModal() {
  if (!activeAgentID) return;
  renderSessionPanels();
  $('session-details-modal').hidden = false;
  window.requestAnimationFrame(() => $('session-details-close-btn').focus());
}

function closeSessionDetailsModal() {
  const modal = $('session-details-modal');
  if (!modal) return;
  modal.hidden = true;
}

function setSessionPanel(panel) {
  if (panel !== 'all' && (!panel || !$(panel + '-panel'))) return;
  activeSessionPanel = panel;
  updateSessionPanelTabs();
}

function updateSessionPanelTabs() {
  const showAll = activeSessionPanel === 'all';

  sessionPanelTabs.forEach(button => {
    const isActive = button.dataset.panel === activeSessionPanel;
    button.classList.toggle('active', isActive);
    button.setAttribute('aria-selected', isActive ? 'true' : 'false');
  });
  if (sessionPanelFilter) sessionPanelFilter.value = activeSessionPanel;

  const panels = $('session-panels');
  if (panels) panels.classList.toggle('show-all', showAll);

  ['jobs', 'artifacts', 'notes', 'audit'].forEach(panel => {
    const el = $(panel + '-panel');
    if (!el) return;
    const isActive = showAll || panel === activeSessionPanel;
    el.hidden = !isActive;
    el.classList.toggle('active', isActive);
  });
}

function updateSessionPanelCounts() {
  const queued = activeAgent && Array.isArray(activeAgent.queued) ? activeAgent.queued.length : 0;
  const running = runningPEASJobs().length;
  const recent = currentOutputs.length;
  $('jobs-count').textContent = String(queued + running + recent);
  $('artifacts-count').textContent = String(artifactLibrary.length);
  $('audit-count').textContent = String(auditLog.length);
}

function renderJobsList() {
  const list = $('jobs-list');
  if (!list) return;
  list.textContent = '';
  if (!activeAgentID) return;

  const queued = activeAgent && Array.isArray(activeAgent.queued) ? activeAgent.queued : [];
  const running = runningPEASJobs();
  if (!queued.length && !running.length && !currentOutputs.length) {
    list.appendChild(panelText('No jobs for this session.'));
    return;
  }

  running.forEach(job => {
    const row = panelItem('RUNNING', job.id.slice(0, 8) + ' PEAS');
    row.appendChild(panelHint('Cancel from Task Builder.'));
    list.appendChild(row);
  });

  queued.forEach(job => {
    const row = panelItem('QUEUED', job.id.slice(0, 8) + ' ' + job.type);
    const button = panelButton('Remove', () => deleteQueuedTask(job.id));
    row.appendChild(button);
    list.appendChild(row);
  });

  currentOutputs.slice(-8).reverse().forEach(output => {
    if (!output || !output.task_id) return;
    const status = output.error ? 'FAILED' : output.type === 'peas_progress' ? 'PROGRESS' : 'DONE';
    list.appendChild(panelItem(status, output.task_id.slice(0, 8) + ' ' + output.type));
  });
}

function runningPEASJobs() {
  const completed = new Set();
  const running = new Map();
  currentOutputs.forEach(output => {
    if (!output || !output.task_id) return;
    if (output.type === 'peas') {
      completed.add(output.task_id);
      running.delete(output.task_id);
      return;
    }
    if (output.type !== 'peas_progress') return;
    const idx = output.task_id.indexOf('-peas-');
    const id = idx > 0 ? output.task_id.slice(0, idx) : output.task_id;
    if (!completed.has(id)) running.set(id, { id, label: 'PEAS' });
  });
  return Array.from(running.values());
}

function renderArtifactList() {
  const list = $('artifact-list');
  if (!list) return;
  list.textContent = '';
  if (!artifactLibrary.length) {
    list.appendChild(panelText('No artifacts captured in this view.'));
    return;
  }
  artifactLibrary.forEach(item => {
    const row = panelItem(item.label || 'artifact', item.filename);
    row.dataset.artifactKey = item.key || '';
    row.appendChild(panelHint(item.archiveFilename || item.filename));
    row.appendChild(panelButton('Save', () => saveArtifact(item)));
    list.appendChild(row);
  });
}

function renderAuditList() {
  const list = $('audit-list');
  if (!list) return;
  list.textContent = '';
  const rows = auditLog.slice(-20).reverse();
  if (!rows.length) {
    list.appendChild(panelText('No audit events loaded.'));
    return;
  }
  rows.forEach(event => {
    const time = event.timestamp ? new Date(event.timestamp).toLocaleTimeString() : '';
    list.appendChild(panelItem(time + ' ' + event.action, event.detail || event.agent_id || ''));
  });
}

function panelText(text) {
  const item = document.createElement('div');
  item.className = 'panel-item';
  item.textContent = text;
  return item;
}

function panelItem(label, text) {
  const item = document.createElement('div');
  item.className = 'panel-item';
  const content = document.createElement('span');
  const strong = document.createElement('strong');
  strong.textContent = label;
  content.appendChild(strong);
  content.appendChild(document.createTextNode(text ? ' ' + text : ''));
  item.appendChild(content);
  return item;
}

function panelButton(label, onClick) {
  const button = document.createElement('button');
  button.type = 'button';
  button.textContent = label;
  button.addEventListener('click', onClick);
  return button;
}

function panelHint(text) {
  const hint = document.createElement('span');
  hint.className = 'panel-hint';
  hint.textContent = text;
  return hint;
}

async function saveArtifact(item) {
  if (item && item.compress) {
    await triggerCompressedDownload(item.base64Value, item.filename, item.archiveFilename || item.filename + '.zip');
    return;
  }
  triggerDownload(item.base64Value, item.archiveFilename || item.filename, item.mime);
}

async function triggerCompressedDownload(base64Value, innerFilename, archiveFilename) {
  const fileBytes = base64ToBytes(base64Value);
  const zipBytes = await createZipArchive(innerFilename, fileBytes);
  const blob = new Blob([zipBytes], { type: 'application/zip' });
  triggerBlobDownload(blob, archiveFilename);
}

function triggerDownload(base64Value, filename, mime) {
  const bytes = base64ToBytes(base64Value);
  const blob = new Blob([bytes], mime ? { type: mime } : undefined);
  triggerBlobDownload(blob, filename);
}

function triggerBlobDownload(blob, filename) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = sanitizeDownloadName(filename);
  document.body.appendChild(anchor);
  anchor.click();
  document.body.removeChild(anchor);
  URL.revokeObjectURL(url);
}

function base64ToBytes(base64Value) {
  const binary = atob(base64Value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

async function createZipArchive(filename, fileBytes) {
  const nameBytes = new TextEncoder().encode(sanitizeArchiveEntryName(filename));
  const crc = crc32(fileBytes);
  const compressed = await deflateRaw(fileBytes);
  const useDeflate = compressed && compressed.length < fileBytes.length;
  const body = useDeflate ? compressed : fileBytes;
  const method = useDeflate ? 8 : 0;
  const now = new Date();
  const dosTime = ((now.getHours() & 31) << 11) | ((now.getMinutes() & 63) << 5) | ((Math.floor(now.getSeconds() / 2)) & 31);
  const dosDate = (((now.getFullYear() - 1980) & 127) << 9) | (((now.getMonth() + 1) & 15) << 5) | (now.getDate() & 31);

  const local = new Uint8Array(30 + nameBytes.length);
  const view = new DataView(local.buffer);
  writeZipHeader(view, 0x04034b50, 0);
  view.setUint16(4, 20, true);
  view.setUint16(8, method, true);
  view.setUint16(10, dosTime, true);
  view.setUint16(12, dosDate, true);
  view.setUint32(14, crc, true);
  view.setUint32(18, body.length, true);
  view.setUint32(22, fileBytes.length, true);
  view.setUint16(26, nameBytes.length, true);
  local.set(nameBytes, 30);

  const central = new Uint8Array(46 + nameBytes.length);
  const centralView = new DataView(central.buffer);
  writeZipHeader(centralView, 0x02014b50, 0);
  centralView.setUint16(4, 20, true);
  centralView.setUint16(6, 20, true);
  centralView.setUint16(10, method, true);
  centralView.setUint16(12, dosTime, true);
  centralView.setUint16(14, dosDate, true);
  centralView.setUint32(16, crc, true);
  centralView.setUint32(20, body.length, true);
  centralView.setUint32(24, fileBytes.length, true);
  centralView.setUint16(28, nameBytes.length, true);
  central.set(nameBytes, 46);

  const eocd = new Uint8Array(22);
  const eocdView = new DataView(eocd.buffer);
  writeZipHeader(eocdView, 0x06054b50, 0);
  eocdView.setUint16(8, 1, true);
  eocdView.setUint16(10, 1, true);
  eocdView.setUint32(12, central.length, true);
  eocdView.setUint32(16, local.length + body.length, true);

  const out = new Uint8Array(local.length + body.length + central.length + eocd.length);
  out.set(local, 0);
  out.set(body, local.length);
  out.set(central, local.length + body.length);
  out.set(eocd, local.length + body.length + central.length);
  return out;
}

function writeZipHeader(view, signature, offset) {
  view.setUint32(offset, signature, true);
}

async function deflateRaw(bytes) {
  if (typeof CompressionStream !== 'function') return null;
  try {
    const stream = new Blob([bytes]).stream().pipeThrough(new CompressionStream('deflate-raw'));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  } catch (_) {
    return null;
  }
}

function sanitizeArchiveEntryName(filename) {
  return String(filename || 'download.bin')
    .replace(/^[a-zA-Z]:/, '')
    .replace(/[\\/]+/g, '_')
    .replace(/[\u0000-\u001f<>:"|?*]/g, '_')
    .replace(/^\.+$/, 'download.bin')
    .slice(0, 180) || 'download.bin';
}

function sanitizeDownloadName(filename) {
  return sanitizeArchiveEntryName(filename)
    .replace(/^\.+/, '')
    .trim()
    .slice(0, 180) || 'download.bin';
}

let crcTable = null;
function crc32(bytes) {
  if (!crcTable) {
    crcTable = new Uint32Array(256);
    for (let i = 0; i < 256; i++) {
      let c = i;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      crcTable[i] = c >>> 0;
    }
  }
  let c = 0xffffffff;
  for (let i = 0; i < bytes.length; i++) c = crcTable[(c ^ bytes[i]) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function baseDownloadTaskID(taskID) {
  const idx = String(taskID || '').indexOf('-download-');
  return idx > 0 ? taskID.slice(0, idx) : taskID;
}

applyTaskTypeUI();
updateOutputControls();
updateOutputEmptyState();
