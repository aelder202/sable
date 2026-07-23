'use strict';

function beginSession(nextToken) {
  token = nextToken;
  selectedTaskType = 'shell';
  bulkSelectionMode = false;
  taskTargetMode = 'current';
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
    handlePrimaryRoute(true);
    const target = activePrimaryView === 'overview' ? $('overview-filter') : $('agent-filter');
    if (target) target.focus();
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
  fleetOverview = null;
  ignoredFailureAlertIDs = new Set();
  failureAlertActionIDs = new Set();
  agentsRequestInFlight = false;
  selectedAgentIDs = new Set();
  bulkSelectionMode = false;
  taskTargetMode = 'current';
  seenTaskIDs = new Set();
  currentOutputs = [];
  outputsRequestID++;
  pendingUploadFile = null;
  taskHistory = [];
  taskHistoryIndex = -1;
  taskMetadataByID = new Map();
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
  fileBrowserStates = new Map();
  deferredFileBrowserOutputs = new Map();
  fileBrowserSubmissionCount = 0;
  downloadTasks = new Map();
  metadataDirty = false;
  metadataDraftAgentID = '';
  metadataEditAgentID = '';
  outputSearchExpanded = true;
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
  $('meta-state').hidden = true;
  $('console-title').textContent = 'Select an agent';
  closeSessionDetailsModal();
  closeActiveJobsModal();
  closeFileBrowserModal();
  closeClearConfirmModal();
  closeKillConfirmModal();
  closeActionConfirm(false);
  $('output-resizer').hidden = true;
  $('session-count').textContent = '0 agents';
  $('active-job-count').textContent = '0';
  $('active-transfer-count').textContent = '0';
  $('active-jobs-btn').classList.remove('has-activity');
  $('active-transfers-btn').classList.remove('has-activity');
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
  $('details-transfer-list').textContent = '';
  $('tag-input').value = '';
  $('display-name-input').value = '';
  $('notes-input').value = '';
  $('metadata-save-status').textContent = '';
  $('output-search').value = '';
  updateOutputSearchUI(true);
  updateBulkSelectionUI();
  clearPendingUpload();
  closeClearConfirmModal();
  closeKillConfirmModal();
  setQueueBusy(false, '');
  resetDashboard();
  setPrimaryView('overview', false);
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
  if (agentsRequestInFlight || !token) return;
  agentsRequestInFlight = true;
  try {
    const resp = await apiFetch('/api/overview');
    if (!resp.ok) throw new Error('request failed (' + resp.status + ')');
    const overview = await resp.json();
    fleetOverview = overview && typeof overview === 'object' ? overview : null;
    allAgents = fleetOverview && Array.isArray(fleetOverview.agents) ? fleetOverview.agents.slice() : [];
    pruneSelectedAgents();

    updateAgentStats(allAgents);
    updateRefreshMeta(allAgents.length);
    syncActiveAgent();
    renderAgentList();
    renderDashboard();
    updateBulkSelectionUI();
    renderSessionPanels();
    resolvePendingAgentRoute();
  } catch (_) {
    $('refresh-indicator').textContent = 'Refresh failed';
    $('overview-updated').textContent = 'Refresh failed';
  } finally {
    agentsRequestInFlight = false;
  }

  await loadOutputs();
}

function getAgentAgeMs(agent) {
  if (!agent || !agent.last_seen) return Number.POSITIVE_INFINITY;
  const ts = new Date(agent.last_seen).getTime();
  if (!Number.isFinite(ts) || ts <= 0) return Number.POSITIVE_INFINITY;
  return Math.max(0, Date.now() - ts);
}

function getAgentState(agent) {
  const reported = agent && String(agent.status || '');
  if (['on_schedule', 'overdue', 'offline', 'never_seen', 'retired'].includes(reported)) return reported;
  const ageMs = getAgentAgeMs(agent);
  if (ageMs <= 3 * 60 * 1000) return 'on_schedule';
  if (ageMs <= 10 * 60 * 1000) return 'overdue';
  return 'offline';
}

function getAgentStateLabel(state) {
  if (state === 'on_schedule') return 'Active';
  if (state === 'overdue') return 'Overdue';
  if (state === 'never_seen') return 'Never seen';
  if (state === 'retired') return 'Retired';
  return 'Offline';
}

function getAgentStateDescription(state) {
  if (state === 'on_schedule') return 'Active and checking in within the expected sleep and jitter window.';
  if (state === 'overdue') return 'Late beyond the expected check-in window.';
  if (state === 'never_seen') return 'Registered, but has not completed its first check-in.';
  if (state === 'retired') return 'Hidden from active fleet views while history and artifacts are retained.';
  return 'No check-in within the offline threshold.';
}

function updateAgentStats(agents) {
  let online = 0;
  let stale = 0;
  let offline = 0;

  for (const agent of agents) {
    const state = getAgentState(agent);
    if (state === 'on_schedule') online++;
    else if (state === 'overdue') stale++;
    else if (state === 'offline') offline++;
  }

  $('count-online').textContent = String(online);
  $('count-stale').textContent = String(stale);
  $('count-offline').textContent = String(offline);
}

function updateRefreshMeta(agentCount) {
  const timeLabel = new Date().toLocaleTimeString();
  $('refresh-indicator').textContent = 'Updated ' + timeLabel;
  $('overview-updated').textContent = 'Updated ' + timeLabel;
  $('session-count').textContent = agentCount === 1 ? '1 agent' : agentCount + ' agents';
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

  activeAgent = { ...(activeAgent || {}), ...match };
  updateSessionHeader();
}

function pruneSelectedAgents() {
  if (!selectedAgentIDs.size) return;
  const available = new Set(allAgents.filter(agent => getAgentState(agent) !== 'retired').map(agent => agent.id));
  selectedAgentIDs = new Set(Array.from(selectedAgentIDs).filter(id => available.has(id)));
}

function clearActiveSession() {
  saveActiveTaskDraft();
  persistActiveFileBrowserState();
  if (activeAgentID) resetPathBrowserState(activeAgentID, true);
  exitInteractiveMode(false);
  resetActivePathCompletion();
  stopSSEStream();
  activeAgentID = null;
  activeAgent = null;
  metadataDirty = false;
  metadataDraftAgentID = '';
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
  $('meta-state').hidden = true;
  $('console-title').textContent = 'Select an agent';
  $('display-name-input').value = '';
  $('tag-input').value = '';
  $('notes-input').value = '';
  $('metadata-save-status').textContent = '';
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
  const statusFilter = $('agent-status-filter').value || 'all';
  const filtered = allAgents
    .filter(agent => matchesAgentFilter(agent, query))
    .filter(agent => statusFilter === 'all' ? getAgentState(agent) !== 'retired' : getAgentState(agent) === statusFilter)
    .sort(compareAgents);

  if (!filtered.length) {
    const empty = $('agent-filter-empty');
    empty.hidden = false;
    empty.textContent = allAgents.length
      ? 'No agents match the current filter.'
      : 'No agents have registered yet.';
    return;
  }

  $('agent-filter-empty').hidden = true;

  for (const agent of filtered) {
    list.appendChild(buildAgentItem(agent));
  }
}

function initAgentSidebar() {
  const button = $('sidebar-collapse-btn');
  if (!button) return;
  let collapsed = false;
  try {
    collapsed = window.localStorage.getItem('sable-agent-sidebar-collapsed') === 'true';
  } catch (_) {
    collapsed = false;
  }
  setAgentSidebarCollapsed(collapsed, false);
  button.addEventListener('click', () => {
    setAgentSidebarCollapsed(!$('content').classList.contains('sidebar-collapsed'), true);
  });
}

function setAgentSidebarCollapsed(collapsed, persist) {
  const content = $('content');
  const button = $('sidebar-collapse-btn');
  if (!content || !button) return;
  content.classList.toggle('sidebar-collapsed', collapsed);
  button.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
  button.setAttribute('aria-label', collapsed ? 'Expand agents sidebar' : 'Collapse agents sidebar');
  button.title = collapsed ? 'Expand agents sidebar' : 'Collapse agents sidebar';
  button.querySelector('span').textContent = collapsed ? '»' : '«';
  if (persist) {
    try {
      window.localStorage.setItem('sable-agent-sidebar-collapsed', collapsed ? 'true' : 'false');
    } catch (_) {
      // Local storage may be unavailable in hardened browser contexts.
    }
  }
}

function matchesAgentFilter(agent, query) {
  if (!query) return true;
  const haystack = [
    agent.id || '',
    agent.display_name || '',
    agent.hostname || '',
    agent.os || '',
    agent.arch || '',
    agent.transport || '',
    ...(Array.isArray(agent.tags) ? agent.tags : []),
  ].join(' ').toLowerCase();
  return haystack.includes(query);
}

function compareAgents(left, right) {
  const order = { on_schedule: 0, overdue: 1, offline: 2, never_seen: 3, retired: 4 };
  const stateDelta = (order[getAgentState(left)] ?? 9) - (order[getAgentState(right)] ?? 9);
  if (stateDelta) return stateDelta;
  return agentDisplayName(left).localeCompare(agentDisplayName(right), undefined, { sensitivity: 'base' });
}

function agentDisplayName(agent) {
  return SableLogic.agentDisplayName(agent);
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

  const main = document.createElement('button');
  main.type = 'button';
  main.className = 'agent-card-main';
  main.setAttribute('aria-label', (bulkSelectionMode ? 'Select ' : 'Open ') + agentDisplayName(agent));
  if (bulkSelectionMode) main.setAttribute('aria-pressed', isSelected ? 'true' : 'false');
  main.addEventListener('click', () => {
    if (bulkSelectionMode) toggleBulkSession(agent.id, !selectedAgentIDs.has(agent.id));
    else selectAgent(agent);
  });

  const topRow = document.createElement('div');
  topRow.className = 'agent-row-top';

  const host = document.createElement('span');
  host.className = 'agent-host';
  host.textContent = agentDisplayName(agent);

  const stateLabel = document.createElement('span');
  stateLabel.className = 'agent-state state-' + state;
  stateLabel.textContent = getAgentStateLabel(state);
  stateLabel.title = getAgentStateDescription(state);

  topRow.appendChild(host);
  topRow.appendChild(stateLabel);

  const details = document.createElement('dl');
  details.className = 'agent-card-details';
  [
    ['Platform', (agent.os || 'unknown') + ' / ' + (agent.arch || 'unknown')],
    ['Transport', agent.transport ? String(agent.transport).toUpperCase() : 'Not seen'],
    ['Last check-in', formatLastSeenCompact(agent)],
    ['Hostname', agent.hostname || 'Unknown'],
    ['ID', (agent.id || '').slice(0, 8) || 'Unknown'],
  ].forEach(([label, value]) => {
    const term = document.createElement('dt');
    term.textContent = label;
    const description = document.createElement('dd');
    description.textContent = value;
    details.appendChild(term);
    details.appendChild(description);
  });

  main.appendChild(topRow);
  main.appendChild(details);

  if (Array.isArray(agent.tags) && agent.tags.length) {
    const tags = document.createElement('span');
    tags.className = 'agent-card-tags';
    tags.textContent = agent.tags.slice(0, 3).map(tag => '#' + tag).join(' ');
    main.appendChild(tags);
  }

  const actions = document.createElement('div');
  actions.className = 'agent-actions';

  if (bulkSelectionMode) {
    const selectLabel = document.createElement('label');
    selectLabel.className = 'agent-select-control';
    const selectBox = document.createElement('input');
    selectBox.type = 'checkbox';
    selectBox.checked = isSelected;
    selectBox.setAttribute('aria-label', 'Select ' + agentDisplayName(agent) + ' for bulk task queueing');
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

  const menuWrap = document.createElement('div');
  menuWrap.className = 'agent-menu-wrap';
  const menuButton = document.createElement('button');
  menuButton.type = 'button';
  menuButton.className = 'agent-menu-button';
  menuButton.textContent = 'More';
  menuButton.setAttribute('aria-label', 'More actions for ' + agentDisplayName(agent));
  menuButton.setAttribute('aria-haspopup', 'menu');
  menuButton.setAttribute('aria-expanded', 'false');
  const menu = document.createElement('div');
  menu.className = 'agent-menu';
  menu.setAttribute('role', 'menu');
  const menuOpen = openAgentMenuID === agent.id;
  menu.hidden = !menuOpen;
  menuButton.setAttribute('aria-expanded', menuOpen ? 'true' : 'false');
  menuButton.addEventListener('click', e => {
    e.stopPropagation();
    const opening = menu.hidden;
    closeAgentMenus();
    openAgentMenuID = opening ? agent.id : '';
    menu.hidden = !opening;
    menuButton.setAttribute('aria-expanded', opening ? 'true' : 'false');
  });
  menu.appendChild(agentMenuButton('Open details', () => {
    selectAgent(agent);
    window.setTimeout(openSessionDetailsModal, 0);
  }));
  menu.appendChild(agentMenuButton('Edit info', () => {
    selectAgent(agent);
    window.setTimeout(openEditInfoModal, 0);
  }));
  menu.appendChild(agentMenuButton('Copy ID', async () => {
    try {
      await navigator.clipboard.writeText(agent.id);
      showToast('Agent ID copied.');
    } catch (_) {
      showToast('Could not copy the agent ID.', 'error');
    }
  }));
  menu.appendChild(agentMenuButton(agent.retired ? 'Restore' : 'Retire', () => {
    if (agent.retired) {
      updateAgentRetirement(false, agent.id);
      return;
    }
    openActionConfirm({
      title: 'Retire Agent',
      copy: 'Retire ' + agentDisplayName(agent) + '? History and artifacts will be preserved.',
      confirmLabel: 'Retire Agent',
      onConfirm: () => updateAgentRetirement(true, agent.id),
    });
  }));
  if (!agent.retired) {
    const killButton = agentMenuButton('Kill agent', () => openKillConfirmModal(agent));
    killButton.classList.add('danger-menu-item');
    menu.appendChild(killButton);
  }
  menuWrap.appendChild(menuButton);
  menuWrap.appendChild(menu);
  actions.appendChild(menuWrap);

  card.appendChild(main);
  card.appendChild(actions);
  li.appendChild(card);

  return li;
}

function agentMenuButton(label, onClick) {
  const button = document.createElement('button');
  button.type = 'button';
  button.textContent = label;
  button.setAttribute('role', 'menuitem');
  button.addEventListener('click', event => {
    event.stopPropagation();
    closeAgentMenus();
    onClick();
  });
  return button;
}

function closeAgentMenus(except) {
  if (!except) openAgentMenuID = '';
  document.querySelectorAll('.agent-menu').forEach(menu => {
    if (menu === except) return;
    menu.hidden = true;
    const button = menu.parentElement && menu.parentElement.querySelector('.agent-menu-button');
    if (button) button.setAttribute('aria-expanded', 'false');
  });
}

function setBulkSelectionMode(enabled) {
  bulkSelectionMode = Boolean(enabled);
  renderAgentList();
  updateBulkSelectionUI();
}

function toggleBulkSession(agentID, selected) {
  if (!agentID) return;
  if (selected) selectedAgentIDs.add(agentID);
  else selectedAgentIDs.delete(agentID);
  renderAgentList();
  updateBulkSelectionUI();
  if (taskTargetMode === 'selected' && selectedAgents().length > 0) updateTaskContextStatus();
}

function selectedAgents() {
  const selected = new Set(selectedAgentIDs);
  return allAgents.filter(agent => selected.has(agent.id) && getAgentState(agent) !== 'retired');
}

function updateBulkSelectionUI() {
  const bar = $('bulk-session-bar');
  const count = $('bulk-session-count');
  const clearButton = $('bulk-clear-btn');
  const modeButton = $('bulk-select-mode-btn');
  if (!bar || !count) return;

  const total = selectedAgents().length;
  bar.hidden = !bulkSelectionMode && total === 0;
  count.textContent = total === 1 ? '1 selected' : total + ' selected';
  if (clearButton) clearButton.disabled = total === 0;
  if (modeButton) {
    modeButton.textContent = bulkSelectionMode ? 'Done selecting' : 'Select multiple';
    modeButton.classList.toggle('active', bulkSelectionMode);
    modeButton.disabled = false;
    modeButton.setAttribute('aria-pressed', bulkSelectionMode ? 'true' : 'false');
    modeButton.title = bulkSelectionMode
      ? 'Finish selecting agents'
      : 'Enable multi-select for bulk task queueing';
  }
  updateBulkTaskButton();
  updateComposerReadiness();
}

function updateBulkTaskButton() {
  const button = $('bulk-send-btn');
  if (!button) return;

  const total = selectedAgents().length;
  const bulkAllowed = BULK_TASK_TYPES.has(selectedTaskType);
  // Bulk execution uses the visible target toggle; retain this legacy button
  // only as a hidden compatibility hook for older event wiring.
  button.hidden = true;
  button.textContent = total <= 1 ? 'Queue Selected' : 'Queue ' + total + ' Agents';
  button.disabled = taskRequestInFlight || total === 0 || !bulkAllowed;
  button.title = bulkAllowed
    ? 'Queue this task for every selected agent'
    : 'This action can only be queued for one agent at a time';
  updateTaskTargetUI();
}

function formatAgentPlatform(agent) {
  const os = agent.os || 'unknown';
  const arch = agent.arch || 'unknown';
  const transport = agent.transport ? ' / ' + String(agent.transport).toUpperCase() : '';
  return os + ' / ' + arch + transport;
}

function formatLastSeenCompact(agent) {
  const age = getAgentAgeMs(agent);
  if (!Number.isFinite(age)) return 'Never seen';
  return formatRelativeAge(age);
}

function formatLastSeenDetailed(agent) {
  if (!agent || !Number.isFinite(getAgentAgeMs(agent))) return 'Last seen never';
  const date = new Date(agent.last_seen);
  if (!Number.isFinite(date.getTime())) return 'Last seen unknown';
  let label = 'Last seen ' + date.toLocaleTimeString() + ' (' + formatRelativeAge(getAgentAgeMs(agent)) + ')';
  if (agent.expected_next_seen) {
    const expected = new Date(agent.expected_next_seen);
    if (Number.isFinite(expected.getTime()) && expected.getTime() > 0) label += ' · expected ' + expected.toLocaleTimeString();
  }
  return label;
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
  if (state === 'on_schedule') {
    warning.hidden = true;
    warning.textContent = '';
    warning.className = 'session-warning';
    return;
  }

  warning.hidden = false;
  warning.className = 'session-warning state-' + state;
  if (state === 'overdue') {
    warning.textContent = 'Agent is overdue for its expected check-in. Verify recency before queueing follow-up tasks or starting interactive mode.';
    return;
  }

  if (state === 'never_seen') {
    warning.textContent = 'This identity is registered but has never checked in. Tasks will remain queued until the deployed agent starts.';
    return;
  }

  if (state === 'retired') {
    warning.textContent = 'This agent is retired. Its history is preserved; restore it before resuming normal operations.';
    return;
  }

  warning.textContent = 'Agent is offline. New tasks will remain queued until it checks in again.';
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
  const previousAgentID = activeAgentID;
  saveActiveTaskDraft();
  persistActiveFileBrowserState();
  exitInteractiveMode(false);
  resetActivePathCompletion();
  if (previousAgentID && previousAgentID !== agent.id) resetPathBrowserState(previousAgentID, true);
  closeClearConfirmModal();
  activeAgentID = agent.id;
  activeAgent = agent;
	rememberAgentTaskMetadata(agent);
	metadataDirty = false;
	metadataDraftAgentID = '';
	activateFileBrowserState(agent.id);
	$('rekey-secret-output').hidden = true;
	$('rekey-secret-output').textContent = '';
	$('copy-rekey-secret-btn').hidden = true;
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
  updateOutputSearchUI(true);
  clearPendingUpload();
  stopSSEStream();
  setPrimaryView('agents');
  setAgentRoute(agent.id);
  renderAgentList();
  updateSessionHeader();
  updateTaskContextStatus();
  updateOutputControls();
  updateOutputEmptyState();
  startSSEStream(agent.id, false);
  loadArtifacts(agent.id);
  loadOutputs();
  loadAudit();
  refreshActiveAgent();
  restoreActiveTaskDraft();
  focusPrimaryInput(false);
}

function updateSessionHeader() {
  if (!activeAgent) {
    $('console-title').textContent = 'Select an agent';
    $('console-meta').hidden = true;
    $('meta-state').hidden = true;
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
  const connectionState = state === 'on_schedule' ? 'on_schedule' : 'offline';

  $('console-title').textContent = agentDisplayName(activeAgent);
  $('meta-state').textContent = connectionState === 'on_schedule' ? 'Active' : 'Offline';
  $('meta-state').title = connectionState === 'on_schedule'
    ? 'The agent is actively checking in.'
    : 'The agent is not currently connected.';
  $('meta-state').className = 'meta-chip meta-state-chip state-' + connectionState;
  $('meta-platform').textContent = (activeAgent.os || 'unknown') + ' / ' + (activeAgent.arch || 'unknown');
  $('meta-transport').textContent = activeAgent.transport ? String(activeAgent.transport).toUpperCase() : 'Transport not seen';
  $('meta-lastseen').textContent = 'Last seen ' + formatLastSeenCompact(activeAgent);
  const tags = $('meta-tags');
  tags.textContent = '';
  (activeAgent.tags || []).slice(0, 4).forEach(tag => {
    const chip = document.createElement('span');
    chip.className = 'meta-chip meta-tag-chip';
    chip.textContent = '#' + tag;
    tags.appendChild(chip);
  });
  $('console-meta').hidden = false;
  $('meta-state').hidden = false;
  $('input-area').hidden = false;
  $('clear-btn').hidden = false;
  $('save-output-btn').hidden = false;
  $('session-details-btn').hidden = !$('session-details-modal').hidden;
  $('output-toolbar').hidden = false;
  $('output-resizer').hidden = false;
	const metadataAgentChanged = metadataDraftAgentID !== activeAgent.id;
	if (metadataAgentChanged || !metadataDirty) {
		$('tag-input').value = (activeAgent.tags || []).join(', ');
		$('display-name-input').value = activeAgent.display_name || '';
		$('notes-input').value = activeAgent.notes || '';
	}
	if (metadataAgentChanged) $('metadata-save-status').textContent = '';
	metadataDraftAgentID = activeAgent.id;
	$('artifact-retention-input').value = String(activeAgent.artifact_retention || 256);
  $('retire-agent-btn').textContent = activeAgent.retired ? 'Restore Agent' : 'Retire Agent';
  updateSessionWarning();

  if (!interactiveMode) applyTaskTypeUI();
  renderSessionPanels();
}

async function refreshActiveAgent() {
  if (!activeAgentID) return;
  const agentID = activeAgentID;
  try {
    const resp = await apiFetch('/api/agents/' + agentID);
    if (!resp.ok || agentID !== activeAgentID) return;
    const detail = await resp.json();
    if (agentID !== activeAgentID) return;
    const summary = allAgents.find(agent => agent.id === agentID) || {};
    activeAgent = { ...summary, ...detail };
    rememberAgentTaskMetadata(activeAgent);
    allAgents = allAgents.map(agent => agent.id === agentID ? { ...agent, ...activeAgent } : agent);
    updateSessionHeader();
    renderAgentList();
    renderDashboard();
    renderSessionPanels();
  } catch (_) {
    // Keep the last known detail view on transient refresh failures.
  }
}
