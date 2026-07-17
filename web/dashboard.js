'use strict';

let overviewOutcomeRange = '24h';

function setPrimaryView(view, updateHash = true) {
  const next = view === 'agents' ? 'agents' : 'overview';
  activePrimaryView = next;
  $('overview-view').hidden = next !== 'overview';
  $('content').hidden = next !== 'agents';
  $('overview-nav-btn').classList.toggle('active', next === 'overview');
  $('agents-nav-btn').classList.toggle('active', next === 'agents');
  if (next === 'overview') {
    $('overview-nav-btn').setAttribute('aria-current', 'page');
    $('agents-nav-btn').removeAttribute('aria-current');
  } else {
    $('agents-nav-btn').setAttribute('aria-current', 'page');
    $('overview-nav-btn').removeAttribute('aria-current');
  }
  if (!updateHash) return;
  const hash = next === 'overview'
    ? '#overview'
    : activeAgentID
      ? '#agents/' + encodeURIComponent(activeAgentID)
      : '#agents';
  if (window.location.hash !== hash) window.location.hash = hash;
}

function setAgentRoute(agentID) {
  if (!agentID || activePrimaryView !== 'agents') return;
  const hash = '#agents/' + encodeURIComponent(agentID);
  if (window.location.hash !== hash) window.history.replaceState(null, '', hash);
}

function handlePrimaryRoute(initial) {
  const hash = String(window.location.hash || '').replace(/^#/, '');
  if (!hash || hash === 'overview') {
    pendingRouteAgentID = '';
    setPrimaryView('overview', false);
    return;
  }
  if (hash === 'agents' || hash.startsWith('agents/')) {
    setPrimaryView('agents', false);
    const encodedID = hash.slice('agents/'.length);
    if (encodedID && hash.startsWith('agents/')) {
      try {
        pendingRouteAgentID = decodeURIComponent(encodedID);
      } catch (_) {
        pendingRouteAgentID = '';
      }
      resolvePendingAgentRoute();
    } else if (initial && !activeAgentID) {
      pendingRouteAgentID = '';
    }
    return;
  }
  setPrimaryView('overview', false);
}

function resolvePendingAgentRoute() {
  if (!pendingRouteAgentID || !allAgents.length) return;
  const match = allAgents.find(agent => agent.id === pendingRouteAgentID);
  pendingRouteAgentID = '';
  if (match && match.id !== activeAgentID) selectAgent(match);
}

window.addEventListener('hashchange', () => handlePrimaryRoute(false));

function resetDashboard() {
  [
    'overview-total', 'overview-schedule', 'overview-overdue', 'overview-offline',
    'overview-never', 'overview-queued', 'overview-running', 'overview-transfers',
    'overview-failures',
  ].forEach(id => { $(id).textContent = '0'; });
  $('active-job-count').textContent = '0';
  $('active-transfer-count').textContent = '0';
  $('overview-agent-list').textContent = '';
  $('overview-attention').textContent = '';
  $('overview-recent-activity').textContent = '';
  $('overview-fleet-mix').textContent = '';
  $('outcome-chart').textContent = '';
  $('outcome-successful-total').textContent = '0';
  $('outcome-failed-total').textContent = '0';
  $('overview-health-copy').textContent = 'Waiting for fleet data.';
  $('overview-result-count').textContent = '0 agents';
  $('overview-updated').textContent = 'Waiting for data';
  $('overview-filter').value = '';
  $('overview-status-filter').value = 'all';
  $('overview-transport-filter').value = 'all';
  $('overview-show-retired').checked = false;
  setOverviewOutcomeRange('24h', false);
}

function renderDashboard() {
  const overview = fleetOverview || {};
  const failuresNeedingAttention = visibleOverviewFailureCount(overview);
  $('overview-total').textContent = String(overview.total || 0);
  $('overview-schedule').textContent = String(overview.on_schedule || 0);
  $('overview-overdue').textContent = String(overview.overdue || 0);
  $('overview-offline').textContent = String(overview.offline || 0);
  $('overview-never').textContent = String(overview.never_seen || 0);
  $('overview-queued').textContent = String(overview.queued_tasks || 0);
  $('overview-running').textContent = String(overview.running_tasks || 0);
  $('overview-transfers').textContent = String(overview.active_transfers || 0);
  $('overview-failures').textContent = String(failuresNeedingAttention);
  const activeJobs = overviewActiveJobs().length;
  const activeTransfers = Number(overview.active_transfers || 0);
  $('active-job-count').textContent = String(activeJobs);
  $('active-transfer-count').textContent = String(activeTransfers);
  $('active-jobs-btn').classList.toggle('has-activity', activeJobs > 0);
  $('active-transfers-btn').classList.toggle('has-activity', activeTransfers > 0);
  $('active-jobs-btn').setAttribute('aria-label', activeJobs + ' active job' + (activeJobs === 1 ? '' : 's') + '. Open active jobs dialog.');
  $('active-transfers-btn').setAttribute('aria-label', activeTransfers + ' active transfer' + (activeTransfers === 1 ? '' : 's') + '. Open the Agents workspace.');
  renderOverviewHealthSummary(overview, failuresNeedingAttention);

  updateOverviewPlatformOptions();
  const query = $('overview-filter').value.trim().toLowerCase();
  const status = $('overview-status-filter').value || 'all';
  const platform = $('overview-platform-filter').value || 'all';
  const transport = $('overview-transport-filter').value || 'all';
  const includeRetired = $('overview-show-retired').checked || status === 'retired';

  const agents = allAgents
    .filter(agent => includeRetired || getAgentState(agent) !== 'retired')
    .filter(agent => status === 'all' || getAgentState(agent) === status)
    .filter(agent => platform === 'all' || String(agent.os || 'unknown') === platform)
    .filter(agent => {
      if (transport === 'all') return true;
      if (transport === 'unknown') return !agent.transport;
      return String(agent.transport || '').toLowerCase() === transport;
    })
    .filter(agent => matchesAgentFilter(agent, query))
    .sort(compareAgents);

  $('overview-result-count').textContent = agents.length === 1 ? '1 agent' : agents.length + ' agents';
  const body = $('overview-agent-list');
  body.textContent = '';
  $('overview-filter-empty').hidden = agents.length > 0;
  agents.forEach(agent => body.appendChild(fleetAgentRow(agent)));
  renderOverviewAttention();
  renderOverviewRecentActivity();
  renderOverviewFleetMix();
  renderTaskOutcomes();
  if (!$('active-jobs-modal').hidden) renderActiveJobsModal();
}

function overviewActiveJobs() {
  return fleetOverview && Array.isArray(fleetOverview.active_jobs)
    ? fleetOverview.active_jobs.filter(job => job && job.id && job.agent_id)
    : [];
}

function renderActiveJobsModal() {
  const jobs = overviewActiveJobs();
  const count = $('active-jobs-modal-count');
  const list = $('active-jobs-list');
  count.textContent = jobs.length === 1 ? '1 job' : jobs.length + ' jobs';
  list.textContent = '';

  if (!jobs.length) {
    const empty = document.createElement('p');
    empty.className = 'active-jobs-empty';
    empty.textContent = 'No jobs are active.';
    list.appendChild(empty);
    return;
  }

  jobs.forEach(job => {
    const item = document.createElement('article');
    item.className = 'active-job-item';

    const state = document.createElement('span');
    state.className = 'active-job-state';
    state.textContent = 'Processing';

    const copy = document.createElement('div');
    copy.className = 'active-job-copy';
    const title = document.createElement('strong');
    title.textContent = humanTaskType(job.type);
    const agent = document.createElement('span');
    agent.textContent = job.agent_name || 'Agent ' + job.agent_id.slice(0, 8);
    const timing = document.createElement('span');
    timing.className = 'active-job-timing';
    timing.textContent = job.received_at ? 'Received ' + formatEventAge(job.received_at) : 'Received by host';
    copy.appendChild(title);
    copy.appendChild(agent);
    copy.appendChild(timing);
    if (job.payload) {
      const payload = document.createElement('code');
      payload.textContent = job.payload;
      copy.appendChild(payload);
    }

    const open = document.createElement('button');
    open.type = 'button';
    open.textContent = 'Open agent';
    const target = allAgents.find(agentSummary => agentSummary.id === job.agent_id);
    open.disabled = !target;
    if (target) {
      open.addEventListener('click', () => {
        closeActiveJobsModal();
        selectAgent(target);
      });
    }

    item.appendChild(state);
    item.appendChild(copy);
    item.appendChild(open);
    list.appendChild(item);
  });
}

function updateOverviewPlatformOptions() {
  const select = $('overview-platform-filter');
  const previous = select.value || 'all';
  const platforms = Array.from(new Set(allAgents.map(agent => String(agent.os || 'unknown')))).sort();
  select.textContent = '';
  select.appendChild(selectOption('all', 'All platforms'));
  platforms.forEach(platform => select.appendChild(selectOption(platform, platform === 'unknown' ? 'Not seen' : platform)));
  select.value = platforms.includes(previous) || previous === 'all' ? previous : 'all';
}

function selectOption(value, label) {
  const option = document.createElement('option');
  option.value = value;
  option.textContent = label;
  return option;
}

function fleetAgentRow(agent) {
  const row = document.createElement('tr');
  row.className = 'fleet-agent-row';

  const identity = document.createElement('td');
  const name = document.createElement('button');
  name.type = 'button';
  name.className = 'fleet-agent-name';
  const displayName = agentDisplayName(agent);
  name.textContent = displayName;
  name.addEventListener('click', () => selectAgent(agent));
  const detail = document.createElement('span');
  detail.className = 'fleet-agent-detail';
  detail.textContent = (agent.hostname && agent.hostname !== displayName ? agent.hostname + ' · ' : '') + agent.id.slice(0, 8);
  identity.appendChild(name);
  identity.appendChild(detail);
  row.appendChild(identity);

  const stateCell = document.createElement('td');
  const state = getAgentState(agent);
  const badge = document.createElement('span');
  badge.className = 'fleet-state state-' + state;
  badge.textContent = getAgentStateLabel(state);
  badge.title = getAgentStateDescription(state);
  stateCell.appendChild(badge);
  row.appendChild(stateCell);

  row.appendChild(fleetTextCell((agent.os || 'unknown') + ' / ' + (agent.arch || 'unknown')));
  row.appendChild(fleetTextCell(agent.transport ? String(agent.transport).toUpperCase() : '—'));

  const seen = fleetTextCell(formatLastSeenCompact(agent));
  seen.title = formatLastSeenDetailed(agent);
  row.appendChild(seen);

  const work = fleetTextCell(String(agent.queued_count || 0) + ' queued · ' + String(agent.running_count || 0) + ' running');
  if (agent.active_transfers) work.appendChild(fleetSubLabel(agent.active_transfers + ' transfer' + (agent.active_transfers === 1 ? '' : 's')));
  row.appendChild(work);
  row.appendChild(fleetTextCell(String(agent.artifact_count || 0)));

  const tags = document.createElement('td');
  tags.className = 'fleet-tags';
  (agent.tags || []).slice(0, 4).forEach(tag => tags.appendChild(fleetTag(tag)));
  if (!(agent.tags || []).length) tags.textContent = '—';
  row.appendChild(tags);

  const action = document.createElement('td');
  action.className = 'fleet-agent-actions';
  const edit = document.createElement('button');
  edit.type = 'button';
  edit.textContent = 'Edit';
  edit.addEventListener('click', () => openEditInfoModal(agent));
  const open = document.createElement('button');
  open.type = 'button';
  open.textContent = 'Open workspace';
  open.addEventListener('click', () => selectAgent(agent));
  action.appendChild(edit);
  action.appendChild(open);
  row.appendChild(action);
  return row;
}

function fleetTextCell(text) {
  const cell = document.createElement('td');
  cell.textContent = text || '';
  return cell;
}

function fleetSubLabel(text) {
  const label = document.createElement('span');
  label.className = 'fleet-sub-label';
  label.textContent = text;
  return label;
}

function fleetTag(tag) {
  const chip = document.createElement('span');
  chip.className = 'fleet-tag';
  chip.textContent = tag;
  return chip;
}

function countBy(items, keyFn) {
  const counts = new Map();
  items.forEach(item => {
    const key = keyFn(item);
    counts.set(key, (counts.get(key) || 0) + 1);
  });
  return Array.from(counts.entries()).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

function renderOverviewAttentionLegacy() {
  const container = $('overview-attention');
  container.textContent = '';
  const attention = allAgents
    .filter(agent => ['overdue', 'offline', 'never_seen'].includes(getAgentState(agent)))
    .sort(compareAgents)
    .slice(0, 6);
  if (!attention.length && !(fleetOverview && fleetOverview.failed_last_24_hours)) {
    container.appendChild(overviewHint('No agents currently need attention.'));
    return;
  }
  attention.forEach(agent => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'attention-item';
    button.textContent = agentDisplayName(agent) + ' · ' + getAgentStateLabel(getAgentState(agent));
    button.addEventListener('click', () => selectAgent(agent));
    container.appendChild(button);
  });
  if (fleetOverview && fleetOverview.failed_last_24_hours) {
    container.appendChild(overviewHint(fleetOverview.failed_last_24_hours + ' failed task result(s) in the last 24 hours.'));
  }
}

function overviewHint(text) {
  const hint = document.createElement('p');
  hint.className = 'overview-hint';
  hint.textContent = text;
  return hint;
}

function renderOverviewHealthSummary(overview, failuresNeedingAttention) {
  const total = Math.max(0, Number(overview.total || 0) - Number(overview.retired || 0));
  const active = Number(overview.on_schedule || 0);
  const attention = Number(overview.never_seen || 0) + Number(overview.overdue || 0) + Number(overview.offline || 0);
  const failures = Number(failuresNeedingAttention || 0);
  let copy = 'No active agents are registered.';
  if (total > 0) {
    copy = active + ' of ' + total + ' ' + pluralize(total, 'agent', 'agents') + ' ' +
      (active === 1 ? 'is' : 'are') + ' checking in normally. ';
    if (attention === 0 && failures === 0) {
      copy += 'No agents need attention and no tasks failed today.';
    } else {
      const parts = [];
      if (attention > 0) parts.push(attention + ' ' + pluralize(attention, 'agent needs', 'agents need') + ' attention');
      if (failures > 0) parts.push(failures + ' ' + pluralize(failures, 'task failed', 'tasks failed') + ' today');
      copy += parts.join(' and ') + '.';
    }
  }
  $('overview-health-copy').textContent = copy;
  const tone = Number(overview.offline || 0) > 0 || failures > 0
    ? 'danger'
    : attention > 0
      ? 'warning'
      : 'healthy';
  $('overview-health-summary').className = 'overview-health-summary state-' + tone;
  setOverviewHealthIcon(tone);
}

function renderOverviewAttention() {
  const container = $('overview-attention');
  container.textContent = '';
  const entries = allAgents
    .filter(agent => ['overdue', 'offline', 'never_seen'].includes(getAgentState(agent)))
    .sort(compareAgents)
    .map(agent => ({
      tone: getAgentState(agent) === 'offline' ? 'danger' : 'warning',
      agent,
      title: attentionTitle(agent),
      detail: attentionDetail(agent),
    }));

  visibleOverviewFailureAlerts().forEach(alert => {
    const agent = allAgents.find(item => item.id === alert.agent_id);
    entries.push({
      tone: 'danger',
      kind: 'failure',
      alertID: alert.id,
      agent: agent || { id: alert.agent_id, display_name: alert.agent_name },
      title: humanTaskType(alert.task_type) + ' failed',
      detail: formatEventAge(alert.timestamp),
    });
  });

  if (!entries.length) {
    const healthy = document.createElement('div');
    healthy.className = 'attention-empty';
    healthy.appendChild(overviewIcon('healthy', 'success'));
    const copy = document.createElement('div');
    const title = document.createElement('strong');
    title.textContent = 'All clear';
    const detail = document.createElement('span');
    detail.textContent = 'No agents currently need attention.';
    copy.appendChild(title);
    copy.appendChild(detail);
    healthy.appendChild(copy);
    container.appendChild(healthy);
    return;
  }
  entries.forEach(entry => container.appendChild(attentionEntry(entry)));
}

function attentionEntry(entry) {
  const item = document.createElement('article');
  item.className = 'attention-entry tone-' + entry.tone;
  item.appendChild(overviewIcon(entry.tone === 'danger' ? 'failure' : 'warning', entry.tone));
  const copy = document.createElement('div');
  copy.className = 'attention-copy';
  const name = document.createElement('strong');
  name.textContent = agentDisplayName(entry.agent);
  const title = document.createElement('span');
  title.className = 'attention-title';
  title.textContent = entry.title;
  const detail = document.createElement('span');
  detail.className = 'attention-detail';
  detail.textContent = entry.detail;
  copy.appendChild(name);
  copy.appendChild(title);
  copy.appendChild(detail);
  item.appendChild(copy);
  if (entry.kind === 'failure') {
    item.appendChild(failureAlertActions(entry));
  } else if (entry.agent && entry.agent.id) {
    const open = document.createElement('button');
    open.type = 'button';
    open.textContent = 'Open workspace';
    open.addEventListener('click', () => selectAgent(entry.agent));
    item.appendChild(open);
  }
  return item;
}

function failureAlertActions(entry) {
  const actions = document.createElement('div');
  actions.className = 'attention-actions';
  if (entry.agent && entry.agent.id && allAgents.some(agent => agent.id === entry.agent.id)) {
    actions.appendChild(attentionAction('Open', () => selectAgent(entry.agent), 'Open agent workspace'));
  }
  actions.appendChild(attentionAction('Ignore', () => ignoreFailureAlert(entry.alertID), 'Hide until this browser session ends'));
  actions.appendChild(attentionAction(
    'Acknowledge',
    () => requestFailureAlertAcknowledgement(entry),
    'Acknowledge this alert without changing agent output',
    failureAlertActionIDs.has(entry.alertID),
  ));
  return actions;
}

function attentionAction(label, onClick, title, disabled = false) {
  const button = document.createElement('button');
  button.type = 'button';
  button.textContent = label;
  button.title = title;
  button.disabled = disabled;
  button.addEventListener('click', onClick);
  return button;
}

function overviewFailureAlerts() {
  return fleetOverview && Array.isArray(fleetOverview.failure_alerts)
    ? fleetOverview.failure_alerts
    : [];
}

function visibleOverviewFailureAlerts() {
  return overviewFailureAlerts().filter(alert => alert && alert.id && !ignoredFailureAlertIDs.has(alert.id));
}

function visibleOverviewFailureCount(overview) {
  const ignoredCount = overviewFailureAlerts().filter(alert => alert && ignoredFailureAlertIDs.has(alert.id)).length;
  return Math.max(0, Number(overview.failed_last_24_hours || 0) - ignoredCount);
}

function ignoreFailureAlert(alertID) {
  if (!alertID) return;
  ignoredFailureAlertIDs.add(alertID);
  renderDashboard();
  showToast('Failure alert ignored for this browser session.');
}

function requestFailureAlertAcknowledgement(entry) {
  if (!entry || !entry.alertID) return;
  if (!shouldShowFailureAcknowledgementWarning()) {
    resolveFailureAlert(entry.alertID);
    return;
  }
  openActionConfirm({
    title: 'Acknowledge task failure?',
    copy: 'This removes only the alert from Overview. The agent output, Recent Activity entry, and task-outcome history will remain available.',
    confirmLabel: 'Acknowledge',
    showPreference: true,
    preferenceLabel: 'Show this warning next time',
    preferenceChecked: true,
    onConfirm: async () => {
      setFailureAcknowledgementWarningPreference($('action-confirm-show-next').checked);
      await resolveFailureAlert(entry.alertID);
    },
  });
}

function shouldShowFailureAcknowledgementWarning() {
  try {
    return window.localStorage.getItem('sable.overview.failure-ack-warning') !== 'false';
  } catch (_) {
    return true;
  }
}

function setFailureAcknowledgementWarningPreference(showNextTime) {
  try {
    window.localStorage.setItem('sable.overview.failure-ack-warning', showNextTime ? 'true' : 'false');
  } catch (_) {
    // Storage can be unavailable in hardened/private browser contexts.
  }
}

async function resolveFailureAlert(alertID) {
  if (!alertID || failureAlertActionIDs.has(alertID)) return;
  failureAlertActionIDs.add(alertID);
  renderOverviewAttention();
  try {
    const resp = await apiFetch('/api/overview/alerts/' + encodeURIComponent(alertID), {
      method: 'PUT',
      body: JSON.stringify({ disposition: 'acknowledged' }),
    });
    if (!resp.ok) throw new Error('request failed (' + resp.status + ')');
    if (fleetOverview) {
      fleetOverview.failure_alerts = overviewFailureAlerts().filter(alert => alert.id !== alertID);
      fleetOverview.failed_last_24_hours = Math.max(0, Number(fleetOverview.failed_last_24_hours || 0) - 1);
    }
    ignoredFailureAlertIDs.delete(alertID);
    renderDashboard();
    showToast('Failure alert acknowledged. Agent output and outcome history were retained.');
  } catch (error) {
    showToast('Unable to update the failure alert. ' + error.message, 'danger');
  } finally {
    failureAlertActionIDs.delete(alertID);
    renderOverviewAttention();
  }
}

function attentionTitle(agent) {
  const state = getAgentState(agent);
  if (state === 'never_seen') return 'Has never checked in';
  if (state === 'offline') return 'No check-in for ' + formatDuration(getAgentAgeMs(agent));
  const expected = new Date(agent.expected_next_seen || '').getTime();
  const lateBy = Number.isFinite(expected) ? Math.max(0, Date.now() - expected) : getAgentAgeMs(agent);
  return 'Missed expected check-in by ' + formatDuration(lateBy);
}

function attentionDetail(agent) {
  if (getAgentState(agent) === 'never_seen') {
    const registeredAt = new Date(agent.registered_at || '').getTime();
    return Number.isFinite(registeredAt) ? 'Registered ' + formatRelativeAge(Date.now() - registeredAt) : 'Waiting for first beacon';
  }
  const transport = agent.transport ? ' via ' + String(agent.transport).toUpperCase() : '';
  return 'Last seen ' + formatLastSeenCompact(agent) + transport;
}

function renderOverviewRecentActivity() {
  const container = $('overview-recent-activity');
  container.textContent = '';
  const activity = overviewActivity();
  if (!activity.length) {
    container.appendChild(overviewHint('Completed tasks, artifacts, and agent state changes will appear here.'));
    return;
  }
  activity.forEach(event => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'recent-activity-item tone-' + activityTone(event.kind);
    button.appendChild(overviewIcon(activityIcon(event.kind), activityTone(event.kind)));
    const title = document.createElement('span');
    title.className = 'recent-activity-title';
    title.textContent = activityTitle(event);
    const time = document.createElement('span');
    time.className = 'recent-activity-time';
    time.textContent = formatEventAge(event.timestamp);
    button.appendChild(title);
    button.appendChild(time);
    const agent = allAgents.find(item => item.id === event.agent_id);
    if (agent) button.addEventListener('click', () => selectAgent(agent));
    else button.disabled = true;
    container.appendChild(button);
  });
}

function renderOverviewFleetMix() {
  const container = $('overview-fleet-mix');
  container.textContent = '';
  const active = allAgents.filter(agent => getAgentState(agent) !== 'retired');
  if (!active.length) {
    container.appendChild(overviewHint('No active agents.'));
    return;
  }
  const platforms = countBy(active, agent => titleCase(agent.os || 'Not seen'));
  const transports = countBy(active, agent => agent.transport ? String(agent.transport).toUpperCase() : 'Not seen');
  container.appendChild(fleetMixRow('platform', compactDistribution(platforms)));
  container.appendChild(fleetMixRow('transport', compactDistribution(transports)));
}

function fleetMixRow(icon, text) {
  const row = document.createElement('div');
  row.className = 'fleet-mix-row';
  row.appendChild(overviewIcon(icon, 'muted'));
  const copy = document.createElement('span');
  copy.textContent = text;
  row.appendChild(copy);
  return row;
}

function compactDistribution(values) {
  return values.slice(0, 4).map(([label, count]) => label + ' ' + count).join(' · ');
}

function setOverviewOutcomeRange(range, render = true) {
  overviewOutcomeRange = range === '7d' ? '7d' : '24h';
  const is24Hours = overviewOutcomeRange === '24h';
  $('outcome-range-24h').classList.toggle('active', is24Hours);
  $('outcome-range-7d').classList.toggle('active', !is24Hours);
  $('outcome-range-24h').setAttribute('aria-pressed', is24Hours ? 'true' : 'false');
  $('outcome-range-7d').setAttribute('aria-pressed', is24Hours ? 'false' : 'true');
  if (render) renderTaskOutcomes();
}

function renderTaskOutcomes() {
  const overview = fleetOverview || {};
  const buckets = overviewOutcomeRange === '7d' ? overview.task_outcomes_7d : overview.task_outcomes_24h;
  const values = Array.isArray(buckets) ? buckets : [];
  const successful = values.reduce((total, bucket) => total + Number(bucket.successful || 0), 0);
  const failed = values.reduce((total, bucket) => total + Number(bucket.failed || 0), 0);
  $('outcome-successful-total').textContent = String(successful);
  $('outcome-failed-total').textContent = String(failed);
  $('outcome-chart').setAttribute(
    'aria-label',
    successful + ' successful and ' + failed + ' failed task ' + pluralize(successful + failed, 'outcome', 'outcomes') +
      ' in the selected ' + (overviewOutcomeRange === '7d' ? '7 day' : '24 hour') + ' range.',
  );
  drawOutcomeChart(values);
}

function drawOutcomeChart(values) {
  const container = $('outcome-chart');
  container.textContent = '';
  const svg = svgElement('svg', { viewBox: '0 0 920 154', preserveAspectRatio: 'xMidYMid meet', 'aria-hidden': 'true' });
  const width = 920;
  const height = 154;
  const left = 34;
  const right = 8;
  const top = 10;
  const bottom = 26;
  const plotWidth = width - left - right;
  const plotHeight = height - top - bottom;
  const maxValue = Math.max(1, ...values.map(bucket => Number(bucket.successful || 0) + Number(bucket.failed || 0)));
  const roundedMax = Math.max(2, Math.ceil(maxValue / 2) * 2);

  [roundedMax, roundedMax / 2, 0].forEach((value, index) => {
    const y = top + (plotHeight * index / 2);
    svg.appendChild(svgElement('line', { x1: left, x2: width - right, y1: y, y2: y, class: 'outcome-grid-line' }));
    const label = svgElement('text', { x: left - 9, y: y + 4, class: 'outcome-axis-value', 'text-anchor': 'end' });
    label.textContent = String(Math.round(value));
    svg.appendChild(label);
  });

  if (values.length) {
    const slot = plotWidth / values.length;
    const barWidth = Math.max(7, Math.min(18, slot * 0.48));
    values.forEach((bucket, index) => {
      const success = Number(bucket.successful || 0);
      const failure = Number(bucket.failed || 0);
      const successHeight = (success / roundedMax) * plotHeight;
      const failureHeight = (failure / roundedMax) * plotHeight;
      const x = left + (slot * index) + ((slot - barWidth) / 2);
      const baseline = top + plotHeight;
      if (success > 0) {
        svg.appendChild(svgElement('rect', {
          x, y: baseline - successHeight, width: barWidth, height: successHeight, rx: 1.5, class: 'outcome-bar-success',
        }));
      }
      if (failure > 0) {
        svg.appendChild(svgElement('rect', {
          x, y: baseline - successHeight - failureHeight, width: barWidth, height: failureHeight, rx: 1.5, class: 'outcome-bar-failure',
        }));
      }
      const title = svgElement('title');
      title.textContent = outcomeBucketLabel(bucket.start) + ': ' + success + ' successful, ' + failure + ' failed';
      const hit = svgElement('rect', {
        x: left + (slot * index), y: top, width: slot, height: plotHeight, class: 'outcome-bar-hit',
      });
      hit.appendChild(title);
      svg.appendChild(hit);

      const labelEvery = overviewOutcomeRange === '7d' ? 1 : 4;
      if (index % labelEvery === 0) {
        const label = svgElement('text', {
          x: left + (slot * index) + (slot / 2), y: height - 6, class: 'outcome-axis-label', 'text-anchor': 'middle',
        });
        label.textContent = outcomeBucketLabel(bucket.start);
        svg.appendChild(label);
      }
    });
  }
  container.appendChild(svg);
}

function outcomeBucketLabel(value) {
  const date = new Date(value || '');
  if (!Number.isFinite(date.getTime())) return '—';
  return overviewOutcomeRange === '7d'
    ? date.toLocaleDateString([], { weekday: 'short' })
    : date.toLocaleTimeString([], { hour: 'numeric' });
}

function overviewActivity() {
  const activity = fleetOverview && Array.isArray(fleetOverview.recent_activity)
    ? fleetOverview.recent_activity.slice()
    : [];
  return activity.sort((left, right) => eventTimestamp(right) - eventTimestamp(left));
}

function eventTimestamp(event) {
  const value = new Date(event && event.timestamp ? event.timestamp : '').getTime();
  return Number.isFinite(value) ? value : 0;
}

function formatEventAge(value) {
  const timestamp = new Date(value || '').getTime();
  return Number.isFinite(timestamp) ? formatRelativeAge(Math.max(0, Date.now() - timestamp)) : 'Unknown time';
}

function formatDuration(value) {
  if (!Number.isFinite(value)) return 'an unknown time';
  if (value < 60 * 1000) return Math.max(1, Math.round(value / 1000)) + 's';
  if (value < 60 * 60 * 1000) return Math.max(1, Math.round(value / 60000)) + 'm';
  if (value < 24 * 60 * 60 * 1000) return Math.max(1, Math.round(value / 3600000)) + 'h';
  return Math.max(1, Math.round(value / 86400000)) + 'd';
}

function activityTitle(event) {
  const name = event.agent_name || 'Unknown agent';
  switch (event.kind) {
  case 'task_success':
    return 'Task completed on ' + name;
  case 'task_failed':
    return humanTaskType(event.task_type) + ' failed on ' + name;
  case 'artifact_received':
    return 'Artifact received from ' + name;
  case 'agent_overdue':
    return name + ' became overdue';
  case 'agent_offline':
    return name + ' became offline';
  default:
    return name + ' activity';
  }
}

function activityTone(kind) {
  if (kind === 'task_failed' || kind === 'agent_offline') return 'danger';
  if (kind === 'agent_overdue') return 'warning';
  return 'success';
}

function activityIcon(kind) {
  if (kind === 'task_failed') return 'failure';
  if (kind === 'artifact_received') return 'artifact';
  if (kind === 'agent_overdue' || kind === 'agent_offline') return 'warning';
  return 'healthy';
}

function humanTaskType(value) {
  const text = String(value || 'task').replace(/_progress$/, '').replace(/_/g, ' ');
  return titleCase(text);
}

function titleCase(value) {
  return String(value || '').replace(/\b\w/g, letter => letter.toUpperCase());
}

function pluralize(count, singular, plural) {
  return Number(count) === 1 ? singular : plural;
}

function setOverviewHealthIcon(tone) {
  const holder = document.querySelector('.overview-health-icon');
  if (!holder) return;
  holder.textContent = '';
  holder.appendChild(overviewIcon(tone === 'healthy' ? 'healthy' : 'warning', tone));
}

function overviewIcon(kind, tone) {
  const wrapper = document.createElement('span');
  wrapper.className = 'overview-semantic-icon tone-' + (tone || 'muted');
  const svg = svgElement('svg', { viewBox: '0 0 24 24', 'aria-hidden': 'true' });
  const paths = {
    healthy: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z', 'm8 12 2.5 2.5L16 9'],
    warning: ['M10.3 3.8 2.4 18a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 3.8a2 2 0 0 0-3.4 0Z', 'M12 9v4', 'M12 17h.01'],
    failure: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z', 'm9 9 6 6', 'm15 9-6 6'],
    artifact: ['M12 3v12', 'm8 11 4 4 4-4', 'M5 19h14'],
    platform: ['M4 5h16v11H4Z', 'M8 20h8', 'M12 16v4'],
    transport: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z', 'M3 12h18', 'M12 3c2.5 2.5 3.7 5.5 3.7 9S14.5 18.5 12 21c-2.5-2.5-3.7-5.5-3.7-9S9.5 5.5 12 3Z'],
  };
  (paths[kind] || paths.healthy).forEach(pathValue => {
    svg.appendChild(svgElement('path', { d: pathValue }));
  });
  wrapper.appendChild(svg);
  return wrapper;
}

function svgElement(name, attributes = {}) {
  const node = document.createElementNS('http://www.w3.org/2000/svg', name);
  Object.entries(attributes).forEach(([key, value]) => node.setAttribute(key, String(value)));
  return node;
}

function showToast(message, tone = '') {
  const toast = $('app-toast');
  if (!toast) return;
  if (toastTimer !== null) window.clearTimeout(toastTimer);
  toast.textContent = message || '';
  toast.className = 'app-toast' + (tone ? ' toast-' + tone : '');
  toast.hidden = !message;
  if (message) {
    toastTimer = window.setTimeout(() => {
      toast.hidden = true;
      toastTimer = null;
    }, 3600);
  }
}
