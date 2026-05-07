'use strict';

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
