import { initEditor, openFileInEditor, updateFileContent, getEditorContent, closeFileInEditor, setOnSave, setOnCursorChange, getCursorLine, getCurrentPath, scrollToLine } from './editor.js';

let ws = null;
let openFiles = new Map(); // path -> content string
let activeFile = null;
let cursorState = []; // latest cursor_state from server
let sessionId = null;
let userName = null;
let myColor = null;
let editorView = null;

// --- Connection (two-step: session ID, then name) ---

window.submitSession = function() {
  let input = document.getElementById('session-input').value.trim();
  if (!input) return;

  // Support pasting a full URL — extract the hash
  if (input.includes('#')) {
    input = input.split('#').pop();
  }

  sessionId = input;
  document.getElementById('connect-error').textContent = '';

  // Move to name step
  document.getElementById('step-session').style.display = 'none';
  document.getElementById('step-name').style.display = '';

  const nameInput = document.getElementById('name-input');
  // Restore saved name from localStorage
  const saved = localStorage.getItem('pairpad-name');
  if (saved) nameInput.value = saved;
  nameInput.focus();
};

window.joinSession = function() {
  const nameInput = document.getElementById('name-input');
  userName = nameInput.value.trim();
  if (!userName) {
    nameInput.focus();
    return;
  }

  // Remember the name for next time
  localStorage.setItem('pairpad-name', userName);

  const err = document.getElementById('connect-error');
  err.textContent = '';

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = `${protocol}//${location.host}/ws/browser?session=${sessionId}`;

  ws = new WebSocket(url);

  ws.onopen = () => {
    // Send identify immediately
    send('identify', { name: userName });

    document.getElementById('connect-overlay').style.display = 'none';
    document.getElementById('ide').style.display = 'flex';
    document.getElementById('session-id-display').textContent = sessionId;
    setStatus('Connected');

    // Initialize the CodeMirror editor
    const container = document.getElementById('editor-container');
    editorView = initEditor(container, saveFile);
    setOnSave(saveFile);

    // Send cursor updates debounced
    let cursorTimer = null;
    setOnCursorChange(() => {
      clearTimeout(cursorTimer);
      cursorTimer = setTimeout(() => {
        const file = getCurrentPath();
        if (file) {
          send('cursor_update', { file, line: getCursorLine() });
        }
      }, 50);
    });
  };

  ws.onclose = () => {
    if (document.getElementById('ide').style.display === 'flex') {
      setStatus('Disconnected — session ended or daemon stopped');
    } else {
      err.textContent = 'Could not connect. Check the session ID and try again.';
      // Reset back to session step
      document.getElementById('step-name').style.display = 'none';
      document.getElementById('step-session').style.display = '';
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
};

// Enter key handlers
document.getElementById('session-input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') window.submitSession();
});
document.getElementById('name-input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') window.joinSession();
});

// --- Message handling ---

function handleMessage(envelope) {
  const payload = JSON.parse(atob(envelope.payload));

  switch (envelope.type) {
    case 'your_color':
      myColor = payload.color;
      break;
    case 'file_tree':
      renderFileTree(payload.files);
      break;
    case 'file_content': {
      const content = decodeContent(payload.content);
      openFiles.set(payload.path, content);
      addTab(payload.path);
      openFileInEditor(payload.path, content);
      activateTab(payload.path);
      setStatus(payload.path);
      // Handle pending scroll from jumpToParticipant
      if (pendingScroll && pendingScroll.file === payload.path) {
        scrollToLine(pendingScroll.line);
        pendingScroll = null;
      }
      break;
    }
    case 'file_changed': {
      const changed = decodeContent(payload.content);
      openFiles.set(payload.path, changed);
      updateFileContent(payload.path, changed);
      break;
    }
    case 'file_deleted':
      openFiles.delete(payload.path);
      removeTab(payload.path);
      closeFileInEditor(payload.path);
      if (activeFile === payload.path) {
        activeFile = null;
        switchToLastTab();
      }
      break;
    case 'participant_list':
      renderParticipants(payload.participants);
      break;
    case 'cursor_state':
      cursorState = payload.cursors || [];
      renderFileTreePresence();
      renderParticipantLocations();
      break;
  }
}

function decodeContent(b64) {
  const bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

// --- Participants ---

let participants = [];

function renderParticipants(list) {
  participants = list;
  const container = document.getElementById('participant-list');
  container.innerHTML = '';

  for (const p of participants) {
    const badge = document.createElement('div');
    badge.className = 'participant-badge';
    badge.dataset.name = p.name;

    const dot = document.createElement('span');
    dot.className = 'participant-dot';
    dot.style.background = p.color;

    const name = document.createElement('span');
    name.className = 'participant-name';
    if (p.name === userName) {
      name.classList.add('you');
      name.textContent = `${p.name} (you)`;
    } else {
      name.textContent = p.name;
      // Clickable — jump to their cursor location
      badge.style.cursor = 'pointer';
      badge.title = 'Click to jump to their location';
      badge.addEventListener('click', () => jumpToParticipant(p.name));
    }

    badge.appendChild(dot);
    badge.appendChild(name);

    // Show current file as a subtitle
    const fileLabel = document.createElement('span');
    fileLabel.className = 'participant-file';
    fileLabel.dataset.name = p.name;
    badge.appendChild(fileLabel);

    container.appendChild(badge);
  }

  // Update file labels from cursor state
  renderParticipantLocations();
}

function renderParticipantLocations() {
  for (const cursor of cursorState) {
    const label = document.querySelector(`.participant-file[data-name="${CSS.escape(cursor.name)}"]`);
    if (label && cursor.name !== userName) {
      label.textContent = cursor.file.split('/').pop();
    }
  }
}

function jumpToParticipant(name) {
  const cursor = cursorState.find(c => c.name === name);
  if (!cursor) return;

  // Open the file if not already open
  activeFile = cursor.file;
  if (openFiles.has(cursor.file)) {
    addTab(cursor.file);
    openFileInEditor(cursor.file, openFiles.get(cursor.file));
    activateTab(cursor.file);
    scrollToLine(cursor.line);
    setStatus(cursor.file);
  } else {
    // Request the file, then scroll after it loads
    pendingScroll = { file: cursor.file, line: cursor.line };
    send('open_file', { path: cursor.file });
  }
}

let pendingScroll = null;

// File tree presence — show colored dots next to files where users have cursors
function renderFileTreePresence() {
  // Clear existing presence dots
  document.querySelectorAll('.tree-presence').forEach(el => el.remove());

  for (const cursor of cursorState) {
    if (cursor.name === userName) continue;

    // Find the file tree item for this path
    const items = document.querySelectorAll('.tree-item.tree-file');
    for (const item of items) {
      const label = item.querySelector('.tree-label');
      if (!label) continue;

      // Match against the full path by checking the item text and its depth
      // We stored the path in the click handler closure, so we need to check label text
      // against the last component of the cursor file path
      if (matchesTreeItem(item, cursor.file)) {
        const dot = document.createElement('span');
        dot.className = 'tree-presence';
        dot.style.background = cursor.color;
        item.appendChild(dot);
        break;
      }
    }
  }
}

function matchesTreeItem(item, filePath) {
  // Reconstruct path from the tree item by walking up through siblings/parents
  // Simpler approach: store path as data attribute
  return item.dataset.filePath === filePath;
}

// --- File tree ---

function renderFileTree(files) {
  const root = { children: {}, isDir: true };

  for (const file of files) {
    const parts = file.path.split('/');
    let node = root;
    for (let i = 0; i < parts.length; i++) {
      const name = parts[i];
      if (!node.children[name]) {
        node.children[name] = {
          name,
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
      icon.textContent = '\u25BE';
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
      icon.textContent = '\u2847';
      item.appendChild(icon);
      item.appendChild(label);
      item.dataset.filePath = entry.path;
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
    openFileInEditor(path, openFiles.get(path));
    activateTab(path);
    setStatus(path);
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
      if (openFiles.has(path)) {
        openFileInEditor(path, openFiles.get(path));
      }
      setStatus(path);
    });

    tabs.appendChild(tab);
  }

  activeFile = path;
}

function activateTab(path) {
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  const tab = document.querySelector(`.tab[data-path="${CSS.escape(path)}"]`);
  if (tab) tab.classList.add('active');
}

function closeTab(path) {
  const tab = document.querySelector(`.tab[data-path="${CSS.escape(path)}"]`);
  if (tab) tab.remove();
  openFiles.delete(path);
  closeFileInEditor(path);
  if (activeFile === path) {
    activeFile = null;
    switchToLastTab();
  }
}

function switchToLastTab() {
  const remaining = document.querySelector('.tab');
  if (remaining) {
    const path = remaining.dataset.path;
    activeFile = path;
    activateTab(path);
    if (openFiles.has(path)) {
      openFileInEditor(path, openFiles.get(path));
    }
    setStatus(path);
  } else {
    setStatus('Connected');
  }
}

function removeTab(path) {
  closeTab(path);
}

// --- Save ---

function saveFile() {
  if (!activeFile) return;
  const content = getEditorContent();
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
  window.submitSession();
}
