'use strict';

function beginSession(nextToken) {
  token = nextToken;
  selectedTaskType = 'shell';
  bulkSelectionMode = false;
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
  selectedAgentIDs = new Set();
  bulkSelectionMode = false;
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
  activeSessionPanel = 'timeline';
  fileBrowserPath = '';
  fileBrowserResult = null;
  outputSearchExpanded = false;
  setOutputTypeFilter('all', true);
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
  $('timeline-summary').textContent = '';
  $('timeline-list').textContent = '';
  $('jobs-list').textContent = '';
  $('artifact-list').textContent = '';
  $('audit-list').textContent = '';
  $('tag-input').value = '';
  $('notes-input').value = '';
  $('output-search').value = '';
  updateOutputSearchUI(false);
  updateBulkSelectionUI();
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

async function loadAgents() {
  try {
    const resp = await apiFetch('/api/agents');
    if (!resp.ok) return;

    const agents = await resp.json();
    allAgents = Array.isArray(agents) ? agents.slice() : [];
    pruneSelectedAgents();

    updateAgentStats(allAgents);
    updateRefreshMeta(allAgents.length);
    warmOnlinePathBrowsers(allAgents);
    syncActiveAgent();
    renderAgentList();
    updateBulkSelectionUI();
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

function pruneSelectedAgents() {
  if (!selectedAgentIDs.size) return;
  const available = new Set(allAgents.map(agent => agent.id));
  selectedAgentIDs = new Set(Array.from(selectedAgentIDs).filter(id => available.has(id)));
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
  activeSessionPanel = 'timeline';
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
  const isSelected = selectedAgentIDs.has(agent.id);

  const li = document.createElement('li');
  li.className = 'agent-item';
  if (agent.id === activeAgentID) li.classList.add('active');

  const card = document.createElement('article');
  card.className = 'agent-card';
  card.classList.toggle('selected', isSelected);
  card.classList.toggle('selection-mode', bulkSelectionMode);
  card.title = agent.id;
  card.tabIndex = taskRequestInFlight ? -1 : 0;
  card.setAttribute('role', 'button');
  card.setAttribute('aria-disabled', taskRequestInFlight ? 'true' : 'false');
  if (bulkSelectionMode) card.setAttribute('aria-pressed', isSelected ? 'true' : 'false');
  card.addEventListener('click', () => {
    if (taskRequestInFlight) return;
    if (bulkSelectionMode) toggleBulkSession(agent.id, !selectedAgentIDs.has(agent.id));
    else selectAgent(agent);
  });
  card.addEventListener('keydown', e => {
    if (taskRequestInFlight || (e.key !== 'Enter' && e.key !== ' ')) return;
    e.preventDefault();
    if (bulkSelectionMode) toggleBulkSession(agent.id, !selectedAgentIDs.has(agent.id));
    else selectAgent(agent);
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

  if (bulkSelectionMode) {
    const selectLabel = document.createElement('label');
    selectLabel.className = 'agent-select-control';
    const selectBox = document.createElement('input');
    selectBox.type = 'checkbox';
    selectBox.checked = isSelected;
    selectBox.disabled = taskRequestInFlight;
    selectBox.setAttribute('aria-label', 'Select ' + (agent.hostname || agent.id.slice(0, 8)) + ' for bulk task queueing');
    selectBox.addEventListener('click', e => {
      e.stopPropagation();
    });
    selectBox.addEventListener('change', e => {
      toggleBulkSession(agent.id, e.target.checked);
    });
    selectLabel.addEventListener('click', e => {
      e.stopPropagation();
    });
    selectLabel.appendChild(selectBox);
    selectLabel.appendChild(document.createTextNode('Select'));
    actions.appendChild(selectLabel);
  }

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

function setBulkSelectionMode(enabled) {
  if (taskRequestInFlight && enabled) return;
  bulkSelectionMode = Boolean(enabled);
  renderAgentList();
  updateBulkSelectionUI();
}

function toggleBulkSession(agentID, selected) {
  if (!agentID || taskRequestInFlight) return;
  if (selected) selectedAgentIDs.add(agentID);
  else selectedAgentIDs.delete(agentID);
  renderAgentList();
  updateBulkSelectionUI();
}

function selectedAgents() {
  const selected = new Set(selectedAgentIDs);
  return allAgents.filter(agent => selected.has(agent.id));
}

function updateBulkSelectionUI() {
  const bar = $('bulk-session-bar');
  const count = $('bulk-session-count');
  const clearButton = $('bulk-clear-btn');
  const modeButton = $('bulk-select-mode-btn');
  if (!bar || !count) return;

  const total = selectedAgentIDs.size;
  bar.hidden = !bulkSelectionMode && total === 0;
  count.textContent = total === 1 ? '1 selected' : total + ' selected';
  if (clearButton) clearButton.disabled = taskRequestInFlight || total === 0;
  if (modeButton) {
    modeButton.textContent = bulkSelectionMode ? 'Done' : 'Select';
    modeButton.classList.toggle('active', bulkSelectionMode);
    modeButton.disabled = taskRequestInFlight;
    modeButton.setAttribute('aria-pressed', bulkSelectionMode ? 'true' : 'false');
    modeButton.title = bulkSelectionMode
      ? 'Finish selecting sessions'
      : 'Enable multi-select for bulk task queueing';
  }
  updateBulkTaskButton();
}

function updateBulkTaskButton() {
  const button = $('bulk-send-btn');
  if (!button) return;

  const total = selectedAgentIDs.size;
  const bulkAllowed = BULK_TASK_TYPES.has(selectedTaskType);
  button.hidden = interactiveMode || total === 0;
  button.textContent = total <= 1 ? 'Queue Selected' : 'Queue ' + total + ' Sessions';
  button.disabled = taskRequestInFlight || total === 0 || !bulkAllowed;
  button.title = bulkAllowed
    ? 'Queue this task for every selected session'
    : 'This action can only be queued for one session at a time';
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
  artifactLibrary = [];
  clearKillConfirmation();
  hasHydratedOutputs = false;
  followOutput = true;
  $('output').textContent = '';
  $('output-search').value = '';
  setOutputTypeFilter('all', true);
  updateOutputSearchUI(false);
  clearPendingUpload();
  stopSSEStream();
  renderAgentList();
  updateSessionHeader();
  updateTaskContextStatus();
  updateOutputControls();
  updateOutputEmptyState();
  startSSEStream(agent.id, false);
  loadArtifacts(agent.id);
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

async function refreshActiveAgent() {
  if (!activeAgentID) return;
  const resp = await apiFetch('/api/agents/' + activeAgentID);
  if (!resp.ok) return;
  activeAgent = await resp.json();
  allAgents = allAgents.map(agent => agent.id === activeAgentID ? { ...agent, ...activeAgent } : agent);
  renderAgentList();
  renderSessionPanels();
}
