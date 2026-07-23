'use strict';

function initDraggableModals() {
  [$('file-browser-panel')].forEach(panel => {
    if (!panel) return;
    const header = panel.querySelector('.rail-header');
    if (!header) return;
    let dragging = false;
    let offsetX = 0;
    let offsetY = 0;

    header.addEventListener('pointerdown', event => {
      if (event.target.closest('button, select, input, a')) return;
      dragging = true;
      if (!panel.classList.contains('panel-dragged')) {
        const rect = panel.getBoundingClientRect();
        panel.style.position = 'absolute';
        panel.style.margin = '0';
        panel.style.left = rect.left + 'px';
        panel.style.top = rect.top + 'px';
        panel.classList.add('panel-dragged');
      }
      const rect = panel.getBoundingClientRect();
      offsetX = event.clientX - rect.left;
      offsetY = event.clientY - rect.top;
      header.setPointerCapture?.(event.pointerId);
      event.preventDefault();
    });

    header.addEventListener('pointermove', event => {
      if (!dragging) return;
      positionPanelWithinViewport(panel, event.clientX - offsetX, event.clientY - offsetY);
    });
    const stop = event => {
      if (!dragging) return;
      dragging = false;
      try { header.releasePointerCapture?.(event.pointerId); } catch (_) { /* no-op */ }
    };
    header.addEventListener('pointerup', stop);
    header.addEventListener('pointercancel', stop);
  });

  window.addEventListener('resize', () => {
    document.querySelectorAll('.panel-dragged').forEach(panel => {
      const rect = panel.getBoundingClientRect();
      positionPanelWithinViewport(panel, rect.left, rect.top);
    });
  });
}

function positionPanelWithinViewport(panel, left, top) {
  const rect = panel.getBoundingClientRect();
  const maxLeft = Math.max(8, window.innerWidth - Math.min(rect.width, window.innerWidth - 16) - 8);
  const maxTop = Math.max(8, window.innerHeight - Math.min(rect.height, window.innerHeight - 16) - 8);
  panel.style.left = Math.min(Math.max(8, left), maxLeft) + 'px';
  panel.style.top = Math.min(Math.max(8, top), maxTop) + 'px';
}

function rememberModalFocus(modalID) {
  modalFocusOrigins.set(modalID, document.activeElement);
}

function restoreModalFocus(modalID) {
  const origin = modalFocusOrigins.get(modalID);
  modalFocusOrigins.delete(modalID);
  if (origin && origin.isConnected && typeof origin.focus === 'function') origin.focus();
}

function visibleModal() {
  const ids = [
    'action-confirm-modal', 'kill-confirm-modal', 'clear-confirm-modal',
    'file-browser-modal', 'edit-info-modal', 'active-jobs-modal', 'session-details-modal',
  ];
  return ids.map(id => $(id)).find(modal => modal && !modal.hidden) || null;
}

function trapModalFocus(event) {
  const modal = visibleModal();
  if (!modal) return false;
  const focusable = Array.from(modal.querySelectorAll(
    'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
  )).filter(element => !element.hidden && element.getClientRects().length > 0);
  if (!focusable.length) {
    event.preventDefault();
    return true;
  }
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && (document.activeElement === first || !modal.contains(document.activeElement))) {
    event.preventDefault();
    last.focus();
    return true;
  }
  if (!event.shiftKey && (document.activeElement === last || !modal.contains(document.activeElement))) {
    event.preventDefault();
    first.focus();
    return true;
  }
  return false;
}

function openActionConfirm(options) {
  const config = options || {};
  rememberModalFocus('action-confirm-modal');
  actionConfirmOrigin = document.activeElement;
  actionConfirmCallback = typeof config.onConfirm === 'function' ? config.onConfirm : null;
  $('action-confirm-title').textContent = config.title || 'Confirm Action';
  $('action-confirm-copy').textContent = config.copy || 'Continue with this action?';
  const button = $('action-confirm-btn');
  button.textContent = config.confirmLabel || 'Confirm';
  button.classList.toggle('danger-button', Boolean(config.danger));
  button.disabled = false;
  const preference = $('action-confirm-preference');
  preference.hidden = !config.showPreference;
  $('action-confirm-preference-label').textContent = config.preferenceLabel || 'Show this warning next time';
  $('action-confirm-show-next').checked = config.preferenceChecked !== false;
  $('action-confirm-modal').hidden = false;
  window.requestAnimationFrame(() => $('action-confirm-cancel-btn').focus());
}

function closeActionConfirm(restoreFocus = true) {
  const modal = $('action-confirm-modal');
  if (!modal) return;
  modal.hidden = true;
  actionConfirmCallback = null;
  actionConfirmOrigin = null;
  $('action-confirm-btn').disabled = false;
  $('action-confirm-preference').hidden = true;
  if (restoreFocus) restoreModalFocus('action-confirm-modal');
  else modalFocusOrigins.delete('action-confirm-modal');
}

async function confirmAction() {
  const callback = actionConfirmCallback;
  if (!callback) {
    closeActionConfirm();
    return;
  }
  const button = $('action-confirm-btn');
  button.disabled = true;
  try {
    await callback();
    closeActionConfirm();
  } catch (err) {
    button.disabled = false;
    showToast(err.message || 'Action failed.', 'error');
  }
}

function createFileBrowserState(agentID) {
  return {
    agentID,
    path: '',
    result: null,
    homePath: '',
    history: [],
    historyIndex: -1,
    cache: new Map(),
    selection: new Set(),
    filter: '',
    sortKey: 'name',
    sortDirection: 'asc',
    pending: new Map(),
    requestSeq: 0,
    activeSeq: 0,
    loading: false,
    error: '',
    scrollByPath: new Map(),
    focusedPath: '',
  };
}

function getFileBrowserState(agentID, create = true) {
  if (!agentID) return null;
  let state = fileBrowserStates.get(agentID);
  if (!state && create) {
    state = createFileBrowserState(agentID);
    fileBrowserStates.set(agentID, state);
  }
  return state || null;
}

function activeFileBrowserState(create = true) {
  return getFileBrowserState(activeAgentID, create);
}

function activateFileBrowserState(agentID) {
  const state = getFileBrowserState(agentID, true);
  fileBrowserPath = state.path;
  fileBrowserResult = state.result;
}

function persistActiveFileBrowserState() {
  const state = getFileBrowserState(activeAgentID, false);
  if (!state) return;
  state.path = fileBrowserPath || state.path;
  state.result = fileBrowserResult || state.result;
  const table = $('file-browser') && $('file-browser').querySelector('.file-browser-table');
  if (table && state.path) state.scrollByPath.set(state.path, table.scrollTop);
}

function handleFileBrowserOutput(output) {
  let state = null;
  let request = null;
  for (const candidate of fileBrowserStates.values()) {
    if (candidate.pending.has(output.task_id)) {
      state = candidate;
      request = candidate.pending.get(output.task_id);
      break;
    }
  }
  if (!state || !request) {
    if (fileBrowserSubmissionCount > 0) deferredFileBrowserOutputs.set(output.task_id, output);
    return true;
  }

  state.pending.delete(output.task_id);
  if (request.seq === state.activeSeq && !Array.from(state.pending.values()).some(item => item.seq === request.seq)) {
    state.loading = false;
  }
  if (output.error) {
    if (request.seq === state.activeSeq) {
      state.error = output.error;
      if (state.agentID === activeAgentID && !$('file-browser-modal').hidden) renderFileBrowser(state.result);
    }
    return true;
  }

  let result;
  try {
    result = JSON.parse(output.output || '{}');
  } catch (_) {
    state.error = 'The agent returned an invalid directory response.';
    if (state.agentID === activeAgentID && !$('file-browser-modal').hidden) renderFileBrowser(state.result);
    return true;
  }
  if (!result || !Array.isArray(result.entries) || typeof result.path !== 'string') return true;

  const previous = state.cache.get(result.path) || state.cache.get(request.path) || null;
  result = SableLogic.mergeDirectoryPage(previous, result);
  result.entries = dedupeBrowserEntries(result.entries);
  state.cache.set(result.path, result);
  state.cache.set(request.path, result);
  if (request.seq !== state.activeSeq) return true;

  state.error = '';
  state.path = result.path;
  state.result = result;
  if (!state.homePath) state.homePath = result.path;
  if (state.historyIndex >= 0) state.history[state.historyIndex] = result.path;
  if (state.agentID === activeAgentID) {
    fileBrowserPath = result.path;
    fileBrowserResult = result;
    if (!$('file-browser-modal').hidden) renderFileBrowser(result);
  }
  return true;
}

function dedupeBrowserEntries(entries) {
  const seen = new Set();
  return entries.filter(entry => {
    const key = entry && entry.path ? entry.path : JSON.stringify(entry);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function renderFileBrowserPlaceholder() {
  renderFileBrowser(null);
}

function renderFileBrowserLoading(path) {
  const state = activeFileBrowserState();
  state.path = path;
  state.loading = true;
  renderFileBrowser(state.result);
}

function renderFileBrowserError(message) {
  const state = activeFileBrowserState();
  state.loading = false;
  state.error = message || 'Unable to browse this directory.';
  renderFileBrowser(state.result);
}

function hideFileBrowser() {
  const panel = $('file-browser');
  if (panel) panel.textContent = '';
}

function openFileBrowserModal(mode) {
  if (!activeAgentID) return;
  rememberModalFocus('file-browser-modal');
  fileBrowserMode = mode === 'select-upload' ? 'select-upload' : 'browse';
  $('file-browser-title').textContent = fileBrowserMode === 'select-upload'
    ? 'Choose Upload Destination'
    : 'Remote Files';
  $('file-browser-modal').hidden = false;
  activateFileBrowserState(activeAgentID);
  ensurePathBrowserForAgent(activeAgent);
  const state = activeFileBrowserState();
  if (state.result) {
    renderFileBrowser(state.result);
  } else {
    queueFileBrowserPath(state.path || '.', 0, { recordHistory: state.history.length === 0 });
  }
  window.requestAnimationFrame(() => {
    const address = $('file-browser').querySelector('.file-browser-address');
    if (address) address.focus();
    else $('file-browser-close-btn').focus();
  });
}

function closeFileBrowserModal() {
  const modal = $('file-browser-modal');
  if (!modal || modal.hidden) return;
  persistActiveFileBrowserState();
  modal.hidden = true;
  fileBrowserMode = 'browse';
  if (activeAgentID && !pathBrowserTaskSelected()) resetPathBrowserState(activeAgentID, true);
  restoreModalFocus('file-browser-modal');
}

function renderFileBrowser(result) {
  const panel = $('file-browser');
  const state = activeFileBrowserState();
  if (!panel || !state) return;
  if (result) {
    state.result = result;
    state.path = result.path;
    fileBrowserResult = result;
    fileBrowserPath = result.path;
  }
  panel.hidden = false;
  panel.textContent = '';

  const current = state.result;
  const currentPath = current ? current.path : (state.path || '.');
  panel.appendChild(fileBrowserNavigation(state, currentPath, current));
  panel.appendChild(fileBrowserBreadcrumbs(currentPath, current && current.separator));

  if (activeAgent && activeAgent.transport === 'dns' && fileBrowserMode === 'browse') {
    const warning = document.createElement('p');
    warning.className = 'file-browser-transport-warning';
    warning.textContent = 'This agent last checked in over DNS. Directory archives require HTTPS, and larger file downloads may fail.';
    panel.appendChild(warning);
  }

  const transfers = fileBrowserTransferPanel();
  if (transfers) panel.appendChild(transfers);

  if (state.error) {
    const error = document.createElement('div');
    error.className = 'file-browser-empty error-copy';
    error.textContent = state.error;
    panel.appendChild(error);
  }
  if (!current) {
    const empty = document.createElement('div');
    empty.className = 'file-browser-empty';
    empty.textContent = state.loading ? 'Waiting for the agent to list ' + currentPath + '…' : 'Enter a path to browse remote files.';
    panel.appendChild(empty);
    return;
  }

  panel.appendChild(fileBrowserSelectionToolbar(state, current));
  const entries = sortedBrowserEntries(state, current.entries);
  panel.appendChild(fileBrowserTable(state, current, entries));

  const footer = document.createElement('div');
  footer.className = 'file-browser-footer';
  const count = document.createElement('span');
  count.textContent = current.entries.length + ' loaded' + (current.more ? ' · more available' : '');
  footer.appendChild(count);
  if (state.loading) footer.appendChild(fileBrowserBusyLabel('Waiting for agent…'));
  if (current.more) {
    const more = fileBrowserButton('Load More', () => {
      queueFileBrowserPath(current.path, current.next_offset || current.entries.length, { recordHistory: false });
    });
    more.className = 'file-browser-load-more';
    more.disabled = state.loading;
    footer.appendChild(more);
  }
  panel.appendChild(footer);
}

function fileBrowserTransferPanel() {
  const transfers = Array.from(downloadTasks.values())
    .filter(transfer => !transfer.agentID || transfer.agentID === activeAgentID)
    .filter(transfer => ['progress', 'cancelling', 'done', 'failed', 'cancelled'].includes(transfer.status))
    .slice(-4)
    .reverse();
  if (!transfers.length) return null;
  const panel = document.createElement('div');
  panel.className = 'file-browser-transfer-panel';
  transfers.forEach(transfer => {
    const row = document.createElement('div');
    row.className = 'file-browser-transfer-row transfer-' + transfer.status;
    const copy = document.createElement('div');
    const title = document.createElement('strong');
    title.textContent = transfer.kind === 'archive' ? 'Directory archive' : 'File download';
    const detail = document.createElement('span');
    detail.textContent = transferProgressLabel(transfer);
    copy.appendChild(title);
    copy.appendChild(detail);
    row.appendChild(copy);
    if (transfer.status === 'done') {
      row.appendChild(fileBrowserButton('Save', () => saveArtifactByKey(transfer.artifactKey)));
    } else if (transfer.status === 'progress') {
      row.appendChild(fileBrowserButton('Cancel', () => cancelTransfer(transfer)));
    }
    panel.appendChild(row);
  });
  return panel;
}

function transferProgressLabel(transfer) {
  if (transfer.status === 'done') return 'Ready to save · ' + (transfer.filename || 'artifact');
  if (transfer.status === 'failed') return 'Failed · choose Retry from the corresponding row';
  if (transfer.status === 'cancelled') return 'Cancelled · choose Retry from the corresponding row';
  if (transfer.status === 'cancelling') return 'Cancelling…';
  const progress = transfer.progress || {};
  if (transfer.kind === 'archive' || progress.kind === 'archive') {
    const details = [];
    if (progress.files) details.push(progress.files + ' file' + (progress.files === 1 ? '' : 's'));
    if (progress.bytes) details.push(formatFileSize(progress.bytes) + ' read');
    if (progress.archiveBytes) details.push(formatFileSize(progress.archiveBytes) + ' ZIP');
    return (progress.message || 'Preparing archive') + (details.length ? ' · ' + details.join(' · ') : '');
  }
  if (progress.totalBytes > 0 && progress.bytes >= 0) {
    const percent = Math.min(100, Math.round((progress.bytes / progress.totalBytes) * 100));
    return (progress.message || 'Transferring') + ' · ' + percent + '%';
  }
  return progress.message || 'Waiting for agent…';
}

function fileBrowserNavigation(state, path, result) {
  const toolbar = document.createElement('div');
  toolbar.className = 'file-browser-toolbar';
  const nav = document.createElement('div');
  nav.className = 'file-browser-nav-buttons';
  const back = fileBrowserButton('Back', () => navigateFileBrowserHistory(-1));
  back.disabled = state.historyIndex <= 0;
  const forward = fileBrowserButton('Forward', () => navigateFileBrowserHistory(1));
  forward.disabled = state.historyIndex < 0 || state.historyIndex >= state.history.length - 1;
  const up = fileBrowserButton('Up', () => result && result.parent && queueFileBrowserPath(result.parent));
  up.disabled = !result || !result.parent;
  const home = fileBrowserButton('Home', () => queueFileBrowserPath(state.homePath || '.'));
  const refresh = fileBrowserButton('Refresh', () => queueFileBrowserPath(path, 0, { recordHistory: false, force: true }));
  [back, forward, up, home, refresh].forEach(button => nav.appendChild(button));

  const form = document.createElement('form');
  form.className = 'file-browser-address-form';
  const label = document.createElement('label');
  label.className = 'sr-only';
  label.textContent = 'Remote path';
  const input = document.createElement('input');
  input.className = 'file-browser-address';
  input.type = 'text';
  input.value = path;
  input.maxLength = MAX_REMOTE_PATH;
  input.autocomplete = 'off';
  input.spellcheck = false;
  label.appendChild(input);
  const go = fileBrowserButton('Go', () => {});
  go.type = 'submit';
  form.addEventListener('submit', event => {
    event.preventDefault();
    const next = input.value.trim();
    if (!next || hasInvalidPathChars(next)) {
      state.error = 'Enter a valid remote path.';
      renderFileBrowser(state.result);
      return;
    }
    queueFileBrowserPath(next);
  });
  form.appendChild(label);
  form.appendChild(go);
  toolbar.appendChild(nav);
  toolbar.appendChild(form);
  return toolbar;
}

function fileBrowserBreadcrumbs(path, separator) {
  const breadcrumbs = document.createElement('nav');
  breadcrumbs.className = 'file-browser-breadcrumbs';
  breadcrumbs.setAttribute('aria-label', 'Remote path breadcrumbs');
  browserBreadcrumbParts(path, separator).forEach((part, index) => {
    if (index > 0) {
      const divider = document.createElement('span');
      divider.textContent = '›';
      divider.setAttribute('aria-hidden', 'true');
      breadcrumbs.appendChild(divider);
    }
    const button = fileBrowserButton(part.label, () => queueFileBrowserPath(part.path));
    button.className = 'file-browser-crumb';
    if (part.path === path) button.setAttribute('aria-current', 'location');
    breadcrumbs.appendChild(button);
  });
  return breadcrumbs;
}

function browserBreadcrumbParts(path, separator) {
  return SableLogic.browserBreadcrumbParts(path, separator);
}

function fileBrowserSelectionToolbar(state, result) {
  const toolbar = document.createElement('div');
  toolbar.className = 'file-browser-selection-toolbar';
  const search = document.createElement('input');
  search.type = 'search';
  search.className = 'file-browser-filter';
  search.placeholder = 'Filter this folder';
  search.value = state.filter;
  search.addEventListener('input', () => {
    state.filter = search.value;
    renderFileBrowser(state.result);
    window.requestAnimationFrame(() => {
      const next = $('file-browser').querySelector('.file-browser-filter');
      if (next) {
        next.focus();
        next.setSelectionRange(next.value.length, next.value.length);
      }
    });
  });
  toolbar.appendChild(search);

  const actions = document.createElement('div');
  actions.className = 'file-browser-selection-actions';
  if (fileBrowserMode === 'select-upload') {
    actions.appendChild(fileBrowserButton('Select This Folder', () => selectUploadDestination(result.path, true)));
  } else {
    const archive = fileBrowserButton('Download Current Folder', () => queueArchiveFromBrowser([result.path], result.parent || result.path));
    archive.disabled = activeAgent && activeAgent.transport === 'dns';
    archive.title = archive.disabled ? 'Directory archives require HTTPS transport' : 'Prepare this directory as one ZIP archive';
    actions.appendChild(archive);
  }

  if (state.selection.size && fileBrowserMode === 'browse') {
    const selected = selectedBrowserEntries(state, result);
    const label = selected.length === 1 && !selected[0].is_dir ? 'Download Selected File' : 'Download Selected as ZIP';
    const download = fileBrowserButton(label, () => queueSelectedBrowserEntries(state, result));
    download.disabled = selected.length === 0 || (activeAgent && activeAgent.transport === 'dns' && (selected.length > 1 || selected.some(entry => entry.is_dir)));
    actions.appendChild(download);
    actions.appendChild(fileBrowserButton('Clear Selection', () => {
      state.selection.clear();
      renderFileBrowser(result);
    }));
  }
  toolbar.appendChild(actions);
  return toolbar;
}

function fileBrowserTable(state, result, entries) {
  const table = document.createElement('div');
  table.className = 'file-browser-table';
  table.setAttribute('role', 'grid');
  const header = document.createElement('div');
  header.className = 'file-browser-row file-browser-header';
  header.setAttribute('role', 'row');

  const selectCell = fileBrowserCell('');
  if (fileBrowserMode === 'browse') {
    const selectAll = document.createElement('input');
    selectAll.type = 'checkbox';
    selectAll.setAttribute('aria-label', 'Select all visible entries');
    selectAll.checked = entries.length > 0 && entries.every(entry => state.selection.has(entry.path));
    selectAll.indeterminate = !selectAll.checked && entries.some(entry => state.selection.has(entry.path));
    selectAll.addEventListener('change', () => {
      entries.forEach(entry => selectAll.checked ? state.selection.add(entry.path) : state.selection.delete(entry.path));
      renderFileBrowser(result);
    });
    selectCell.appendChild(selectAll);
  }
  header.appendChild(selectCell);
  header.appendChild(fileBrowserSortHeader(state, 'name', 'Name'));
  header.appendChild(fileBrowserSortHeader(state, 'size', 'Size'));
  header.appendChild(fileBrowserSortHeader(state, 'modified', 'Modified'));
  header.appendChild(fileBrowserCell('Actions'));
  table.appendChild(header);

  if (!entries.length) {
    const empty = document.createElement('div');
    empty.className = 'file-browser-empty';
    empty.textContent = state.filter ? 'No entries match this folder filter.' : 'This directory is empty.';
    table.appendChild(empty);
  }
  entries.forEach(entry => table.appendChild(fileBrowserRow(state, result, entry)));
  table.addEventListener('scroll', () => state.scrollByPath.set(result.path, table.scrollTop), { passive: true });
  window.requestAnimationFrame(() => {
    table.scrollTop = state.scrollByPath.get(result.path) || 0;
    if (state.focusedPath) {
      const row = Array.from(table.querySelectorAll('[data-browser-row]')).find(item => item.dataset.path === state.focusedPath);
      if (row) row.focus();
    }
  });
  return table;
}

function fileBrowserSortHeader(state, key, label) {
  const cell = fileBrowserCell('');
  const button = fileBrowserButton(label, () => {
    if (state.sortKey === key) state.sortDirection = state.sortDirection === 'asc' ? 'desc' : 'asc';
    else {
      state.sortKey = key;
      state.sortDirection = 'asc';
    }
    renderFileBrowser(state.result);
  });
  button.className = 'file-browser-sort';
  if (state.sortKey === key) {
    button.textContent += state.sortDirection === 'asc' ? ' ↑' : ' ↓';
    cell.setAttribute('aria-sort', state.sortDirection === 'asc' ? 'ascending' : 'descending');
  }
  cell.appendChild(button);
  return cell;
}

function sortedBrowserEntries(state, entries) {
  return SableLogic.filterSortDirectoryEntries(entries, state.filter, state.sortKey, state.sortDirection);
}

function fileBrowserRow(state, result, entry) {
  const row = document.createElement('div');
  row.className = 'file-browser-row';
  if (entry.is_dir) row.classList.add('directory');
  if (state.selection.has(entry.path)) row.classList.add('selected');
  row.dataset.browserRow = 'true';
  row.dataset.path = entry.path;
  row.tabIndex = 0;
  row.setAttribute('role', 'row');

  const selectCell = fileBrowserCell('');
  if (fileBrowserMode === 'browse') {
    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.checked = state.selection.has(entry.path);
    checkbox.setAttribute('aria-label', 'Select ' + entry.name);
    checkbox.addEventListener('change', () => {
      checkbox.checked ? state.selection.add(entry.path) : state.selection.delete(entry.path);
      renderFileBrowser(result);
    });
    selectCell.appendChild(checkbox);
  }
  row.appendChild(selectCell);

  const nameCell = fileBrowserCell('');
  if (entry.is_dir) {
    const name = fileBrowserButton('▸ ' + entry.name, () => queueFileBrowserPath(entry.path));
    name.className = 'file-browser-name';
    name.title = entry.path;
    nameCell.appendChild(name);
  } else {
    const name = document.createElement('span');
    name.className = 'file-browser-filename';
    name.textContent = entry.name;
    name.title = entry.path;
    nameCell.appendChild(name);
  }
  row.appendChild(nameCell);
  row.appendChild(fileBrowserCell(entry.is_dir ? '—' : formatFileSize(entry.size)));
  row.appendChild(fileBrowserCell(formatBrowserTime(entry.mod_time)));

  const actionCell = fileBrowserCell('');
  actionCell.classList.add('file-browser-actions-cell');
  if (entry.error) {
    actionCell.textContent = entry.error;
  } else if (entry.is_dir) {
    actionCell.appendChild(fileBrowserButton('Open', () => queueFileBrowserPath(entry.path)));
    if (fileBrowserMode === 'select-upload') {
      actionCell.appendChild(fileBrowserButton('Select', () => selectUploadDestination(entry.path, true)));
    } else {
      actionCell.appendChild(fileBrowserDownloadButton(entry));
    }
  } else if (fileBrowserMode === 'select-upload') {
    actionCell.appendChild(fileBrowserButton('Use Path', () => selectUploadDestination(entry.path, false)));
  } else {
    actionCell.appendChild(fileBrowserDownloadButton(entry));
  }
  row.appendChild(actionCell);
  row.addEventListener('keydown', event => handleBrowserRowKey(event, state, result, entry));
  row.addEventListener('focus', () => { state.focusedPath = entry.path; });
  return row;
}

function handleBrowserRowKey(event, state, result, entry) {
  const rows = Array.from(event.currentTarget.parentElement.querySelectorAll('[data-browser-row]'));
  const index = rows.indexOf(event.currentTarget);
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    const next = event.key === 'ArrowDown' ? Math.min(rows.length - 1, index + 1) : Math.max(0, index - 1);
    rows[next]?.focus();
    return;
  }
  if (event.key === ' ') {
    event.preventDefault();
    state.selection.has(entry.path) ? state.selection.delete(entry.path) : state.selection.add(entry.path);
    renderFileBrowser(result);
    return;
  }
  if (event.key === 'Enter') {
    event.preventDefault();
    if (entry.is_dir) queueFileBrowserPath(entry.path);
    else if (fileBrowserMode === 'select-upload') selectUploadDestination(entry.path, false);
    else queueDownloadFromBrowser(entry.path);
  }
}

function selectedBrowserEntries(state, result) {
  const selected = state.selection;
  return (result.entries || []).filter(entry => selected.has(entry.path) && !entry.error);
}

function queueSelectedBrowserEntries(state, result) {
  const entries = selectedBrowserEntries(state, result);
  if (!entries.length) return;
  if (entries.length === 1 && !entries[0].is_dir) {
    queueDownloadFromBrowser(entries[0].path);
    return;
  }
  queueArchiveFromBrowser(entries.map(entry => entry.path), result.path);
}

function fileBrowserDownloadButton(entry) {
  const transfer = downloadStateForPath(entry.path);
  if (transfer && transfer.status === 'done') {
    return fileBrowserButton('Save', () => saveArtifactByKey(transfer.artifactKey));
  }
  if (transfer && ['progress', 'cancelling'].includes(transfer.status)) {
    const label = transfer.status === 'cancelling' ? 'Cancelling…' : 'Cancel';
    const button = fileBrowserButton(label, () => cancelTransfer(transfer));
    button.classList.add('file-browser-cancel-transfer');
    button.disabled = transfer.status === 'cancelling';
    return button;
  }
  if (entry.is_dir) {
    const button = fileBrowserButton(transfer && ['failed', 'cancelled'].includes(transfer.status) ? 'Retry ZIP' : 'Download ZIP', () => queueArchiveFromBrowser([entry.path], fileBrowserResult ? fileBrowserResult.path : ''));
    button.disabled = activeAgent && activeAgent.transport === 'dns';
    button.title = button.disabled ? 'Directory archives require HTTPS transport' : 'Prepare this directory as one ZIP archive';
    return button;
  }
  return fileBrowserButton(transfer && ['failed', 'cancelled'].includes(transfer.status) ? 'Retry' : 'Download', () => queueDownloadFromBrowser(entry.path));
}

function downloadStateForPath(path) {
  let match = null;
  for (const transfer of downloadTasks.values()) {
    if (transfer.agentID && transfer.agentID !== activeAgentID) continue;
    if (transfer.path === path || (Array.isArray(transfer.paths) && transfer.paths.includes(path))) match = transfer;
  }
  return match;
}

function openArtifactsPanel(artifactKey) {
  if (!$('file-browser-modal').hidden) closeFileBrowserModal();
  activeSessionPanel = 'artifacts';
  openSessionDetailsModal();
  if (!artifactKey) return;
  window.requestAnimationFrame(() => {
    const row = Array.from(document.querySelectorAll('[data-artifact-key]'))
      .find(item => item.dataset.artifactKey === artifactKey);
    if (!row) return;
    row.classList.add('panel-item-highlight');
    row.scrollIntoView({ block: 'nearest' });
    window.setTimeout(() => row.classList.remove('panel-item-highlight'), 1800);
  });
}

async function cancelTransfer(transfer) {
  if (!transfer || !transfer.taskID) return;
  transfer.status = 'cancelling';
  if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
  const queued = await queueCancelTask(transfer.taskID);
  if (!queued) {
    transfer.status = 'progress';
    if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
  }
}

async function queueFileBrowserPath(path, offset = 0, options = {}) {
  if (!activeAgentID) return;
  const state = activeFileBrowserState();
  path = String(path || '.').trim() || '.';
  const isPage = offset > 0;
  let seq = state.activeSeq;
  if (!isPage) {
    saveFileBrowserScroll(state);
    seq = ++state.requestSeq;
    state.activeSeq = seq;
    state.path = path;
    state.error = '';
    state.selection.clear();
    if (options.recordHistory !== false) pushFileBrowserHistory(state, path);
    const cached = state.cache.get(path);
    if (cached && !options.force) {
      state.result = cached;
      fileBrowserResult = cached;
      fileBrowserPath = cached.path;
    } else if (!cached) {
      state.result = null;
      fileBrowserResult = null;
      fileBrowserPath = path;
    }
  }
  if (!seq) {
    seq = ++state.requestSeq;
    state.activeSeq = seq;
  }
  state.loading = true;
  renderFileBrowser(state.result);

  const request = { agentID: activeAgentID, path, offset, seq };
  fileBrowserSubmissionCount++;
  try {
    const payload = JSON.stringify({ path, offset, limit: 250 });
    const data = await submitTask(activeAgentID, { type: 'ls', payload });
    if (!data) {
      state.loading = false;
      state.error = 'Browse request failed.';
      renderFileBrowser(state.result);
      return;
    }
    state.pending.set(data.task_id, request);
    const deferred = deferredFileBrowserOutputs.get(data.task_id);
    if (deferred) {
      deferredFileBrowserOutputs.delete(data.task_id);
      handleFileBrowserOutput(deferred);
    }
  } catch (err) {
    state.loading = false;
    state.error = err.message;
    renderFileBrowser(state.result);
  } finally {
    fileBrowserSubmissionCount = Math.max(0, fileBrowserSubmissionCount - 1);
  }
}

function pushFileBrowserHistory(state, path) {
  if (state.history[state.historyIndex] === path) return;
  state.history = state.history.slice(0, state.historyIndex + 1);
  state.history.push(path);
  state.historyIndex = state.history.length - 1;
}

function navigateFileBrowserHistory(direction) {
  const state = activeFileBrowserState();
  const nextIndex = state.historyIndex + direction;
  if (nextIndex < 0 || nextIndex >= state.history.length) return;
  saveFileBrowserScroll(state);
  state.historyIndex = nextIndex;
  queueFileBrowserPath(state.history[nextIndex], 0, { recordHistory: false });
}

function saveFileBrowserScroll(state) {
  const table = $('file-browser') && $('file-browser').querySelector('.file-browser-table');
  if (table && state.path) state.scrollByPath.set(state.path, table.scrollTop);
}

async function queueDownloadFromBrowser(path) {
  if (!activeAgentID) return;
  const existing = downloadStateForPath(path);
  if (existing && ['progress', 'cancelling'].includes(existing.status)) return;
  const targetAgentID = activeAgentID;
  try {
    const data = await submitTask(targetAgentID, { type: 'download', payload: path });
    if (!data) return;
    downloadTasks.set(data.task_id, {
      taskID: data.task_id,
      agentID: targetAgentID,
      kind: 'file',
      path,
      paths: [path],
      filename: basenameFromPath(path),
      status: 'progress',
      artifactKey: '',
      progress: null,
    });
    if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
    appendOutput('[>] download ' + path + '  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
    showToast('File download queued.');
    refreshActiveAgent();
  } catch (err) {
    showToast('Download request failed: ' + err.message, 'error');
  }
}

async function queueArchiveFromBrowser(paths, base) {
  if (!activeAgentID || !Array.isArray(paths) || paths.length === 0) return;
  if (paths.length > MAX_ARCHIVE_SELECTION) {
    showToast('Select no more than ' + MAX_ARCHIVE_SELECTION + ' entries per archive.', 'error');
    return;
  }
  if (activeAgent && activeAgent.transport === 'dns') {
    showToast('Directory archives require the agent to reconnect over HTTPS.', 'error');
    return;
  }
  const targetAgentID = activeAgentID;
  const payload = paths.length === 1 ? paths[0] : JSON.stringify({ paths, base });
  try {
    const data = await submitTask(targetAgentID, { type: 'download_archive', payload });
    if (!data) return;
    downloadTasks.set(data.task_id, {
      taskID: data.task_id,
      agentID: targetAgentID,
      kind: 'archive',
      path: paths.length === 1 ? paths[0] : base,
      paths: paths.slice(),
      filename: paths.length === 1 ? basenameFromPath(paths[0]) + '.zip' : 'remote-selection.zip',
      status: 'progress',
      artifactKey: '',
      progress: null,
    });
    if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
    appendOutput('[>] directory archive queued  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
    showToast('Directory archive queued.');
    refreshActiveAgent();
  } catch (err) {
    showToast('Archive request failed: ' + err.message, 'error');
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
  cell.setAttribute('role', 'gridcell');
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

function fileBrowserBusyLabel(label) {
  const status = document.createElement('span');
  status.className = 'file-browser-busy';
  status.textContent = label;
  return status;
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

function openClearConfirmModal() {
  if (!activeAgentID || taskRequestInFlight) return;
  pendingClearAgentID = activeAgentID;
  rememberModalFocus('clear-confirm-modal');
  $('clear-confirm-copy').textContent = 'Clear output history for ' + agentDisplayName(activeAgent) + '? This removes persisted output from the server for this agent.';
  $('clear-confirm-modal').hidden = false;
  window.requestAnimationFrame(() => $('clear-cancel-btn').focus());
}

function closeClearConfirmModal() {
  const modal = $('clear-confirm-modal');
  if (!modal || modal.hidden) return;
  modal.hidden = true;
  pendingClearAgentID = '';
  restoreModalFocus('clear-confirm-modal');
}

async function confirmClearOutput() {
  const agentID = pendingClearAgentID;
  if (!agentID) return;
  closeClearConfirmModal();
  await clearOutputHistory(agentID);
}

function openKillConfirmModal(agent) {
  if (!agent || !agent.id || taskRequestInFlight) return;
  if (getAgentState(agent) === 'retired') {
    showToast('Restore this retired agent before queueing a kill task.', 'error');
    return;
  }
  pendingKillAgentID = agent.id;
  rememberModalFocus('kill-confirm-modal');
  $('kill-confirm-copy').textContent = 'Queue a kill task for ' + agentDisplayName(agent) + '? It takes effect after the agent checks in and processes the task.';
  $('kill-confirm-modal').hidden = false;
  window.requestAnimationFrame(() => $('kill-cancel-btn').focus());
}

function closeKillConfirmModal() {
  const modal = $('kill-confirm-modal');
  if (!modal || modal.hidden) return;
  modal.hidden = true;
  pendingKillAgentID = '';
  restoreModalFocus('kill-confirm-modal');
}

async function confirmKillSession() {
  const targetAgentID = pendingKillAgentID;
  if (!targetAgentID) return;
  closeKillConfirmModal();
  setQueueBusy(true, 'Queueing kill task...');
  try {
    const data = await submitTask(targetAgentID, { type: 'kill', payload: '' });
    if (data) {
      appendOutput('[>] kill queued  (id: ' + data.task_id.slice(0, 8) + ')', '', targetAgentID);
      if (targetAgentID === activeAgentID) await refreshActiveAgent();
      else await loadAgents();
      showToast('Kill task queued.');
    }
  } catch (err) {
    appendOutput('[-] kill request error: ' + err.message, '', targetAgentID);
    showToast('Kill request failed: ' + err.message, 'error');
  } finally {
    setQueueBusy(false, '');
    renderAgentList();
  }
}

function openSessionDetailsModal() {
  if (!activeAgentID) return;
  rememberModalFocus('session-details-modal');
  $('session-details-title').textContent = 'Agent details';
  renderSessionPanels();
  loadAudit();
  $('session-details-modal').hidden = false;
  $('session-details-btn').hidden = true;
  window.requestAnimationFrame(() => $('session-details-close-btn').focus());
}

function closeSessionDetailsModal() {
  const modal = $('session-details-modal');
  if (!modal || modal.hidden) return;
  modal.hidden = true;
  if (activeAgentID) $('session-details-btn').hidden = false;
  restoreModalFocus('session-details-modal');
}

function openActiveJobsModal() {
  rememberModalFocus('active-jobs-modal');
  renderActiveJobsModal();
  $('active-jobs-modal').hidden = false;
  window.requestAnimationFrame(() => $('active-jobs-close-btn').focus());
}

function closeActiveJobsModal() {
  const modal = $('active-jobs-modal');
  if (!modal || modal.hidden) return;
  modal.hidden = true;
  restoreModalFocus('active-jobs-modal');
}

async function openEditInfoModal(agent = activeAgent) {
  if (!agent || !agent.id) return;
  const targetAgentID = agent.id;
  let detail = agent;
  try {
    const resp = await apiFetch('/api/agents/' + targetAgentID);
    if (resp.ok) detail = { ...agent, ...(await resp.json()) };
  } catch (_) {
    // The overview summary still contains enough information to edit the profile.
  }
  rememberModalFocus('edit-info-modal');
  metadataEditAgentID = targetAgentID;
  $('edit-info-title').textContent = 'Edit ' + agentDisplayName(detail);
  $('display-name-input').value = detail.display_name || '';
  $('tag-input').value = (detail.tags || []).join(', ');
  $('notes-input').value = detail.notes || '';
  $('metadata-save-status').textContent = '';
  metadataDirty = false;
  metadataDraftAgentID = targetAgentID;
  $('edit-info-modal').hidden = false;
  window.requestAnimationFrame(() => $('display-name-input').focus());
}

function closeEditInfoModal() {
  const modal = $('edit-info-modal');
  if (!modal || modal.hidden) return;
  modal.hidden = true;
  metadataEditAgentID = '';
  restoreModalFocus('edit-info-modal');
}
