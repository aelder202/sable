'use strict';

async function loadArtifacts(agentID) {
  if (!agentID || !token) return;
  try {
    const resp = await apiFetch('/api/agents/' + agentID + '/artifacts');
    if (!resp.ok) return;
    const data = await resp.json();
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
  artifactLibrary = merged.slice(0, 64);
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
    short,
    ts,
    base64Value,
    filename: sourceName,
    archiveFilename: 'download-' + short + '.zip',
    mime: 'application/zip',
    buttonLabel: 'Save Compressed',
    label: 'download ready',
    type: 'download',
    historical,
    compress: true,
    createdAt,
  });
  if (state) {
    state.status = 'done';
    state.artifactKey = artifact ? artifact.key : '';
    if (fileBrowserResult) renderFileBrowser(fileBrowserResult);
  }
}

function appendArtifactResult(short, ts, payload, label, buttonLabel, historical, createdAt) {
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
  const label = '[' + options.short + ' ' + options.ts + '] ' + options.label;
  const shouldScroll = followOutput || isOutputNearBottom();
  const wrap = document.createElement('div');
  wrap.className = 'output-download';
  wrap.dataset.outputType = 'artifact';
  wrap.dataset.searchText = (options.label + ' ' + options.filename).toLowerCase();

  const text = document.createElement('span');
  text.className = 'output-download-text';
  text.textContent = options.historical
    ? label + ' from session history. Click to save it locally.'
    : label + '. Click to save it locally.';

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

  wrap.appendChild(text);
  wrap.appendChild(button);
  appendOutputActions(wrap);
  $('output').appendChild(wrap);
  const artifact = rememberArtifact(options);
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
    taskID: options.short,
    type: options.type || '',
    label: options.label,
    filename: options.filename,
    archiveFilename: options.archiveFilename || options.filename,
    mime: options.mime || 'application/octet-stream',
    base64Value: options.base64Value,
    compress: Boolean(options.compress),
    timestamp: options.ts,
    createdAt: options.createdAt || new Date().toISOString(),
    serverSynced: Boolean(options.serverSynced),
  };
  artifactLibrary.unshift(artifact);
  if (artifactLibrary.length > 64) artifactLibrary.pop();
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
    row.appendChild(panelHint((item.serverSynced ? 'Server artifact' : 'Local artifact') + ' - ' + (item.archiveFilename || item.filename)));
    row.appendChild(panelButton('Save', () => saveArtifact(item)));
    list.appendChild(row);
  });
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

function baseDownloadTaskID(taskID) {
  const idx = String(taskID || '').indexOf('-download-');
  return idx > 0 ? taskID.slice(0, idx) : taskID;
}
