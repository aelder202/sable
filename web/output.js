'use strict';

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

function applyOutputSearch() {
  const input = $('output-search');
  const query = input ? input.value.trim().toLowerCase() : '';
  Array.from($('output').children).forEach(child => {
    const matchesType = outputTypeFilter === 'all' || child.dataset.outputType === outputTypeFilter;
    const haystack = child.dataset.searchText || child.textContent.toLowerCase();
    const matchesQuery = !query || haystack.includes(query);
    child.hidden = !(matchesType && matchesQuery);
  });
}

function initOutputTypeMenu() {
  if (!outputTypeFilterEl || !outputTypeButton || !outputTypeList) return;

  outputTypeList.textContent = '';
  Array.from(outputTypeFilterEl.options).forEach(option => {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'output-type-option';
    item.role = 'option';
    item.dataset.value = option.value;
    item.textContent = option.textContent;
    item.addEventListener('click', () => {
      setOutputTypeFilter(option.value, true);
      closeOutputTypeMenu();
      outputTypeButton.focus();
    });
    outputTypeList.appendChild(item);
  });

  outputTypeButton.addEventListener('click', () => {
    setOutputTypeMenuOpen(outputTypeList.hidden);
  });
  outputTypeButton.addEventListener('keydown', event => {
    if (event.key === 'ArrowDown' || event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      setOutputTypeMenuOpen(true);
      focusOutputTypeOption(outputTypeFilter);
      return;
    }
    if (event.key === 'Escape') closeOutputTypeMenu();
  });
  outputTypeList.addEventListener('keydown', event => {
    const options = outputTypeOptions();
    const index = options.indexOf(document.activeElement);
    if (event.key === 'Escape') {
      event.preventDefault();
      closeOutputTypeMenu();
      outputTypeButton.focus();
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
      return;
    }
    if (event.key === 'Home') {
      event.preventDefault();
      options[0]?.focus();
      return;
    }
    if (event.key === 'End') {
      event.preventDefault();
      options[options.length - 1]?.focus();
    }
  });
  document.addEventListener('click', event => {
    if (!outputTypeMenu || outputTypeMenu.contains(event.target)) return;
    closeOutputTypeMenu();
  });

  syncOutputTypeMenu();
}

function setOutputTypeFilter(value, updateSelect) {
  outputTypeFilter = value || 'all';
  if (updateSelect && outputTypeFilterEl) outputTypeFilterEl.value = outputTypeFilter;
  syncOutputTypeMenu();
  applyOutputSearch();
}

function setOutputTypeMenuOpen(isOpen) {
  if (!outputTypeButton || !outputTypeList) return;
  outputTypeButton.setAttribute('aria-expanded', isOpen ? 'true' : 'false');
  outputTypeList.hidden = !isOpen;
}

function closeOutputTypeMenu() {
  setOutputTypeMenuOpen(false);
}

function outputTypeOptions() {
  return Array.from(outputTypeList ? outputTypeList.querySelectorAll('.output-type-option') : []);
}

function focusOutputTypeOption(value) {
  const option = outputTypeOptions().find(item => item.dataset.value === value) || outputTypeOptions()[0];
  if (option) option.focus();
}

function syncOutputTypeMenu() {
  if (!outputTypeFilterEl || !outputTypeButtonLabel) return;
  const option = Array.from(outputTypeFilterEl.options).find(item => item.value === outputTypeFilter)
    || outputTypeFilterEl.options[0];
  outputTypeButtonLabel.textContent = option ? option.textContent : outputTypeFilter;
  outputTypeOptions().forEach(item => {
    const active = item.dataset.value === outputTypeFilter;
    item.setAttribute('aria-selected', active ? 'true' : 'false');
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
    appendOutput(formatErroredTaskOutput(output, short, ts), '', '', 'error');
    renderSessionPanels();
    return;
  }

  if (output.type === 'download' && output.output) {
    const payload = output.output.trim();
    if (!payload) {
      appendOutput('[err ' + short + ' ' + ts + '] invalid download payload');
      return;
    }

    appendDownloadResult(output.task_id, short, ts, payload, historical, output.timestamp);
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
      output.timestamp,
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
    createdAt: now.toISOString(),
  });
  renderSessionPanels();
  if (artifact) openArtifactsPanel(artifact.key);
}

function renderedOutputText() {
  return Array.from($('output').children)
    .map(child => {
      if (child.classList.contains('output-download')) {
        return outputRowText(child);
      }
      return outputRowText(child);
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

function appendOutput(text, cssClass, targetAgentID, outputType) {
  if (targetAgentID && targetAgentID !== activeAgentID) return;

  const shouldScroll = followOutput || isOutputNearBottom();
  const line = document.createElement('div');
  line.className = 'output-line';
  if (cssClass) line.classList.add(cssClass);
  line.dataset.outputType = outputType || inferOutputType(text, cssClass);
  line.dataset.rowID = String(++outputRowSeq);

  const body = document.createElement('div');
  body.className = 'output-line-text';
  body.textContent = text;
  line.appendChild(body);
  appendOutputActions(line);
  line.dataset.searchText = text.toLowerCase();
  $('output').appendChild(line);
  applyOutputSearch();
  if (shouldScroll) scrollOutputToBottom();
  else updateOutputControls();
  updateOutputEmptyState();
}

function inferOutputType(text, cssClass) {
  const value = String(text || '').trim().toLowerCase();
  if (cssClass === 'interactive-out') return 'shell';
  if (value.startsWith('[-]') || value.startsWith('[err')) return 'error';
  if (value.startsWith('[>]')) return 'operator';
  if (value.startsWith('[download]') || value.includes('progress')) return 'progress';
  return 'shell';
}

function formatErroredTaskOutput(output, short, ts) {
  const captured = output.output && output.output.trim()
    ? '[' + short + ' ' + ts + ']\n' + output.output.trimEnd() + '\n'
    : '';
  return captured + '[err ' + short + ' ' + ts + '] ' + output.error;
}

function appendOutputActions(row) {
  const actions = document.createElement('div');
  actions.className = 'output-row-actions';

  const pinButton = document.createElement('button');
  pinButton.type = 'button';
  pinButton.className = 'output-action-btn';
  pinButton.textContent = 'Pin';
  pinButton.addEventListener('click', () => {
    const pinned = !row.classList.contains('pinned');
    row.classList.toggle('pinned', pinned);
    pinButton.textContent = pinned ? 'Unpin' : 'Pin';
  });

  const copyButton = document.createElement('button');
  copyButton.type = 'button';
  copyButton.className = 'output-action-btn';
  copyButton.textContent = 'Copy';
  copyButton.addEventListener('click', async () => {
    const text = outputRowText(row);
    try {
      await navigator.clipboard.writeText(text);
      copyButton.textContent = 'Copied';
      window.setTimeout(() => { copyButton.textContent = 'Copy'; }, 1200);
    } catch (_) {
      appendOutput('[-] copy failed', '', activeAgentID, 'error');
    }
  });

  actions.appendChild(pinButton);
  actions.appendChild(copyButton);
  row.appendChild(actions);
}

function outputRowText(row) {
  const text = row.querySelector('.output-line-text');
  if (text) return text.textContent || '';
  const downloadText = row.querySelector('.output-download-text');
  if (downloadText) return downloadText.textContent || '';
  return row.textContent || '';
}

function renderSessionPanels() {
  updateSessionPanelTabs();
  updateSessionPanelCounts();
  updateCancellationControls();
  renderTimelinePanel();
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

function setSessionPanel(panel) {
  if (!panel || !$(panel + '-panel')) return;
  activeSessionPanel = panel;
  updateSessionPanelTabs();
}

function updateSessionPanelTabs() {
  sessionPanelTabs.forEach(button => {
    const isActive = button.dataset.panel === activeSessionPanel;
    button.classList.toggle('active', isActive);
    button.setAttribute('aria-selected', isActive ? 'true' : 'false');
  });
  if (sessionPanelFilter) sessionPanelFilter.value = activeSessionPanel;

  ['timeline', 'jobs', 'artifacts', 'notes', 'audit'].forEach(panel => {
    const el = $(panel + '-panel');
    if (!el) return;
    const isActive = panel === activeSessionPanel;
    el.hidden = !isActive;
    el.classList.toggle('active', isActive);
  });
}

function updateSessionPanelCounts() {
  const queued = activeAgent && Array.isArray(activeAgent.queued) ? activeAgent.queued.length : 0;
  const running = runningPEASJobs().length;
  const recent = currentOutputs.length;
  $('timeline-count').textContent = String(sessionTimelineEntries().length);
  $('jobs-count').textContent = String(queued + running + recent);
  $('artifacts-count').textContent = String(artifactLibrary.length);
  $('audit-count').textContent = String(sessionAuditEvents().length);
}

function renderTimelinePanel() {
  renderTimelineSummary();

  const list = $('timeline-list');
  if (!list) return;
  list.textContent = '';
  if (!activeAgentID) return;

  const entries = sessionTimelineEntries()
    .sort((a, b) => b.sort - a.sort)
    .slice(0, 80);
  if (!entries.length) {
    list.appendChild(panelText('No timeline events for this session.'));
    return;
  }
  entries.forEach(entry => list.appendChild(timelineItem(entry)));
}

function renderTimelineSummary() {
  const summary = $('timeline-summary');
  if (!summary) return;
  summary.textContent = '';
  if (!activeAgentID) return;

  const queued = activeAgent && Array.isArray(activeAgent.queued) ? activeAgent.queued.length : 0;
  const running = runningPEASJobs().length;
  const state = activeAgent ? getAgentState(activeAgent) : 'offline';
  const stats = [
    { label: 'State', value: getAgentStateLabel(state), tone: 'state-' + state },
    { label: 'Queued', value: String(queued) },
    { label: 'Running', value: String(running), tone: running ? 'state-stale' : '' },
    { label: 'Artifacts', value: String(artifactLibrary.length) },
  ];

  stats.forEach(stat => {
    const item = document.createElement('article');
    item.className = 'timeline-stat';
    const label = document.createElement('span');
    label.textContent = stat.label;
    const value = document.createElement('strong');
    value.textContent = stat.value;
    if (stat.tone) value.classList.add(stat.tone);
    item.appendChild(label);
    item.appendChild(value);
    summary.appendChild(item);
  });
}

function sessionTimelineEntries() {
  if (!activeAgentID) return [];
  const entries = [];
  const now = Date.now();
  const shortID = activeAgentID.slice(0, 8);
  const queued = activeAgent && Array.isArray(activeAgent.queued) ? activeAgent.queued : [];

  runningPEASJobs().forEach((job, index) => {
    entries.push({
      tone: 'running',
      label: 'RUNNING',
      title: 'PEAS background task running',
      detail: job.id.slice(0, 8) + ' can be cancelled from Task Builder.',
      time: 'now',
      sort: now + 1000 - index,
    });
  });

  queued.forEach((job, index) => {
    const queuedAt = timelineDate(job.queued_at);
    entries.push({
      tone: 'queued',
      label: 'QUEUED',
      title: (job.type || 'task') + ' waiting for delivery',
      detail: job.id.slice(0, 8) + timelinePayloadDetail(job.payload),
      time: timelineTimeLabel(queuedAt, 'queued'),
      sort: timelineSortValue(queuedAt, now - 1000 - index),
    });
  });

  currentOutputs.forEach((output, index) => {
    if (!output || !output.task_id) return;
    const timestamp = timelineDate(output.timestamp);
    const failed = Boolean(output.error);
    const progress = String(output.type || '').endsWith('_progress');
    entries.push({
      tone: failed ? 'failed' : progress ? 'progress' : 'done',
      label: failed ? 'FAILED' : progress ? 'PROGRESS' : 'DONE',
      title: timelineTaskTitle(output),
      detail: output.task_id.slice(0, 8) + timelineOutputDetail(output),
      time: timelineTimeLabel(timestamp, ''),
      sort: timelineSortValue(timestamp, now - 5000 - index),
    });
  });

  artifactLibrary.forEach((artifact, index) => {
    if (!artifact || artifact.taskID !== shortID) return;
    const createdAt = timelineDate(artifact.createdAt);
    entries.push({
      tone: 'artifact',
      label: 'ARTIFACT',
      title: artifact.label || 'artifact ready',
      detail: artifact.filename || artifact.archiveFilename || 'saveable result',
      time: artifact.timestamp || timelineTimeLabel(createdAt, ''),
      sort: timelineSortValue(createdAt, now - 10000 - index),
    });
  });

  sessionAuditEvents().forEach((event, index) => {
    const timestamp = timelineDate(event.timestamp);
    entries.push({
      tone: 'audit',
      label: 'AUDIT',
      title: timelineAuditTitle(event.action),
      detail: event.detail || '',
      time: timelineTimeLabel(timestamp, ''),
      sort: timelineSortValue(timestamp, now - 20000 - index),
    });
  });

  return entries;
}

function timelineItem(entry) {
  const item = document.createElement('article');
  item.className = 'timeline-item timeline-' + (entry.tone || 'event');

  const rail = document.createElement('div');
  rail.className = 'timeline-marker';

  const body = document.createElement('div');
  body.className = 'timeline-body';

  const head = document.createElement('div');
  head.className = 'timeline-head';
  const label = document.createElement('span');
  label.className = 'timeline-label';
  label.textContent = entry.label || 'EVENT';
  const time = document.createElement('time');
  time.textContent = entry.time || '';
  head.appendChild(label);
  head.appendChild(time);

  const title = document.createElement('strong');
  title.textContent = entry.title || 'Session event';
  body.appendChild(head);
  body.appendChild(title);
  if (entry.detail) {
    const detail = document.createElement('p');
    detail.textContent = entry.detail;
    body.appendChild(detail);
  }

  item.appendChild(rail);
  item.appendChild(body);
  return item;
}

function timelineDate(value) {
  if (!value) return null;
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date : null;
}

function timelineSortValue(date, fallback) {
  return date ? date.getTime() : fallback;
}

function timelineTimeLabel(date, fallback) {
  return date ? date.toLocaleTimeString() : fallback;
}

function timelinePayloadDetail(payload) {
  const text = String(payload || '').trim();
  if (!text) return '';
  return ' - ' + (text.length > 90 ? text.slice(0, 87) + '...' : text);
}

function timelineOutputDetail(output) {
  if (output.error) return ' - ' + output.error;
  const text = String(output.output || '').trim().replace(/\s+/g, ' ');
  if (!text) return '';
  return ' - ' + (text.length > 110 ? text.slice(0, 107) + '...' : text);
}

function timelineTaskTitle(output) {
  const type = String(output.type || 'task');
  if (type === 'peas_progress') return 'PEAS progress update';
  if (type === 'download_progress') return 'Download progress update';
  if (type === 'snapshot') return 'Host info artifact ready';
  if (type === 'screenshot') return 'Screenshot artifact ready';
  return type + ' result received';
}

function timelineAuditTitle(action) {
  return String(action || 'audit event').replace(/_/g, ' ');
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

function renderAuditList() {
  const list = $('audit-list');
  if (!list) return;
  list.textContent = '';
  const rows = sessionAuditEvents().slice(-20).reverse();
  if (!rows.length) {
    list.appendChild(panelText('No audit events loaded.'));
    return;
  }
  rows.forEach(event => {
    const time = event.timestamp ? new Date(event.timestamp).toLocaleTimeString() : '';
    list.appendChild(panelItem(time + ' ' + event.action, event.detail || event.agent_id || ''));
  });
}

function sessionAuditEvents() {
  if (!activeAgentID) return [];
  return auditLog.filter(event => event && event.agent_id === activeAgentID);
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
