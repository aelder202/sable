'use strict';

async function loadArtifacts(agentID) {
  if (!agentID || !token) return;
  try {
    const data = await apiFetchAll('/api/agents/' + agentID + '/artifacts');
    if (agentID !== activeAgentID) return;
    mergeServerArtifacts(Array.isArray(data) ? data.map(hydrateServerArtifact).filter(Boolean) : []);
    renderSessionPanels();
  } catch (_) {
    // Artifacts are supplemental; keep the locally discovered list on transient failures.
  }
}

function mergeServerArtifacts(serverArtifacts) {
  const existing = new Map(artifactLibrary.map(item => [item.key, item]));
  const seen = new Set();
  const merged = serverArtifacts.map(item => {
    seen.add(item.key);
    const local = existing.get(item.key);
    return local
      ? { ...item, base64Value: local.base64Value || item.base64Value }
      : item;
  });
  artifactLibrary.forEach(item => {
    if (!seen.has(item.key)) merged.push(item);
  });
	const retention = activeAgent && activeAgent.artifact_retention ? activeAgent.artifact_retention : 256;
	artifactLibrary = merged.slice(0, retention);
}

function hydrateServerArtifact(item) {
  if (!item || !item.filename) return null;
  const createdAt = item.created_at || new Date().toISOString();
  return {
    key: item.key || item.id,
    serverID: item.id,
    taskID: item.task_id || '',
    type: item.type || '',
    label: item.label || 'artifact',
    filename: item.filename,
    archiveFilename: item.archive_filename || item.filename,
    mime: item.mime || 'application/octet-stream',
    base64Value: item.data || '',
    sizeBytes: Number(item.size_bytes) || 0,
    compress: Boolean(item.compress),
    timestamp: createdAt ? new Date(createdAt).toLocaleTimeString() : '',
    createdAt,
    serverSynced: true,
  };
}

function appendDownloadResult(taskID, short, ts, base64Value, historical, createdAt) {
  const state = downloadTasks.get(taskID);
  const sourceName = state && state.filename ? state.filename : 'download.bin';
  const artifact = appendFileResult({
	 taskID,
    short,
    ts,
    base64Value,
    filename: sourceName,
    archiveFilename: sourceName,
    mime: 'application/octet-stream',
    buttonLabel: 'Save File',
    label: 'download ready',
    type: 'download',
    historical,
    compress: false,
    createdAt,
  });
  if (state) {
    state.status = 'done';
    state.artifactKey = artifact ? artifact.key : '';
    if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
  }
}

function appendArchiveResult(taskID, short, ts, payload, historical, createdAt) {
  let result;
  try {
    result = JSON.parse(payload || '{}');
  } catch (_) {
    appendOutput('[err ' + short + ' ' + ts + '] invalid archive payload', '', activeAgentID, 'error');
    return;
  }
  if (!result || !result.data || !result.filename) {
    appendOutput('[err ' + short + ' ' + ts + '] invalid archive payload', '', activeAgentID, 'error');
    return;
  }
  const skipped = Array.isArray(result.skipped) ? result.skipped : [];
  const summary = String(result.file_count || 0) + ' files · ' + formatFileSize(Number(result.source_bytes || 0)) +
    (skipped.length ? ' · ' + skipped.length + ' skipped' : '');
  const artifact = appendFileResult({
    taskID,
    short,
    ts,
    base64Value: result.data,
    filename: result.filename,
    archiveFilename: result.filename,
    mime: result.mime || 'application/zip',
    buttonLabel: 'Save Archive',
    label: 'directory archive ready',
    type: 'download_archive',
    historical,
    compress: false,
    createdAt,
    summary,
    warnings: skipped,
  });
  const state = downloadTasks.get(taskID);
  if (state) {
    state.status = 'done';
    state.filename = result.filename;
    state.artifactKey = artifact ? artifact.key : '';
    state.progress = { kind: 'archive', phase: 'ready', message: summary };
  }
  if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
  if (skipped.length) showToast('Archive ready with ' + skipped.length + ' skipped entr' + (skipped.length === 1 ? 'y.' : 'ies.'));
}

function appendArtifactResult(taskID, short, ts, payload, label, buttonLabel, historical, createdAt) {
  let result;
  try {
    result = JSON.parse(payload);
  } catch (_) {
    appendOutput('[err ' + short + ' ' + ts + '] invalid artifact payload');
    return;
  }

  if (!result || !result.data || !result.filename) {
    appendOutput('[err ' + short + ' ' + ts + '] invalid artifact payload');
    return;
  }

  appendFileResult({
	 taskID,
    short,
    ts,
    base64Value: result.data,
    filename: result.filename,
    mime: result.mime || 'application/octet-stream',
    buttonLabel,
    label,
    type: label,
    historical,
    createdAt,
  });
}

function appendFileResult(options) {
  const shouldScroll = followOutput || isOutputNearBottom();
  const wrap = document.createElement('article');
  wrap.className = 'output-download output-task-card output-artifact-card success';
  wrap.dataset.outputType = 'artifact';
  wrap.dataset.outputTaskType = options.type || 'artifact';
  wrap.dataset.taskId = options.taskID || options.short || '';
  wrap.dataset.completedAt = options.createdAt || '';
  wrap.dataset.rowID = String(++outputRowSeq);
  wrap.dataset.searchText = (options.label + ' ' + options.filename).toLowerCase();

  const header = document.createElement('header');
  header.className = 'output-task-header';
  const time = document.createElement('time');
  time.className = 'output-task-time';
  time.dateTime = options.createdAt || '';
  time.textContent = options.ts || resultTimeLabel(options.createdAt);
  const marker = document.createElement('span');
  marker.className = 'output-task-marker';
  marker.appendChild(createUIIcon('arrow'));
  const heading = document.createElement('div');
  heading.className = 'output-task-heading';
  const title = document.createElement('strong');
  title.className = 'output-task-command';
  title.textContent = options.label || 'Artifact ready';
  const identifier = document.createElement('span');
  identifier.className = 'output-task-id';
  identifier.textContent = String(options.taskID || options.short || '').slice(0, 8);
  heading.appendChild(title);
  heading.appendChild(identifier);
  const status = document.createElement('span');
  status.className = 'output-task-status success';
  const statusDot = document.createElement('span');
  statusDot.className = 'output-task-status-dot';
  status.appendChild(statusDot);
  status.appendChild(document.createTextNode('Success'));
  const duration = document.createElement('span');
  duration.className = 'output-task-duration';

  const bodyWrap = document.createElement('div');
  bodyWrap.className = 'output-task-body-wrap';
  bodyWrap.id = 'output-task-body-' + wrap.dataset.rowID;
  const text = document.createElement('pre');
  text.className = 'output-task-body output-download-text';
  text.textContent = (options.historical
    ? 'Artifact restored from agent history.'
    : 'Artifact returned by the agent.') + (options.summary ? ' ' + options.summary + '.' : '');

  const attachment = document.createElement('div');
  attachment.className = 'output-artifact-attachment';
  const attachmentIcon = document.createElement('span');
  attachmentIcon.className = 'output-artifact-icon';
  attachmentIcon.appendChild(createUIIcon('artifact'));
  const attachmentCopy = document.createElement('span');
  attachmentCopy.className = 'output-artifact-copy';
  const filename = document.createElement('strong');
  filename.textContent = options.filename;
  const size = document.createElement('span');
  const approximateBytes = options.base64Value ? Math.floor(String(options.base64Value).length * 3 / 4) : NaN;
  size.textContent = Number.isFinite(approximateBytes) ? formatFileSize(approximateBytes) : (options.mime || 'Artifact');
  attachmentCopy.appendChild(filename);
  attachmentCopy.appendChild(size);

  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'output-download-btn';
  button.textContent = options.buttonLabel;
  button.addEventListener('click', async () => {
    try {
      await saveArtifact(options);
    } catch (err) {
      appendOutput('[err ' + options.short + ' ' + options.ts + '] invalid file payload');
    }
  });

  attachment.appendChild(attachmentIcon);
  attachment.appendChild(attachmentCopy);
  attachment.appendChild(button);
  bodyWrap.appendChild(text);
  bodyWrap.appendChild(attachment);

  const toggle = document.createElement('button');
  toggle.type = 'button';
  toggle.className = 'output-task-toggle';
  toggle.setAttribute('aria-expanded', 'true');
  toggle.setAttribute('aria-controls', bodyWrap.id);
  toggle.title = 'Collapse task output';
  toggle.appendChild(createUIIcon('chevron'));
  toggle.addEventListener('click', () => {
    const collapsed = wrap.classList.toggle('collapsed');
    toggle.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
    toggle.title = collapsed ? 'Expand task output' : 'Collapse task output';
  });

  header.appendChild(time);
  header.appendChild(marker);
  header.appendChild(heading);
  header.appendChild(status);
  header.appendChild(duration);
  header.appendChild(toggle);
  wrap.appendChild(header);
  wrap.appendChild(bodyWrap);
  applyTaskCardMetadata(wrap);
  appendOutputActions(wrap);
  wrap.dataset.searchText = (options.label + ' ' + options.filename + ' ' + text.textContent).toLowerCase();
  $('output').appendChild(wrap);
  const artifact = rememberArtifact(options);
  if (!options.historical && artifact) showToast('Artifact ready — ' + options.filename);
  applyOutputSearch();
  if (shouldScroll) scrollOutputToBottom();
  else updateOutputControls();
  updateOutputEmptyState();
  return artifact;
}

function rememberArtifact(options) {
  if (!options || !options.filename) return;
  const key = options.key || (options.short + ':' + (options.archiveFilename || options.filename));
  const existing = artifactLibrary.find(item => item.key === key);
  if (existing) {
    if (!existing.base64Value && options.base64Value) existing.base64Value = options.base64Value;
    return existing;
  }
  const artifact = {
    key,
    serverID: options.serverID || '',
    taskID: options.taskID || options.short,
    type: options.type || '',
    label: options.label,
    filename: options.filename,
    archiveFilename: options.archiveFilename || options.filename,
    mime: options.mime || 'application/octet-stream',
    base64Value: options.base64Value,
    sizeBytes: Number(options.sizeBytes) || (options.base64Value ? Math.floor(String(options.base64Value).length * 3 / 4) : 0),
    compress: Boolean(options.compress),
    timestamp: options.ts,
    createdAt: options.createdAt || new Date().toISOString(),
    serverSynced: Boolean(options.serverSynced),
    summary: options.summary || '',
    warnings: Array.isArray(options.warnings) ? options.warnings.slice() : [],
  };
  artifactLibrary.unshift(artifact);
	const retention = activeAgent && activeAgent.artifact_retention ? activeAgent.artifact_retention : 256;
	if (artifactLibrary.length > retention) artifactLibrary.length = retention;
  renderArtifactList();
  if (!artifact.serverSynced && artifact.base64Value) persistArtifact(artifact);
  return artifact;
}

async function persistArtifact(artifact) {
  if (!activeAgentID || !artifact || !artifact.base64Value) return;
  try {
    const resp = await apiFetch('/api/agents/' + activeAgentID + '/artifacts', {
      method: 'POST',
      body: JSON.stringify({
        key: artifact.key,
        task_id: artifact.taskID,
        type: artifact.type,
        label: artifact.label,
        filename: artifact.filename,
        archive_filename: artifact.archiveFilename,
        mime: artifact.mime,
        data: artifact.base64Value,
        compress: artifact.compress,
        created_at: artifact.createdAt,
      }),
    });
    if (!resp.ok) return;
    const saved = await resp.json();
    artifact.serverID = saved.id || artifact.serverID;
    artifact.sizeBytes = Number(saved.size_bytes) || artifact.sizeBytes || 0;
    artifact.serverSynced = true;
    renderArtifactList();
    renderSessionPanels();
  } catch (_) {
    // Keep the local artifact available even if persistence is temporarily unavailable.
  }
}

function renderArtifactList() {
  const list = $('artifact-list');
  if (!list) return;
  list.textContent = '';
  if (!artifactLibrary.length) {
    list.appendChild(panelText('No artifacts captured in this view.'));
    return;
  }
  artifactLibrary.forEach(item => {
    const row = panelItem(item.label || 'artifact', item.filename);
    row.dataset.artifactKey = item.key || '';
    row.appendChild(panelHint((item.serverSynced ? 'Server artifact' : 'Local artifact') + ' - ' + (item.archiveFilename || item.filename) + (item.summary ? ' - ' + item.summary : '')));
    row.appendChild(panelButton('Save', () => saveArtifact(item)));
	 if (item.serverID) row.appendChild(panelButton('Delete', () => deleteArtifact(item)));
    list.appendChild(row);
  });
}

async function deleteArtifact(item) {
  if (!activeAgentID || !item || !item.serverID) return;
  if (!window.confirm('Delete retained artifact ' + item.filename + '?')) return;
  try {
    const resp = await apiFetch('/api/agents/' + activeAgentID + '/artifacts/' + encodeURIComponent(item.serverID), { method: 'DELETE' });
    if (!resp.ok) {
      appendOutput('[-] delete artifact failed (' + resp.status + ')', '', activeAgentID, 'error');
      return;
    }
    artifactLibrary = artifactLibrary.filter(candidate => candidate !== item);
    renderSessionPanels();
  } catch (err) {
    appendOutput('[-] delete artifact error: ' + err.message, '', activeAgentID, 'error');
  }
}

async function saveArtifact(item) {
  if (!item) return;
  if (!item.base64Value && item.serverID) {
    await hydrateArtifactData(item);
  }
  if (!item.base64Value) {
    appendOutput('[-] artifact data is not available for ' + (item.filename || 'artifact'), '', activeAgentID, 'error');
    return;
  }
  if (item && item.compress) {
    await triggerCompressedDownload(item.base64Value, item.filename, item.archiveFilename || item.filename + '.zip');
    return;
  }
  triggerDownload(item.base64Value, item.archiveFilename || item.filename, item.mime);
}

async function saveArtifactByKey(key) {
  const item = artifactLibrary.find(artifact => artifact.key === key);
  if (!item) {
    showToast('The transfer is ready, but its retained artifact has not loaded yet.', 'error');
    return;
  }
  await saveArtifact(item);
  showToast('Saving ' + (item.archiveFilename || item.filename) + '.');
}

async function hydrateArtifactData(item) {
  if (!activeAgentID || !item || !item.serverID) return;
  try {
    const resp = await apiFetch('/api/agents/' + activeAgentID + '/artifacts/' + encodeURIComponent(item.serverID));
    if (!resp.ok) return;
    const data = await resp.json();
    item.base64Value = data.data || item.base64Value || '';
    item.mime = data.mime || item.mime;
    item.compress = Boolean(data.compress);
    item.archiveFilename = data.archive_filename || item.archiveFilename;
    item.filename = data.filename || item.filename;
  } catch (_) {
    // Save will show a user-visible failure if the data is still unavailable.
  }
}

async function triggerCompressedDownload(base64Value, innerFilename, archiveFilename) {
  const fileBytes = base64ToBytes(base64Value);
  const zipBytes = await createZipArchive(innerFilename, fileBytes);
  const blob = new Blob([zipBytes], { type: 'application/zip' });
  triggerBlobDownload(blob, archiveFilename);
}

function triggerDownload(base64Value, filename, mime) {
  const bytes = base64ToBytes(base64Value);
  const blob = new Blob([bytes], mime ? { type: mime } : undefined);
  triggerBlobDownload(blob, filename);
}

function triggerBlobDownload(blob, filename) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = sanitizeDownloadName(filename);
  document.body.appendChild(anchor);
  anchor.click();
  document.body.removeChild(anchor);
  URL.revokeObjectURL(url);
}

function base64ToBytes(base64Value) {
  const binary = atob(base64Value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

async function createZipArchive(filename, fileBytes) {
  const nameBytes = new TextEncoder().encode(sanitizeArchiveEntryName(filename));
  const crc = crc32(fileBytes);
  const compressed = await deflateRaw(fileBytes);
  const useDeflate = compressed && compressed.length < fileBytes.length;
  const body = useDeflate ? compressed : fileBytes;
  const method = useDeflate ? 8 : 0;
  const now = new Date();
  const dosTime = ((now.getHours() & 31) << 11) | ((now.getMinutes() & 63) << 5) | ((Math.floor(now.getSeconds() / 2)) & 31);
  const dosDate = (((now.getFullYear() - 1980) & 127) << 9) | (((now.getMonth() + 1) & 15) << 5) | (now.getDate() & 31);

  const local = new Uint8Array(30 + nameBytes.length);
  const view = new DataView(local.buffer);
  writeZipHeader(view, 0x04034b50, 0);
  view.setUint16(4, 20, true);
  view.setUint16(8, method, true);
  view.setUint16(10, dosTime, true);
  view.setUint16(12, dosDate, true);
  view.setUint32(14, crc, true);
  view.setUint32(18, body.length, true);
  view.setUint32(22, fileBytes.length, true);
  view.setUint16(26, nameBytes.length, true);
  local.set(nameBytes, 30);

  const central = new Uint8Array(46 + nameBytes.length);
  const centralView = new DataView(central.buffer);
  writeZipHeader(centralView, 0x02014b50, 0);
  centralView.setUint16(4, 20, true);
  centralView.setUint16(6, 20, true);
  centralView.setUint16(10, method, true);
  centralView.setUint16(12, dosTime, true);
  centralView.setUint16(14, dosDate, true);
  centralView.setUint32(16, crc, true);
  centralView.setUint32(20, body.length, true);
  centralView.setUint32(24, fileBytes.length, true);
  centralView.setUint16(28, nameBytes.length, true);
  central.set(nameBytes, 46);

  const eocd = new Uint8Array(22);
  const eocdView = new DataView(eocd.buffer);
  writeZipHeader(eocdView, 0x06054b50, 0);
  eocdView.setUint16(8, 1, true);
  eocdView.setUint16(10, 1, true);
  eocdView.setUint32(12, central.length, true);
  eocdView.setUint32(16, local.length + body.length, true);

  const out = new Uint8Array(local.length + body.length + central.length + eocd.length);
  out.set(local, 0);
  out.set(body, local.length);
  out.set(central, local.length + body.length);
  out.set(eocd, local.length + body.length + central.length);
  return out;
}

function writeZipHeader(view, signature, offset) {
  view.setUint32(offset, signature, true);
}

async function deflateRaw(bytes) {
  if (typeof CompressionStream !== 'function') return null;
  try {
    const stream = new Blob([bytes]).stream().pipeThrough(new CompressionStream('deflate-raw'));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  } catch (_) {
    return null;
  }
}

function sanitizeArchiveEntryName(filename) {
  return String(filename || 'download.bin')
    .replace(/^[a-zA-Z]:/, '')
    .replace(/[\\/]+/g, '_')
    .replace(/[\u0000-\u001f<>:"|?*]/g, '_')
    .replace(/^\.+$/, 'download.bin')
    .slice(0, 180) || 'download.bin';
}

function sanitizeDownloadName(filename) {
  return sanitizeArchiveEntryName(filename)
    .replace(/^\.+/, '')
    .trim()
    .slice(0, 180) || 'download.bin';
}

function crc32(bytes) {
  if (!crcTable) {
    crcTable = new Uint32Array(256);
    for (let i = 0; i < 256; i++) {
      let c = i;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      crcTable[i] = c >>> 0;
    }
  }
  let c = 0xffffffff;
  for (let i = 0; i < bytes.length; i++) c = crcTable[(c ^ bytes[i]) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function baseTransferTaskID(taskID) {
  const value = String(taskID || '');
  for (const marker of ['-download-', '-archive-']) {
    const idx = value.indexOf(marker);
    if (idx > 0) return value.slice(0, idx);
  }
  return value;
}

function baseDownloadTaskID(taskID) {
  return baseTransferTaskID(taskID);
}
