// Pairpad frontend — Phase 1 scaffold
// Connects to server, renders nested file tree, basic text editing with save.

let ws = null;
let openFiles = new Map(); // path -> content string
let activeFile = null;
let sessionId = null;

// --- Connection ---

function joinSession() {
  let input = document.getElementById('session-input').value.trim();
  if (!input) return;

  // Support pasting a full URL — extract the hash
  if (input.includes('#')) {
    input = input.split('#').pop();
  }

  sessionId = input;
  const err = document.getElementById('connect-error');
  err.textContent = '';

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = `${protocol}//${location.host}/ws/browser?session=${sessionId}`;

  ws = new WebSocket(url);

  ws.onopen = () => {
    document.getElementById('connect-overlay').style.display = 'none';
    document.getElementById('ide').style.display = 'flex';
    document.getElementById('session-id-display').textContent = sessionId;
    setStatus('Connected');
  };

  ws.onclose = (e) => {
    if (document.getElementById('ide').style.display === 'flex') {
      setStatus('Disconnected — session ended or daemon stopped');
    } else {
      err.textContent = 'Could not connect. Check the session ID and try again.';
    }
  };

  ws.onerror = () => {
    err.textContent = 'Connection failed.';
  };

  ws.onmessage = (event) => {
    try {
      handleMessage(JSON.parse(event.data));
    } catch (e) {
      console.error('Failed to handle message:', e);
    }
  };
}

// Allow Enter key to submit
document.getElementById('session-input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') joinSession();
});

// --- Message handling ---

function handleMessage(envelope) {
  const payload = JSON.parse(atob(envelope.payload));

  switch (envelope.type) {
    case 'file_tree':
      renderFileTree(payload.files);
      break;
    case 'file_content':
      openFiles.set(payload.path, decodeContent(payload.content));
      addTab(payload.path);
      if (activeFile === payload.path) renderEditor();
      break;
    case 'file_changed':
      openFiles.set(payload.path, decodeContent(payload.content));
      if (activeFile === payload.path) renderEditor();
      break;
    case 'file_deleted':
      openFiles.delete(payload.path);
      removeTab(payload.path);
      if (activeFile === payload.path) {
        activeFile = null;
        renderEditor();
      }
      break;
    case 'participant_info':
      document.getElementById('participant-count').textContent = payload.count;
      break;
  }
}

function decodeContent(b64) {
  const bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

// --- File tree ---

function renderFileTree(files) {
  // Build a nested structure from flat paths
  const root = { children: {}, isDir: true };

  for (const file of files) {
    const parts = file.path.split('/');
    let node = root;
    for (let i = 0; i < parts.length; i++) {
      const name = parts[i];
      if (!node.children[name]) {
        node.children[name] = {
          name: name,
          path: parts.slice(0, i + 1).join('/'),
          isDir: i < parts.length - 1 || file.is_dir,
          size: file.size,
          children: {},
        };
      }
      node = node.children[name];
    }
  }

  const container = document.getElementById('file-tree');
  container.innerHTML = '';
  renderTreeNode(root, container, 0);
}

function renderTreeNode(node, container, depth) {
  // Sort: directories first, then alphabetical
  const entries = Object.values(node.children).sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    return a.name.localeCompare(b.name);
  });

  for (const entry of entries) {
    const item = document.createElement('div');
    item.className = `tree-item ${entry.isDir ? 'tree-dir' : 'tree-file'}`;
    item.style.paddingLeft = `${12 + depth * 16}px`;

    const icon = document.createElement('span');
    icon.className = 'icon';

    const label = document.createElement('span');
    label.className = 'tree-label';
    label.textContent = entry.name;

    if (entry.isDir) {
      icon.textContent = '\u25BE'; // down triangle
      item.appendChild(icon);
      item.appendChild(label);

      const children = document.createElement('div');
      children.className = 'children';
      renderTreeNode(entry, children, depth + 1);

      item.addEventListener('click', (e) => {
        e.stopPropagation();
        item.classList.toggle('collapsed');
        icon.textContent = item.classList.contains('collapsed') ? '\u25B8' : '\u25BE';
      });

      container.appendChild(item);
      container.appendChild(children);
    } else {
      icon.textContent = '\u2847'; // braille dot for file
      item.appendChild(icon);
      item.appendChild(label);
      item.addEventListener('click', (e) => {
        e.stopPropagation();
        openFile(entry.path);
      });
      container.appendChild(item);
    }
  }
}

// --- File operations ---

function openFile(path) {
  activeFile = path;
  if (openFiles.has(path)) {
    addTab(path);
    renderEditor();
  } else {
    send('open_file', { path });
  }
}

// --- Tabs ---

function addTab(path) {
  const tabs = document.getElementById('tabs');
  let tab = tabs.querySelector(`[data-path="${CSS.escape(path)}"]`);

  if (!tab) {
    tab = document.createElement('div');
    tab.className = 'tab';
    tab.dataset.path = path;

    const label = document.createElement('span');
    label.textContent = path.split('/').pop();
    tab.appendChild(label);

    const close = document.createElement('span');
    close.className = 'close';
    close.textContent = '\u00d7';
    close.addEventListener('click', (e) => {
      e.stopPropagation();
      closeTab(path);
    });
    tab.appendChild(close);

    tab.addEventListener('click', () => {
      activeFile = path;
      activateTab(path);
      renderEditor();
    });

    tabs.appendChild(tab);
  }

  activateTab(path);
}

function activateTab(path) {
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  const tab = document.querySelector(`.tab[data-path="${CSS.escape(path)}"]`);
  if (tab) tab.classList.add('active');

  // Also highlight in file tree
  document.querySelectorAll('.tree-item').forEach(t => t.classList.remove('active'));
}

function closeTab(path) {
  const tab = document.querySelector(`.tab[data-path="${CSS.escape(path)}"]`);
  if (tab) tab.remove();
  openFiles.delete(path);
  if (activeFile === path) {
    // Switch to the last remaining tab, if any
    const remaining = document.querySelector('.tab');
    if (remaining) {
      activeFile = remaining.dataset.path;
      activateTab(activeFile);
    } else {
      activeFile = null;
    }
    renderEditor();
  }
}

function removeTab(path) {
  closeTab(path);
}

// --- Editor ---

function renderEditor() {
  const container = document.getElementById('editor-container');
  if (!activeFile || !openFiles.has(activeFile)) {
    container.innerHTML = '';
    return;
  }

  let textarea = container.querySelector('textarea');
  if (!textarea) {
    textarea = document.createElement('textarea');
    textarea.spellcheck = false;
    textarea.addEventListener('keydown', (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault();
        saveFile();
      }
      // Tab key inserts spaces
      if (e.key === 'Tab') {
        e.preventDefault();
        const start = textarea.selectionStart;
        const end = textarea.selectionEnd;
        textarea.value = textarea.value.substring(0, start) + '    ' + textarea.value.substring(end);
        textarea.selectionStart = textarea.selectionEnd = start + 4;
      }
    });
    container.innerHTML = '';
    container.appendChild(textarea);
  }

  textarea.value = openFiles.get(activeFile);
  setStatus(activeFile);
}

function saveFile() {
  if (!activeFile) return;
  const content = document.querySelector('#editor-container textarea')?.value;
  if (content == null) return;

  openFiles.set(activeFile, content);
  const encoded = btoa(new TextEncoder().encode(content).reduce((s, b) => s + String.fromCharCode(b), ''));
  send('save_file', { path: activeFile, content: encoded });
  setStatus(`Saved ${activeFile}`);
}

// --- WebSocket send ---

function send(type, payload) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  ws.send(JSON.stringify({
    type,
    payload: btoa(JSON.stringify(payload)),
  }));
}

// --- Status ---

function setStatus(text) {
  document.getElementById('status-text').textContent = text;
}

// --- Auto-join from URL hash ---

if (location.hash && location.hash.length > 1) {
  document.getElementById('session-input').value = location.hash.slice(1);
  joinSession();
}
