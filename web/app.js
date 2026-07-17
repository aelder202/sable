'use strict';

let token = null;

let activeAgentID = null;

let activeAgent = null;

let seenTaskIDs = new Set();

let currentOutputs = [];

let pendingUploadFile = null;

let taskHistory = [];

let taskHistoryIndex = -1;

let taskMetadataByID = new Map();

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

let fleetOverview = null;

let ignoredFailureAlertIDs = new Set();

let failureAlertActionIDs = new Set();

let activePrimaryView = 'overview';

let pendingRouteAgentID = '';

let agentsRequestInFlight = false;

let selectedAgentIDs = new Set();

let bulkSelectionMode = false;

let taskTargetMode = 'current';

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

let fileBrowserStates = new Map();

let deferredFileBrowserOutputs = new Map();

let fileBrowserSubmissionCount = 0;

let pendingKillAgentID = '';

let outputSearchExpanded = true;

let outputTypeFilter = 'all';

let downloadTasks = new Map();

let outputRowSeq = 0;

let taskTypeSearchInput = null;

let actionConfirmCallback = null;

let actionConfirmOrigin = null;

let modalFocusOrigins = new Map();

let toastTimer = null;

let metadataDirty = false;

let metadataDraftAgentID = '';

let metadataEditAgentID = '';

let openAgentMenuID = '';

const MAX_LOGIN_BODY_BYTES = 4096;

const MAX_UPLOAD_BYTES = 40 * 1024 * 1024;

const MAX_SLEEP_SECONDS = 24 * 60 * 60;

const MAX_REMOTE_PATH = 4096;

const MAX_ARCHIVE_SELECTION = 100;

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
  { label: 'Agent Control', types: ['sleep', 'kill'] },
];

const BULK_TASK_TYPES = new Set(['shell', 'ps', 'screenshot', 'snapshot', 'persistence', 'peas', 'sleep']);

const TASK_TYPES = {
  shell: {
    buttonLabel: 'Run shell command',
    help: 'Queue a single shell command for the selected agent.',
    note: 'Shell payloads stay in memory only and are still server-side validated before queueing.',
    placeholder: 'Enter a shell command',
    requiresPayload: true,
    inputMode: 'text',
  },
  ps: {
    buttonLabel: 'Run process listing',
    help: 'List running processes for the selected agent.',
    note: 'Process listing is a read-only, one-shot situational awareness task.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  screenshot: {
    buttonLabel: 'Capture screenshot',
    help: 'Capture one bounded screenshot from the selected agent.',
    note: 'Screenshots are operator-initiated, downsampled, and delivered as bounded chunks.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  snapshot: {
    buttonLabel: 'Collect host info',
    help: 'Collect a host information report from the selected agent.',
    note: 'Host Info returns identity, network, route, disk, and environment basics as a text artifact.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  persistence: {
    buttonLabel: 'Check persistence',
    help: 'List common persistence locations for defensive review.',
    note: 'Persistence detection reads common autorun locations and does not modify them.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  peas: {
    buttonLabel: 'Run PEAS scan',
    help: 'Run LinPEAS or winPEAS based on the selected agent OS.',
    note: 'PEAS output is captured as a text artifact and returned through chunked results.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  download: {
    buttonLabel: 'Download file',
    help: 'Request a remote file path and receive the result as a browser download.',
    note: 'Path suggestions are prepared automatically. Use Browse to open the remote file browser and download files directly.',
    placeholder: 'Enter a remote file path',
    requiresPayload: true,
    inputMode: 'text',
  },
  upload: {
    buttonLabel: 'Upload file',
    help: 'Send a local file to a remote destination path on the selected agent.',
    note: 'Path suggestions are prepared automatically. Pick a file with Choose File or Browse remote directories to set the destination.',
    placeholder: 'Enter a remote destination path',
    requiresPayload: true,
    inputMode: 'text',
  },
  sleep: {
    buttonLabel: 'Update sleep interval',
    help: 'Update the beacon interval in seconds.',
    note: 'Sleep values must be whole seconds between 1 and 86400.',
    placeholder: 'Enter seconds between 1 and 86400',
    requiresPayload: true,
    inputMode: 'numeric',
  },
  kill: {
    buttonLabel: 'Terminate agent',
    help: 'Terminate the selected agent after it processes the task.',
    note: 'Kill requires a second confirmation click and does not accept an additional payload.',
    placeholder: 'No additional value required',
    requiresPayload: false,
    inputMode: 'text',
  },
  interactive: {
    buttonLabel: 'Start interactive shell',
    help: 'Open a near-real-time shell view for the selected agent.',
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

$('overview-nav-btn').addEventListener('click', () => setPrimaryView('overview'));

$('agents-nav-btn').addEventListener('click', () => setPrimaryView('agents'));

$('active-jobs-btn').addEventListener('click', openActiveJobsModal);

$('active-transfers-btn').addEventListener('click', () => setPrimaryView('agents'));

$('overview-filter').addEventListener('input', renderDashboard);
$('overview-status-filter').addEventListener('change', renderDashboard);
$('overview-platform-filter').addEventListener('change', renderDashboard);
$('overview-transport-filter').addEventListener('change', renderDashboard);
$('overview-show-retired').addEventListener('change', renderDashboard);
$('outcome-range-24h').addEventListener('click', () => setOverviewOutcomeRange('24h'));
$('outcome-range-7d').addEventListener('click', () => setOverviewOutcomeRange('7d'));

document.querySelectorAll('[data-overview-status]').forEach(button => {
  button.addEventListener('click', () => {
    const status = button.dataset.overviewStatus || 'all';
    $('overview-status-filter').value = status;
    if (status === 'retired') $('overview-show-retired').checked = true;
    renderDashboard();
  });
});

$('agent-filter').addEventListener('input', renderAgentList);

$('agent-status-filter').addEventListener('change', renderAgentList);

document.querySelectorAll('[data-agent-status]').forEach(button => {
  button.addEventListener('click', () => {
    $('agent-status-filter').value = button.dataset.agentStatus || 'all';
    renderAgentList();
  });
});

$('bulk-select-mode-btn').addEventListener('click', () => {
  setBulkSelectionMode(!bulkSelectionMode);
});

initAgentSidebar();

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

$('edit-info-close-btn').addEventListener('click', closeEditInfoModal);

document.querySelector('[data-close-edit-info]').addEventListener('click', closeEditInfoModal);

$('active-jobs-close-btn').addEventListener('click', closeActiveJobsModal);

document.querySelector('[data-close-active-jobs]').addEventListener('click', closeActiveJobsModal);

$('file-browser-close-btn').addEventListener('click', closeFileBrowserModal);

document.querySelector('[data-close-file-browser]').addEventListener('click', closeFileBrowserModal);

$('clear-cancel-btn').addEventListener('click', closeClearConfirmModal);

document.querySelector('[data-close-clear-confirm]').addEventListener('click', closeClearConfirmModal);

$('clear-confirm-btn').addEventListener('click', confirmClearOutput);

$('kill-cancel-btn').addEventListener('click', closeKillConfirmModal);

document.querySelector('[data-close-kill-confirm]').addEventListener('click', closeKillConfirmModal);

$('kill-confirm-btn').addEventListener('click', confirmKillSession);

$('action-confirm-cancel-btn').addEventListener('click', closeActionConfirm);

document.querySelector('[data-close-action-confirm]').addEventListener('click', closeActionConfirm);

$('action-confirm-btn').addEventListener('click', confirmAction);

initDraggableModals();

$('task-input').addEventListener('input', () => {
  clearTaskInputError();
  saveActiveTaskDraft();
  updateComposerReadiness();
  if (pathSuggestionTaskSelected() && !interactiveMode) {
    schedulePathCompletion();
  } else {
    hidePathSuggestions();
  }
});

$('send-btn').addEventListener('click', runComposerTask);

$('bulk-send-btn').addEventListener('click', sendBulkTask);

$('target-current-btn').addEventListener('click', () => setTaskTargetMode('current'));

$('target-selected-btn').addEventListener('click', () => setTaskTargetMode('selected'));

$('details-open-files-btn').addEventListener('click', () => {
  if (!activeAgentID || taskRequestInFlight) return;
  closeSessionDetailsModal();
  openFileBrowserModal('browse');
});

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
  const targetAgentID = metadataEditAgentID || activeAgentID;
  if (!targetAgentID) return;
  const tags = $('tag-input').value.split(',').map(tag => tag.trim()).filter(Boolean);
  const button = $('save-metadata-btn');
  const status = $('metadata-save-status');
  button.disabled = true;
  status.textContent = 'Saving...';
  try {
    const resp = await apiFetch('/api/agents/' + targetAgentID + '/metadata', {
      method: 'PUT',
      body: JSON.stringify({
        display_name: $('display-name-input').value.trim(),
        notes: $('notes-input').value,
        tags,
      }),
    });
    if (!resp.ok) {
      status.textContent = await readResponseMessage(resp, 'Save failed (' + resp.status + ')');
      return;
    }
    const updated = await resp.json();
    if (targetAgentID === activeAgentID) activeAgent = { ...(activeAgent || {}), ...updated };
    allAgents = allAgents.map(agent => agent.id === targetAgentID ? { ...agent, ...updated } : agent);
    metadataDirty = false;
    if (targetAgentID === activeAgentID) updateSessionHeader();
    status.textContent = 'Saved';
    showToast('Agent metadata saved.');
    renderAgentList();
    renderDashboard();
    renderSessionPanels();
    closeEditInfoModal();
  } catch (err) {
    status.textContent = 'Save failed: ' + err.message;
  } finally {
    button.disabled = false;
  }
});

['display-name-input', 'tag-input', 'notes-input'].forEach(id => {
  $(id).addEventListener('input', () => {
    metadataDirty = true;
    if ($('metadata-save-status').textContent !== 'Saving...') $('metadata-save-status').textContent = 'Unsaved changes';
  });
});

$('save-artifact-retention-btn').addEventListener('click', async () => {
	if (!activeAgentID) return;
	const maxItems = Number.parseInt($('artifact-retention-input').value, 10);
	if (!Number.isInteger(maxItems) || maxItems < 1 || maxItems > 256) {
		appendOutput('[-] artifact retention must be between 1 and 256', '', activeAgentID, 'error');
		return;
	}
	try {
		const resp = await apiFetch('/api/agents/' + activeAgentID + '/artifacts/retention', {
			method: 'PUT', body: JSON.stringify({ max_items: maxItems }),
		});
		if (!resp.ok) throw new Error('request failed (' + resp.status + ')');
		activeAgent.artifact_retention = maxItems;
		artifactLibrary = artifactLibrary.slice(0, maxItems);
		appendOutput('[>] artifact retention set to ' + maxItems, '', activeAgentID, 'operator');
		renderSessionPanels();
	} catch (err) {
		appendOutput('[-] artifact retention error: ' + err.message, '', activeAgentID, 'error');
	}
});

$('rekey-agent-btn').addEventListener('click', async () => {
	if (!activeAgentID) return;
  openActionConfirm({
    title: 'Rotate Agent Secret',
    copy: 'The deployed agent will stop authenticating until it is rebuilt or reconfigured with the new secret.',
    confirmLabel: 'Rotate Secret',
    danger: true,
    onConfirm: async () => {
      try {
        const resp = await apiFetch('/api/agents/' + activeAgentID + '/rekey', { method: 'POST' });
        if (!resp.ok) throw new Error(await readResponseMessage(resp, 'request failed (' + resp.status + ')'));
        const data = await resp.json();
        $('rekey-secret-output').textContent = data.secret_hex || '';
        $('rekey-secret-output').hidden = false;
        $('copy-rekey-secret-btn').hidden = false;
        showToast('Agent secret rotated. Copy the new secret now.');
      } catch (err) {
        showToast('Secret rotation failed: ' + err.message, 'error');
      }
    },
  });
});

$('copy-rekey-secret-btn').addEventListener('click', async () => {
	const secret = $('rekey-secret-output').textContent || '';
	if (!secret) return;
	try {
		await navigator.clipboard.writeText(secret);
		$('copy-rekey-secret-btn').textContent = 'Copied';
		window.setTimeout(() => { $('copy-rekey-secret-btn').textContent = 'Copy New Secret'; }, 1200);
	} catch (_) {
		appendOutput('[-] copy secret failed', '', activeAgentID, 'error');
	}
});

$('copy-agent-id-btn').addEventListener('click', async () => {
  if (!activeAgentID) return;
  try {
    await navigator.clipboard.writeText(activeAgentID);
    showToast('Agent ID copied.');
  } catch (_) {
    showToast('Could not copy the agent ID.', 'error');
  }
});

$('retire-agent-btn').addEventListener('click', () => {
  if (!activeAgentID || !activeAgent) return;
  const retiring = !activeAgent.retired;
  if (!retiring) {
    updateAgentRetirement(false);
    return;
  }
  openActionConfirm({
    title: 'Retire Agent',
    copy: 'Retire ' + agentDisplayName(activeAgent) + '? Its history and artifacts will be preserved and it can be restored later.',
    confirmLabel: 'Retire Agent',
    onConfirm: () => updateAgentRetirement(true),
  });
});

async function updateAgentRetirement(retired, targetAgentID = activeAgentID) {
  if (!targetAgentID) return;
  try {
    const resp = await apiFetch('/api/agents/' + targetAgentID + '/lifecycle', {
      method: 'PUT',
      body: JSON.stringify({ retired }),
    });
    if (!resp.ok) throw new Error(await readResponseMessage(resp, 'request failed (' + resp.status + ')'));
    const updated = await resp.json();
    if (targetAgentID === activeAgentID) activeAgent = updated;
    allAgents = allAgents.map(agent => agent.id === targetAgentID ? { ...agent, ...updated } : agent);
    showToast(retired ? 'Agent retired.' : 'Agent restored to active fleet views.');
    updateSessionHeader();
    renderAgentList();
    await loadAgents();
  } catch (err) {
    showToast('Lifecycle update failed: ' + err.message, 'error');
  }
}

$('revoke-agent-btn').addEventListener('click', async () => {
	if (!activeAgentID) return;
  const agentID = activeAgentID;
  openActionConfirm({
    title: 'Permanently Revoke Agent',
    copy: 'Permanently delete this identity, queued work, output history, and retained artifacts? This cannot be undone.',
    confirmLabel: 'Revoke Permanently',
    danger: true,
    onConfirm: async () => {
      try {
        const resp = await apiFetch('/api/agents/' + agentID, { method: 'DELETE' });
        if (!resp.ok) throw new Error(await readResponseMessage(resp, 'request failed (' + resp.status + ')'));
        closeSessionDetailsModal();
        clearActiveSession();
        await loadAgents();
        showToast('Agent revoked and retained state deleted.');
      } catch (err) {
        showToast('Agent revocation failed: ' + err.message, 'error');
      }
    },
  });
});

$('output-search').addEventListener('input', applyOutputSearch);

$('task-input').addEventListener('keydown', e => {
  if (e.key === 'Enter') {
    runComposerTask();
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

  if (e.key === 'Tab' && trapModalFocus(e)) return;

  if (e.key === '/' && !$('main-view').hidden && !isTypingTarget(document.activeElement)) {
    e.preventDefault();
    const filter = activePrimaryView === 'overview' ? $('overview-filter') : $('agent-filter');
    filter.focus();
    filter.select();
    return;
  }

  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k' && !$('main-view').hidden) {
    e.preventDefault();
    focusPrimaryInput(true);
    return;
  }

  if (e.key !== 'Escape') return;

  if (!$('action-confirm-modal').hidden) {
    e.preventDefault();
    closeActionConfirm();
    return;
  }

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

  if (!$('active-jobs-modal').hidden) {
    e.preventDefault();
    closeActiveJobsModal();
    return;
  }

  if (!$('edit-info-modal').hidden) {
    e.preventDefault();
    closeEditInfoModal();
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

document.addEventListener('click', event => {
  if (!event.target.closest('.agent-menu-wrap')) closeAgentMenus();
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
    setTaskInputError('The remote path browser is still preparing. Try again when the agent confirms readiness.');
    return;
  }
  openFileBrowserModal(selectedTaskType === 'upload' ? 'select-upload' : 'browse');
});

const outputEl = $('output');
const outputFollowToggle = $('output-follow-toggle');

$('jump-latest-btn').addEventListener('click', () => {
  scrollOutputToBottom();
});

outputFollowToggle.addEventListener('change', () => {
  followOutput = outputFollowToggle.checked;
  if (followOutput) scrollOutputToBottom();
  else updateOutputControls();
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
