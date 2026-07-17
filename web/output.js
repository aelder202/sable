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
  const followToggle = $('output-follow-toggle');
  if (followToggle) followToggle.checked = followOutput;
}

function scrollOutputToBottom() {
  outputEl.scrollTop = outputEl.scrollHeight;
  followOutput = true;
  updateOutputControls();
}

function scrollOutputToTask(card) {
  if (!card) return;
  outputEl.scrollTop = Math.max(0, card.offsetTop);
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
    outputEmptyTitle.textContent = 'No agent selected';
    outputEmptyText.textContent = 'Choose an agent to inspect activity and queue tasks. Output is rendered as plain text only.';
    empty.hidden = false;
    return;
  }

  if ($('output').childElementCount === 0) {
    outputEmptyTitle.textContent = 'No task output yet';
    outputEmptyText.textContent = 'This agent has not returned task output during the current view.';
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

  if (output.type === 'download_progress' || output.type === 'archive_progress') {
    const baseID = baseTransferTaskID(output.task_id);
    const progress = parseTransferProgress(output.output);
    let state = downloadTasks.get(baseID);
    if (!state && progress.path) {
      state = {
        taskID: baseID,
        agentID: activeAgentID,
        kind: progress.kind === 'archive' ? 'archive' : 'file',
        path: progress.path,
        paths: [progress.path],
        filename: basenameFromPath(progress.path),
        status: 'progress',
        artifactKey: '',
        progress: null,
      };
      downloadTasks.set(baseID, state);
    }
    if (state) {
      state.status = 'progress';
      state.progress = progress;
      if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
    }
    if (progress.message) appendOutput('[transfer] ' + progress.message, '', '', 'progress');
    renderSessionPanels();
    return;
  }

  if (output.type === 'ls') {
    handleFileBrowserOutput(output);
    renderSessionPanels();
    return;
  }

  if (output.error) {
    if (output.type === 'download' || output.type === 'download_archive') {
      const transfer = downloadTasks.get(output.task_id);
      if (transfer) transfer.status = /cancelled/i.test(output.error) ? 'cancelled' : 'failed';
      if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
    }
    appendTaskResultCard(output, historical);
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

  if (output.type === 'download_archive' && output.output) {
    appendArchiveResult(output.task_id, short, ts, output.output.trim(), historical, output.timestamp);
    renderSessionPanels();
    return;
  }

  if ((output.type === 'screenshot' || output.type === 'peas' || output.type === 'snapshot') && output.output) {
    appendArtifactResult(
	  output.task_id,
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
      appendTaskResultCard(output, historical);
    }
  }
  renderSessionPanels();
}

function parseTransferProgress(value) {
  const fallback = String(value || '').trim();
  try {
    const parsed = JSON.parse(fallback || '{}');
    if (parsed && typeof parsed === 'object') {
      return {
        kind: parsed.kind || 'file',
        phase: parsed.phase || 'transferring',
        path: parsed.path || '',
        files: Number(parsed.files || 0),
        bytes: Number(parsed.bytes || 0),
        totalBytes: Number(parsed.total_bytes || 0),
        archiveBytes: Number(parsed.archive_bytes || 0),
        message: parsed.message || fallback,
      };
    }
  } catch (_) {
    // Older agents returned a plain-text progress line.
  }
  return { kind: 'file', phase: 'transferring', path: '', files: 0, bytes: 0, totalBytes: 0, archiveBytes: 0, message: fallback };
}

async function loadAudit() {
  if (!token) return;
  try {
    const data = await apiFetchAll('/api/audit');
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
  const filename = 'agent-output-' + sanitizeArchiveEntryName(host) + '-' + stamp + '.txt';
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

const UI_ICON_PATHS = {
  arrow: ['M5 12h14', 'M13 6l6 6-6 6'],
  chevron: ['m8 10 4 4 4-4'],
  identity: ['M4 4h16v16H4z', 'M8 8h8', 'M8 12h5', 'M8 16h7'],
  connection: ['M5 12a7 7 0 0 1 14 0', 'M8 15a4 4 0 0 1 8 0', 'M12 18h.01'],
  activity: ['M3 12h4l2-6 4 12 2-6h6'],
  artifact: ['M6 3h8l4 4v14H6z', 'M14 3v5h5'],
  folder: ['M3 6h7l2 2h9v11H3z'],
  check: ['m5 12 4 4L19 6'],
};

function createUIIcon(name, className) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  if (className) svg.setAttribute('class', className);
  (UI_ICON_PATHS[name] || UI_ICON_PATHS.activity).forEach(value => {
    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', value);
    svg.appendChild(path);
  });
  return svg;
}

function appendTaskResultCard(output, historical) {
  if (!output || !output.task_id) return;
  rememberTaskMetadata(output.task_id, output, activeAgentID);
  const shouldScroll = followOutput || isOutputNearBottom();
  const failed = Boolean(output.error);
  const card = document.createElement('article');
  card.className = 'output-task-card' + (failed ? ' failed' : ' success');
  card.dataset.outputType = failed ? 'error' : 'shell';
  card.dataset.outputTaskType = output.type || 'task';
  card.dataset.taskId = output.task_id;
  card.dataset.completedAt = output.timestamp || '';
  card.dataset.rowID = String(++outputRowSeq);

  const header = document.createElement('header');
  header.className = 'output-task-header';

  const time = document.createElement('time');
  time.className = 'output-task-time';
  time.dateTime = output.timestamp || '';
  time.textContent = resultTimeLabel(output.timestamp);

  const marker = document.createElement('span');
  marker.className = 'output-task-marker';
  marker.appendChild(createUIIcon('arrow'));

  const heading = document.createElement('div');
  heading.className = 'output-task-heading';
  const command = document.createElement('strong');
  command.className = 'output-task-command';
  const identifier = document.createElement('span');
  identifier.className = 'output-task-id';
  identifier.textContent = output.task_id.slice(0, 8);
  heading.appendChild(command);
  heading.appendChild(identifier);

  const status = document.createElement('span');
  status.className = 'output-task-status ' + (failed ? 'failed' : 'success');
  const statusDot = document.createElement('span');
  statusDot.className = 'output-task-status-dot';
  status.appendChild(statusDot);
  status.appendChild(document.createTextNode(failed ? 'Failed' : 'Success'));

  const duration = document.createElement('span');
  duration.className = 'output-task-duration';

  const bodyWrap = document.createElement('div');
  bodyWrap.className = 'output-task-body-wrap';
  bodyWrap.id = 'output-task-body-' + card.dataset.rowID;
  const body = document.createElement('pre');
  body.className = 'output-task-body';
  const bodyText = taskResultBody(output);
  body.textContent = bodyText;
  bodyWrap.appendChild(body);

  const toggle = document.createElement('button');
  toggle.type = 'button';
  toggle.className = 'output-task-toggle';
  toggle.setAttribute('aria-expanded', 'true');
  toggle.setAttribute('aria-controls', bodyWrap.id);
  toggle.title = 'Collapse task output';
  toggle.appendChild(createUIIcon('chevron'));
  toggle.addEventListener('click', () => {
    const collapsed = card.classList.toggle('collapsed');
    toggle.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
    toggle.title = collapsed ? 'Expand task output' : 'Collapse task output';
  });

  header.appendChild(time);
  header.appendChild(marker);
  header.appendChild(heading);
  header.appendChild(status);
  header.appendChild(duration);
  header.appendChild(toggle);
  card.appendChild(header);
  card.appendChild(bodyWrap);

  const lineCount = bodyText ? bodyText.split(/\r?\n/).length : 0;
  if (lineCount > 12 || bodyText.length > 1500) {
    body.classList.add('is-truncated');
    const showMore = document.createElement('button');
    showMore.type = 'button';
    showMore.className = 'output-task-more';
    showMore.textContent = 'Show more' + (lineCount > 12 ? ' (' + lineCount + ' lines)' : '');
    showMore.addEventListener('click', () => {
      const expanded = body.classList.toggle('is-expanded');
      showMore.textContent = expanded
        ? 'Show less'
        : 'Show more' + (lineCount > 12 ? ' (' + lineCount + ' lines)' : '');
    });
    bodyWrap.appendChild(showMore);
  }

  if (historical) card.classList.add('historical');
  applyTaskCardMetadata(card);
  appendOutputActions(card);
  card.dataset.searchText = [
    card.querySelector('.output-task-command').textContent,
    output.type,
    output.task_id,
    failed ? 'failed' : 'success',
    bodyText,
  ].join(' ').toLowerCase();
  $('output').appendChild(card);
  applyOutputSearch();
  if (shouldScroll) scrollOutputToTask(card);
  else updateOutputControls();
  updateOutputEmptyState();
}

function taskResultBody(output) {
  const captured = String(output.output || '').trimEnd();
  const error = String(output.error || '').trim();
  if (captured && error) return captured + '\n\nError: ' + error;
  if (error) return 'Error: ' + error;
  return captured || 'Task completed without text output.';
}

function resultTimeLabel(value) {
  const date = timelineDate(value);
  if (!date) return '--:--:--';
  return date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit', second: '2-digit' });
}

function taskResultPresentation(taskID, fallbackType, completedAt) {
  const metadata = taskMetadataByID.get(taskID) || {};
  const type = metadata.type || fallbackType || 'task';
  const payload = String(metadata.payload || '').trim().replace(/\s+/g, ' ');
  const labels = {
    shell: 'Shell command',
    ps: 'Process listing',
    screenshot: 'Screenshot',
    snapshot: 'Host information',
    persistence: 'Persistence check',
    peas: 'PEAS assessment',
    download: 'File download',
    download_archive: 'Directory archive',
    upload: 'File upload',
    sleep: 'Sleep interval update',
    kill: 'Agent termination',
    interactive: 'Interactive shell',
  };
  const payloadTypes = new Set(['shell', 'download', 'upload', 'sleep']);
  const title = payload && payloadTypes.has(type)
    ? (payload.length > 100 ? payload.slice(0, 97) + '...' : payload)
    : (labels[type] || String(type).replace(/_/g, ' '));

  const completed = timelineDate(completedAt);
  const deliveredCandidate = timelineDate(metadata.deliveredAt);
  const queuedCandidate = timelineDate(metadata.queuedAt);
  const delivered = deliveredCandidate && deliveredCandidate.getFullYear() >= 2000 ? deliveredCandidate : null;
  const queued = queuedCandidate && queuedCandidate.getFullYear() >= 2000 ? queuedCandidate : null;
  const started = delivered || queued;
  const elapsed = completed && started ? completed.getTime() - started.getTime() : NaN;
  return {
    title,
    duration: Number.isFinite(elapsed) && elapsed >= 0 ? formatTaskDuration(elapsed) : '—',
    durationTitle: delivered ? 'Response time after task delivery' : queued ? 'Elapsed since task queued' : 'Response time unavailable',
  };
}

function formatTaskDuration(milliseconds) {
  if (milliseconds < 1000) return Math.max(1, Math.round(milliseconds)) + 'ms';
  if (milliseconds < 10000) return (milliseconds / 1000).toFixed(1) + 's';
  if (milliseconds < 60000) return Math.round(milliseconds / 1000) + 's';
  const minutes = Math.floor(milliseconds / 60000);
  const seconds = Math.round((milliseconds % 60000) / 1000);
  return minutes + 'm ' + seconds + 's';
}

function applyTaskCardMetadata(card) {
  if (!card) return;
  const presentation = taskResultPresentation(
    card.dataset.taskId,
    card.dataset.outputTaskType,
    card.dataset.completedAt,
  );
  const command = card.querySelector('.output-task-command');
  const duration = card.querySelector('.output-task-duration');
  if (command) command.textContent = presentation.title;
  if (duration) {
    duration.textContent = presentation.duration;
    duration.hidden = false;
    duration.title = presentation.durationTitle;
  }
}

function enrichRenderedTaskCard(taskID) {
  Array.from($('output').querySelectorAll('.output-task-card')).forEach(card => {
    if (card.dataset.taskId !== taskID) return;
    applyTaskCardMetadata(card);
    const body = outputRowText(card);
    card.dataset.searchText = [card.textContent, body].join(' ').toLowerCase();
  });
  applyOutputSearch();
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
  const taskBody = row.querySelector('.output-task-body');
  if (taskBody) return taskBody.textContent || '';
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
  renderDetailsFilePanel();
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
  return runningPEASJobs().concat(runningTransferJobs());
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

  ['timeline', 'activity', 'jobs', 'artifacts', 'files', 'notes', 'audit'].forEach(panel => {
    const el = $(panel + '-panel');
    if (!el) return;
    const isActive = panel === activeSessionPanel;
    el.hidden = !isActive;
    el.classList.toggle('active', isActive);
  });
}

function updateSessionPanelCounts() {
  const queued = activeAgent && Array.isArray(activeAgent.queued) ? activeAgent.queued.length : 0;
  const running = cancellableTasks().length;
  const recent = currentOutputs.length;
  $('timeline-count').textContent = String(sessionTimelineEntries().length);
  $('jobs-count').textContent = String(queued + running + recent);
  $('artifacts-count').textContent = String(artifactLibrary.length);
  $('files-count').textContent = String(runningTransferJobs().length);
  $('audit-count').textContent = String(sessionAuditEvents().length);
}

function renderTimelinePanel() {
  renderTimelineSummary();
  renderActivityScheduleSummary();

  const list = $('timeline-list');
  if (!list) return;
  list.textContent = '';
  if (!activeAgentID) return;

  const entries = sessionTimelineEntries()
    .sort((a, b) => b.sort - a.sort)
    .slice(0, 80);
  if (!entries.length) {
    list.appendChild(panelText('No timeline events for this agent.'));
    return;
  }
  entries.forEach(entry => list.appendChild(timelineItem(entry)));
}

function renderActivityScheduleSummary() {
  const container = $('activity-schedule-summary');
  if (!container) return;
  container.textContent = '';
  if (!activeAgent) return;
  [
    ['Expected next check-in', summaryDateLabel(activeAgent.expected_next_seen, 'Not scheduled')],
    ['Beacon interval', activeAgent.sleep_seconds ? activeAgent.sleep_seconds + ' seconds' : 'Not reported'],
  ].forEach(([label, value]) => {
    const item = document.createElement('div');
    const term = document.createElement('span');
    const detail = document.createElement('strong');
    term.textContent = label;
    detail.textContent = value;
    item.appendChild(term);
    item.appendChild(detail);
    container.appendChild(item);
  });
}

function renderTimelineSummary() {
  const summary = $('timeline-summary');
  if (!summary) return;
  summary.textContent = '';
  if (!activeAgentID) return;

  const state = activeAgent ? getAgentState(activeAgent) : 'offline';
  const identity = summaryDetailSection('Identity', 'identity');
  identity.body.appendChild(summaryDetailRow('Agent ID', activeAgent.id || 'Not reported', true));
  identity.body.appendChild(summaryDetailRow('Hostname', activeAgent.hostname || 'Not reported'));
  identity.body.appendChild(summaryDetailRow('Platform', (activeAgent.os || 'unknown') + ' / ' + (activeAgent.arch || 'unknown')));
  identity.body.appendChild(summaryDetailRow('First seen', summaryDateLabel(activeAgent.first_seen || activeAgent.registered_at)));
  if (Array.isArray(activeAgent.tags) && activeAgent.tags.length) {
    identity.body.appendChild(summaryDetailRow('Tags', activeAgent.tags.join(', ')));
  }
  summary.appendChild(identity.section);

  const connection = summaryDetailSection('Connection', 'connection');
  connection.body.appendChild(summaryDetailRow('Transport', activeAgent.transport ? String(activeAgent.transport).toUpperCase() : 'Not reported'));
  const connected = state === 'on_schedule';
  connection.body.appendChild(summaryStatusRow('Status', connected ? 'Connected' : 'Offline', connected ? 'on_schedule' : 'offline'));
  connection.body.appendChild(summaryDetailRow('Last check-in', formatLastSeenCompact(activeAgent)));
  connection.body.appendChild(summaryDetailRow('IP address', activeAgent.host_ip || 'Not reported', true));
  summary.appendChild(connection.section);

  const cutoff = Date.now() - 24 * 60 * 60 * 1000;
  const recentResults = currentOutputs.filter(output => {
    if (!output || !output.task_id || String(output.type || '').endsWith('_progress')) return false;
    if (['pathbrowse', 'complete', 'ls', 'interactive'].includes(output.type)) return false;
    const timestamp = timelineDate(output.timestamp);
    return !timestamp || timestamp.getTime() >= cutoff;
  });
  const activity = summaryDetailSection('Task activity (24h)', 'activity');
  const activityGrid = document.createElement('div');
  activityGrid.className = 'summary-activity-grid';
  [
    { label: 'Commands', value: recentResults.length },
    { label: 'Success', value: recentResults.filter(output => !output.error).length, tone: 'success' },
    { label: 'Failed', value: recentResults.filter(output => output.error).length, tone: 'failed' },
  ].forEach(stat => {
    const item = document.createElement('article');
    item.className = 'summary-activity-stat' + (stat.tone ? ' ' + stat.tone : '');
    const label = document.createElement('span');
    label.textContent = stat.label;
    const value = document.createElement('strong');
    value.textContent = String(stat.value);
    item.appendChild(label);
    item.appendChild(value);
    activityGrid.appendChild(item);
  });
  activity.body.appendChild(activityGrid);
  summary.appendChild(activity.section);

  const artifacts = summaryDetailSection('Recent artifacts', 'artifact');
  const viewAll = document.createElement('button');
  viewAll.type = 'button';
  viewAll.className = 'summary-link-button';
  viewAll.textContent = 'View all';
  viewAll.disabled = artifactLibrary.length === 0;
  viewAll.addEventListener('click', () => setSessionPanel('artifacts'));
  artifacts.header.appendChild(viewAll);
  const recentArtifacts = artifactLibrary.slice(0, 2);
  if (!recentArtifacts.length) {
    artifacts.body.appendChild(panelText('No artifacts captured for this agent.'));
  } else {
    recentArtifacts.forEach(artifact => artifacts.body.appendChild(summaryArtifactRow(artifact)));
  }
  summary.appendChild(artifacts.section);
}

function summaryDetailSection(title, iconName) {
  const section = document.createElement('section');
  section.className = 'summary-detail-section';
  const header = document.createElement('header');
  header.className = 'summary-detail-heading';
  const heading = document.createElement('h5');
  const icon = document.createElement('span');
  icon.className = 'summary-detail-icon';
  icon.appendChild(createUIIcon(iconName));
  heading.appendChild(icon);
  heading.appendChild(document.createTextNode(title));
  header.appendChild(heading);
  const body = document.createElement('div');
  body.className = 'summary-detail-body';
  section.appendChild(header);
  section.appendChild(body);
  return { section, header, body };
}

function summaryDetailRow(labelText, valueText, mono) {
  const row = document.createElement('div');
  row.className = 'summary-detail-row';
  const label = document.createElement('span');
  label.textContent = labelText;
  const value = document.createElement('strong');
  value.textContent = valueText || 'Not reported';
  if (mono) value.classList.add('mono');
  row.appendChild(label);
  row.appendChild(value);
  return row;
}

function summaryStatusRow(labelText, valueText, state) {
  const row = summaryDetailRow(labelText, valueText);
  const value = row.querySelector('strong');
  value.classList.add('summary-connection-status', 'state-' + state);
  const dot = document.createElement('span');
  dot.className = 'summary-connection-dot';
  value.prepend(dot);
  return row;
}

function summaryDateLabel(value, fallback) {
  const date = timelineDate(value);
  return date ? date.toLocaleString() : (fallback || 'Not reported');
}

function summaryArtifactRow(artifact) {
  const row = document.createElement('article');
  row.className = 'summary-artifact-row';
  const icon = document.createElement('span');
  icon.className = 'summary-artifact-icon';
  icon.appendChild(createUIIcon('artifact'));
  const copy = document.createElement('span');
  copy.className = 'summary-artifact-copy';
  const name = document.createElement('strong');
  name.textContent = artifact.filename || 'Artifact';
  const meta = document.createElement('span');
  const approximateBytes = Number(artifact.sizeBytes) || (artifact.base64Value ? Math.floor(String(artifact.base64Value).length * 3 / 4) : NaN);
  const parts = [];
  if (Number.isFinite(approximateBytes)) parts.push(formatFileSize(approximateBytes));
  if (artifact.createdAt) parts.push(resultTimeLabel(artifact.createdAt));
  meta.textContent = parts.join('  ·  ') || artifact.label || 'Ready';
  copy.appendChild(name);
  copy.appendChild(meta);
  const open = document.createElement('button');
  open.type = 'button';
  open.textContent = 'Open';
  open.addEventListener('click', () => openArtifactsPanel(artifact.key));
  row.appendChild(icon);
  row.appendChild(copy);
  row.appendChild(open);
  return row;
}

function renderDetailsFilePanel() {
  const list = $('details-transfer-list');
  const openButton = $('details-open-files-btn');
  if (!list || !openButton) return;
  list.textContent = '';
  openButton.disabled = !activeAgentID || taskRequestInFlight;
  const transfers = Array.from(downloadTasks.values()).filter(transfer => !transfer.agentID || transfer.agentID === activeAgentID);
  if (!transfers.length) {
    list.appendChild(panelText('No file transfers in this view.'));
    return;
  }
  transfers.slice(-8).reverse().forEach(transfer => {
    const status = String(transfer.status || 'queued').replace(/_/g, ' ');
    const name = transfer.filename || transfer.path || transfer.taskID.slice(0, 8);
    list.appendChild(panelItem(name, status));
  });
}

function sessionTimelineEntries() {
  if (!activeAgentID) return [];
  const entries = [];
  const now = Date.now();
  const queued = activeAgent && Array.isArray(activeAgent.queued) ? activeAgent.queued : [];

  cancellableTasks().forEach((job, index) => {
    entries.push({
      tone: 'running',
      label: 'RUNNING',
      title: job.label + ' background task running',
      detail: job.id.slice(0, 8) + ' can be cancelled from Task Builder.',
      time: 'now',
      sort: now + 1000 - index,
    });
  });

  queued.forEach((job, index) => {
    const queuedAt = timelineDate(job.queued_at);
	const inFlight = job.status === 'in_flight';
	const attempts = Number(job.delivery_attempts || 0);
    entries.push({
      tone: 'queued',
	  label: SableLogic.deliveryLabel(job),
	  title: (job.type || 'task') + (inFlight ? ' awaiting acknowledgment' : ' waiting for delivery'),
	  detail: job.id.slice(0, 8) + (attempts ? ' - attempt ' + attempts : '') + timelinePayloadDetail(job.payload),
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
	if (!artifact) return;
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
  title.textContent = entry.title || 'Agent event';
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
  const type = String(output.type || '');
  if (['download', 'download_archive', 'screenshot', 'peas', 'snapshot'].includes(type) && output.output) {
    return ' - saveable artifact ready';
  }
  if (type.endsWith('_progress')) {
    const progress = parseTransferProgress(output.output);
    return progress.message ? ' - ' + progress.message : '';
  }
  const text = String(output.output || '').trim().replace(/\s+/g, ' ');
  if (!text) return '';
  return ' - ' + (text.length > 110 ? text.slice(0, 107) + '...' : text);
}

function timelineTaskTitle(output) {
  const type = String(output.type || 'task');
  if (type === 'peas_progress') return 'PEAS progress update';
  if (type === 'download_progress') return 'Download progress update';
  if (type === 'archive_progress') return 'Archive progress update';
  if (type === 'download_archive') return 'Directory archive ready';
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
  const running = cancellableTasks();
  if (!queued.length && !running.length && !currentOutputs.length) {
    list.appendChild(panelText('No jobs for this agent.'));
    return;
  }

  running.forEach(job => {
    const row = panelItem('RUNNING', job.id.slice(0, 8) + ' ' + job.label);
    row.appendChild(panelHint('Cancel from Task Builder.'));
    list.appendChild(row);
  });

  queued.forEach(job => {
	const status = SableLogic.deliveryLabel(job);
	const row = panelItem(status, job.id.slice(0, 8) + ' ' + job.type);
	const attempts = Number(job.delivery_attempts || 0);
	row.appendChild(panelHint(attempts > 0 ? attempts + ' delivery attempt' + (attempts === 1 ? '' : 's') : 'Waiting for first delivery'));
    const button = panelButton('Remove', () => deleteQueuedTask(job.id));
    row.appendChild(button);
    list.appendChild(row);
  });

  currentOutputs.slice(-8).reverse().forEach(output => {
    if (!output || !output.task_id) return;
    const status = output.error ? 'FAILED' : String(output.type || '').endsWith('_progress') ? 'PROGRESS' : 'DONE';
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

function runningTransferJobs() {
  const jobs = [];
  for (const transfer of downloadTasks.values()) {
    if (transfer.agentID && transfer.agentID !== activeAgentID) continue;
    if (!['progress', 'cancelling'].includes(transfer.status)) continue;
    jobs.push({
      id: transfer.taskID,
      label: transfer.kind === 'archive' ? 'Archive' : 'Download',
    });
  }
  return jobs;
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
