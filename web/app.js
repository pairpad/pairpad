import { initEditor, openFileInEditor, updateFileContent, getEditorContent, closeFileInEditor, setOnSave, setOnCursorChange, getCursorLine, getCurrentPath, scrollToLine, updateCommentMarkers, getTopVisibleLine, getSelectionLines, scrollToTopLine, setGuideHighlight, setGuideCursorLine, setPeerHighlights, updateTourMarkers, setEditorTheme, getEditorTheme, setEditorEditable, getView } from './editor.js';
import { getSymbolAtLine, reanchorBySymbol } from './symbols.js';

let ws = null;
let openFiles = new Map(); // path -> content string
let activeFile = null;
let fileTreeEntries = []; // latest file tree from daemon
let cursorState = []; // latest cursor_state from server

// Guide mode state
let guideActive = false;     // am I the guide?
let guideName = null;        // who is guiding (null = nobody)
let guideColor = null;
let following = false;       // am I following the guide?
let lastGuideState = null;   // latest guide viewport
let suppressBreakAway = false; // prevent breaking away during programmatic scrolls
let followers = new Map();     // name -> bool (who is following the guide)
let creatingTour = false;      // are we in tour creation mode?
let sessionId = null;
let userName = null;
let myColor = null;
let myRole = null;
let hostToken = null;
let editorView = null;

// --- Connection (two-step: session ID, then name) ---

window.submitSession = function() {
  let input = document.getElementById('session-input').value.trim();
  if (!input) return;

  // Support pasting a full URL — extract the hash and query params
  if (input.includes('#')) {
    input = input.split('#').pop();
  }

  // Extract host token if present (e.g. "sessionid?host=abc123")
  if (input.includes('?')) {
    const parts = input.split('?');
    input = parts[0];
    const params = new URLSearchParams(parts[1]);
    hostToken = params.get('host') || null;
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
    // Send identify with host token if we have one
    const identifyMsg = { name: userName };
    if (hostToken) identifyMsg.host_token = hostToken;
    send('identify', identifyMsg);

    document.getElementById('connect-overlay').style.display = 'none';
    document.getElementById('ide').style.display = 'flex';
    document.getElementById('session-id-display').textContent = sessionId;
    setStatus('Connected');

    // Initialize the CodeMirror editor
    const container = document.getElementById('editor-container');
    editorView = initEditor(container, saveFile);
    setOnSave(saveFile);

    // Send cursor updates debounced + guide state
    let cursorTimer = null;
    setOnCursorChange(() => {
      clearTimeout(cursorTimer);
      cursorTimer = setTimeout(() => {
        sendCursorUpdate();
        broadcastGuideState();
      }, 50);

      // Break away from following when user interacts manually
      if (following && !guideActive && !suppressBreakAway) {
        following = false;
        send('follow_status', { following: false });
        updateGuideUI();
      }
    });

    // Also broadcast guide state on scroll (CodeMirror's scroller)
    const scroller = document.querySelector('.cm-scroller');
    if (scroller) {
      scroller.addEventListener('scroll', () => broadcastGuideState(), { passive: true });
    }
  };

  ws.onclose = (e) => {
    if (document.getElementById('ide').style.display === 'flex') {
      // Already in a session — try to reconnect
      document.getElementById('reconnect-banner').style.display = 'block';
      setStatus('Reconnecting...');
      setTimeout(() => reconnect(), 2000);
    } else {
      err.textContent = e.code === 1006
        ? 'Could not connect to relay. Is it running?'
        : 'Session not found. Check the session ID and try again.';
      document.getElementById('step-name').style.display = 'none';
      document.getElementById('step-session').style.display = '';
    }
  };

  ws.onerror = () => {}; // onclose handles the error display

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
    case 'daemon_status':
      if (payload.connected) {
        document.getElementById('reconnect-banner').style.display = 'none';
        setStatus('Connected');
      } else {
        document.getElementById('reconnect-banner').style.display = 'block';
        document.getElementById('reconnect-banner').textContent = 'Host disconnected. Waiting for reconnection...';
        setStatus('Host disconnected');
      }
      break;
    case 'your_color':
      myColor = payload.color;
      if (payload.project_name) {
        document.title = `Pairpad — ${payload.project_name}`;
      }
      break;
    case 'file_tree':
      fileTreeEntries = payload.files || [];
      renderFileTree(fileTreeEntries);
      break;
    case 'file_content': {
      const content = decodeContent(payload.content);
      openFiles.set(payload.path, content);
      addTab(payload.path);
      openFileInEditor(payload.path, content);
      activateTab(payload.path);
      highlightFileInTree(payload.path);
      setStatus(payload.path);
      sendCursorUpdate();
      // Handle pending scroll from jumpToParticipant
      if (pendingScroll && pendingScroll.file === payload.path) {
        scrollToLine(pendingScroll.line);
        pendingScroll = null;
      }
      // Handle pending guide state
      if (pendingGuideState && pendingGuideState.file === payload.path) {
        applyGuideState(pendingGuideState);
        pendingGuideState = null;
      }
      // Handle pending tour step
      if (pendingTourStep != null) {
        const idx = pendingTourStep;
        pendingTourStep = null;
        goToTourStep(idx);
      }
      // Delay marker refresh to let the new editor state settle
      requestAnimationFrame(() => {
        refreshCommentGutter();
        refreshTourMarkers();
      });
      break;
    }
    case 'file_changed': {
      const changed = decodeContent(payload.content);
      openFiles.set(payload.path, changed);
      updateFileContent(payload.path, changed);
      // Refresh markers and re-anchor annotations via AST
      if (payload.path === getCurrentPath()) {
        requestAnimationFrame(() => {
          refreshCommentGutter();
          refreshTourMarkers();
          reanchorAnnotationsForFile(payload.path);
        });
      }
      break;
    }
    case 'file_created': {
      const content = decodeContent(payload.content);
      openFiles.set(payload.path, content);
      // Add to file tree if not already there
      if (!fileTreeEntries.some(f => f.path === payload.path)) {
        fileTreeEntries.push({ path: payload.path, size: content.length, is_dir: false });
        renderFileTree(fileTreeEntries);
      }
      break;
    }
    case 'file_deleted':
      openFiles.delete(payload.path);
      removeTab(payload.path);
      closeFileInEditor(payload.path);
      // Remove from file tree
      fileTreeEntries = fileTreeEntries.filter(f => f.path !== payload.path);
      renderFileTree(fileTreeEntries);
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
      renderPeerSelections();
      break;
    case 'comment_list':
      handleCommentList(payload.comments || []);
      break;
    case 'guide_start':
      handleGuideStart(payload);
      break;
    case 'guide_stop':
      handleGuideStop();
      break;
    case 'guide_state':
      handleGuideState(payload);
      break;
    case 'follow_status':
      handleFollowStatus(payload);
      break;
    case 'tour_list':
      handleTourList(payload.tours || []);
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

  // Track own role
  const me = participants.find(p => p.name === userName);
  const prevRole = myRole;
  if (me) myRole = me.role;

  // Toast on role change (not on initial load)
  if (prevRole && myRole && prevRole !== myRole) {
    showRoleToast(myRole);
  }

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
      name.textContent = `${p.name} (you, ${p.role})`;
    } else {
      name.textContent = `${p.name} (${p.role})`;
      // Clickable — jump to their cursor location
      badge.style.cursor = 'pointer';
      badge.title = 'Click to jump to their location';
      badge.addEventListener('click', (e) => {
        // If host and right-click area, show role menu
        if (e.detail === 2 && myRole === 'host') {
          showRoleMenu(p.name, p.role, badge);
        } else {
          jumpToParticipant(p.name);
        }
      });
      // Host can right-click to change role
      if (myRole === 'host') {
        badge.addEventListener('contextmenu', (e) => {
          e.preventDefault();
          showRoleMenu(p.name, p.role, badge);
        });
      }
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

  applyRoleRestrictions();

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

function renderPeerSelections() {
  const currentFile = getCurrentPath();
  if (!currentFile) {
    setPeerHighlights([]);
    return;
  }

  const selections = [];
  for (const cursor of cursorState) {
    if (cursor.name === userName) continue;
    if (cursor.file !== currentFile) continue;
    if (cursor.selection_from && cursor.selection_to && cursor.selection_from !== cursor.selection_to) {
      selections.push({
        fromLine: cursor.selection_from,
        toLine: cursor.selection_to,
        color: cursor.color,
      });
    }
  }
  setPeerHighlights(selections);
}

function matchesTreeItem(item, filePath) {
  // Reconstruct path from the tree item by walking up through siblings/parents
  // Simpler approach: store path as data attribute
  return item.dataset.filePath === filePath;
}

// --- AST Re-anchoring ---

function reanchorAnnotationsForFile(file) {
  const v = getView();
  if (!v) return;

  let changed = false;
  const updatedComments = [];
  const updatedTours = [];

  // Re-anchor comments for this file
  for (const c of comments) {
    if (c.file !== file || c.parent_id) continue;
    if (!c.symbol_path) continue;

    const newLine = reanchorBySymbol(v, c.symbol_path, c.symbol_offset || 0);
    if (newLine !== null && newLine !== c.line) {
      const updated = { ...c, line: newLine, orphaned: false };
      updatedComments.push(updated);
      changed = true;
    } else if (newLine === null && !c.orphaned) {
      const updated = { ...c, orphaned: true };
      updatedComments.push(updated);
      changed = true;
    }
  }

  // Re-anchor tour steps for this file
  for (const tour of allTours) {
    let tourChanged = false;
    const updatedSteps = tour.steps.map(step => {
      if (step.file !== file || !step.symbol_path) return step;
      const newLine = reanchorBySymbol(v, step.symbol_path, step.symbol_offset || 0);
      if (newLine !== null && newLine !== step.line) {
        tourChanged = true;
        return { ...step, line: newLine, orphaned: false };
      } else if (newLine === null && !step.orphaned) {
        tourChanged = true;
        return { ...step, orphaned: true };
      }
      return step;
    });
    if (tourChanged) {
      updatedTours.push({ ...tour, steps: updatedSteps });
      changed = true;
    }
  }

  // Send corrected annotations to relay
  if (changed) {
    const msg = {};
    if (updatedComments.length > 0) msg.comments = updatedComments;
    if (updatedTours.length > 0) msg.tours = updatedTours;
    send('reanchor', msg);
  }
}

function updateCommentToggleBadge() {
  const btn = document.getElementById('comment-toggle-btn');
  if (!btn) return;
  const unresolvedCount = comments.filter(c => !c.parent_id && !c.resolved).length;
  // Update badge
  let badge = btn.querySelector('.badge');
  if (unresolvedCount > 0) {
    if (!badge) {
      badge = document.createElement('span');
      badge.className = 'badge';
      btn.appendChild(badge);
    }
    badge.textContent = unresolvedCount;
  } else if (badge) {
    badge.remove();
  }
  // Auto-open sidebar when project has comments (first load)
  if (unresolvedCount > 0 && !document.getElementById('comment-sidebar').classList.contains('was-toggled')) {
    document.getElementById('comment-sidebar').classList.remove('hidden');
  }
}

// --- Roles ---

function showRoleMenu(targetName, currentRole, anchorEl) {
  // Remove any existing menu
  document.querySelectorAll('.role-menu').forEach(el => el.remove());

  const menu = document.createElement('div');
  menu.className = 'role-menu';
  menu.style.cssText = `position:absolute;background:var(--bg-secondary);border:1px solid var(--border);border-radius:4px;padding:4px 0;z-index:300;font-size:12px;min-width:120px;`;

  const rect = anchorEl.getBoundingClientRect();
  menu.style.top = (rect.bottom + 4) + 'px';
  menu.style.left = rect.left + 'px';

  for (const role of ['editor', 'commenter']) {
    const item = document.createElement('div');
    item.style.cssText = `padding:4px 12px;cursor:pointer;color:var(--text-primary);`;
    item.textContent = role;
    if (role === currentRole) {
      item.style.fontWeight = '700';
      item.style.color = 'var(--accent)';
    }
    item.addEventListener('mouseenter', () => { item.style.background = 'var(--bg-hover)'; });
    item.addEventListener('mouseleave', () => { item.style.background = ''; });
    item.addEventListener('click', () => {
      send('set_role', { target_name: targetName, role });
      menu.remove();
    });
    menu.appendChild(item);
  }

  document.body.appendChild(menu);

  // Close on click outside
  const close = (e) => {
    if (!menu.contains(e.target)) {
      menu.remove();
      document.removeEventListener('click', close);
    }
  };
  setTimeout(() => document.addEventListener('click', close), 0);
}

function showRoleToast(role) {
  const container = document.getElementById('toast-container');
  const toast = document.createElement('div');
  toast.className = 'toast';
  toast.textContent = `You are now ${role === 'editor' ? 'an' : 'a'} ${role}`;
  container.appendChild(toast);
  setTimeout(() => toast.remove(), 3000);
}

function applyRoleRestrictions() {
  const canEdit = myRole === 'host' || myRole === 'editor';

  // Guide button: host only
  const guideBtn = document.getElementById('guide-btn');
  if (guideBtn) {
    guideBtn.style.display = myRole === 'host' ? '' : 'none';
  }

  // Tour creation: editor or host
  const tourActions = document.getElementById('tour-actions');
  if (tourActions) {
    tourActions.style.display = canEdit ? 'flex' : 'none';
  }

  // Editor: read-only for commenters
  setEditorEditable(canEdit);
}

// --- File tree ---

const collapsedDirs = new Set(); // tracks which directory paths are collapsed

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

  // Re-apply decorations after tree rebuild
  renderFileTreePresence();
  renderFileTreeCommentBadges();
  renderTourFileTreeIndicators();

  // Re-highlight active file
  if (activeFile) {
    const item = document.querySelector(`.tree-item[data-file-path="${CSS.escape(activeFile)}"]`);
    if (item) item.classList.add('active');
  }
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
      const isCollapsed = collapsedDirs.has(entry.path);
      icon.textContent = isCollapsed ? '\u25B8' : '\u25BE';
      if (isCollapsed) item.classList.add('collapsed');
      item.appendChild(icon);
      item.appendChild(label);

      const children = document.createElement('div');
      children.className = 'children';
      if (isCollapsed) children.style.display = 'none';
      renderTreeNode(entry, children, depth + 1);

      item.addEventListener('click', (e) => {
        e.stopPropagation();
        const nowCollapsed = !item.classList.contains('collapsed');
        item.classList.toggle('collapsed');
        icon.textContent = nowCollapsed ? '\u25B8' : '\u25BE';
        children.style.display = nowCollapsed ? 'none' : '';
        if (nowCollapsed) {
          collapsedDirs.add(entry.path);
        } else {
          collapsedDirs.delete(entry.path);
        }
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

function highlightFileInTree(path) {
  // Expand parent directories to reveal the file
  const parts = path.split('/');
  let needsRerender = false;
  for (let i = 1; i < parts.length; i++) {
    const dirPath = parts.slice(0, i).join('/');
    if (collapsedDirs.has(dirPath)) {
      collapsedDirs.delete(dirPath);
      needsRerender = true;
    }
  }

  if (needsRerender) {
    renderFileTree(fileTreeEntries);
  } else {
    // Just update the highlight without full re-render
    document.querySelectorAll('.tree-item.active').forEach(el => el.classList.remove('active'));
    const item = document.querySelector(`.tree-item[data-file-path="${CSS.escape(path)}"]`);
    if (item) item.classList.add('active');
  }

  // Scroll the active file into view
  const item = document.querySelector(`.tree-item[data-file-path="${CSS.escape(path)}"]`);
  if (item) item.scrollIntoView({ block: 'nearest' });
}

// --- File operations ---

function openFile(path) {
  activeFile = path;
  highlightFileInTree(path);
  if (openFiles.has(path)) {
    addTab(path);
    openFileInEditor(path, openFiles.get(path));
    activateTab(path);
    setStatus(path);
    sendCursorUpdate();
    requestAnimationFrame(() => {
      refreshCommentGutter();
      renderPeerSelections();
      refreshTourMarkers();
    });
  } else {
    send('open_file', { path });
  }
}

function sendCursorUpdate() {
  const file = getCurrentPath();
  if (!file) return;
  const sel = getSelectionLines();
  send('cursor_update', {
    file,
    line: getCursorLine(),
    selection_from: sel ? sel.from : 0,
    selection_to: sel ? sel.to : 0,
  });
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
      highlightFileInTree(path);
      if (openFiles.has(path)) {
        openFileInEditor(path, openFiles.get(path));
      }
      setStatus(path);
      sendCursorUpdate();
      refreshCommentGutter();
      broadcastGuideState();
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
  if (myRole !== 'host' && myRole !== 'editor') return;
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

// --- Comments ---

let comments = [];
let previousCommentIds = new Set();

function refreshCommentGutter() {
  const file = getCurrentPath();
  if (!file) return;
  const entries = [];
  for (const c of comments) {
    if (c.file !== file || c.parent_id || c.resolved || c.orphaned) continue;
    entries.push({ line: c.line, lineEnd: c.line_end || 0, color: c.color });
  }
  updateCommentMarkers(entries);
}

function handleCommentList(newComments) {
  const oldComments = comments;
  comments = newComments;

  // Detect new comments for toasts
  const newIds = new Set(newComments.map(c => c.id));
  for (const c of newComments) {
    if (!previousCommentIds.has(c.id) && c.author !== userName && !c.parent_id) {
      showToast(c);
    }
  }
  previousCommentIds = newIds;

  renderCommentFeed();
  renderFileTreeCommentBadges();
  refreshCommentGutter();
  updateCommentToggleBadge();

  // Auto-close sidebar when all comments are deleted
  if (comments.length === 0) {
    document.getElementById('comment-sidebar').classList.add('hidden');
  }
}

function renderCommentFeed() {
  const feed = document.getElementById('comment-feed');
  feed.innerHTML = '';

  // Group into threads: root comments and their replies
  const roots = comments.filter(c => !c.parent_id);
  const replies = comments.filter(c => c.parent_id);

  for (const root of roots) {
    const thread = document.createElement('div');
    const classes = ['comment-thread'];
    if (root.resolved) classes.push('resolved');
    if (root.orphaned) classes.push('orphaned');
    thread.className = classes.join(' ');
    thread.dataset.commentId = root.id;

    // Orphaned badge
    if (root.orphaned) {
      const badge = document.createElement('div');
      badge.className = 'comment-orphaned-badge';
      badge.textContent = 'Code changed — this comment may no longer apply';
      thread.appendChild(badge);
    }

    // Location link — always clickable, even for orphaned (jumps to last-known location)
    const loc = document.createElement('div');
    loc.className = 'comment-location';
    loc.textContent = formatLocation(root.file, root.line, root.line_end);
    loc.addEventListener('click', () => jumpToComment(root));
    thread.appendChild(loc);

    // Root comment
    thread.appendChild(renderCommentEntry(root));

    // Replies
    const threadReplies = replies.filter(r => r.parent_id === root.id);
    for (const reply of threadReplies) {
      const replyEl = renderCommentEntry(reply);
      replyEl.style.paddingLeft = '12px';
      replyEl.style.borderLeft = `2px solid ${reply.color}`;
      replyEl.style.marginLeft = '8px';
      thread.appendChild(replyEl);
    }

    // Actions
    const actions = document.createElement('div');
    actions.className = 'comment-actions';

    const replyBtn = document.createElement('button');
    replyBtn.textContent = 'Reply';
    replyBtn.addEventListener('click', () => {
      const input = thread.querySelector('.comment-reply-input');
      input.style.display = input.style.display === 'none' ? 'block' : 'none';
      if (input.style.display === 'block') input.querySelector('input').focus();
    });
    actions.appendChild(replyBtn);

    const resolveBtn = document.createElement('button');
    resolveBtn.textContent = root.resolved ? 'Unresolve' : 'Resolve';
    resolveBtn.addEventListener('click', () => {
      send('comment_resolve', { comment_id: root.id });
    });
    actions.appendChild(resolveBtn);

    const deleteBtn = document.createElement('button');
    deleteBtn.textContent = 'Delete';
    deleteBtn.style.color = 'var(--error)';
    deleteBtn.addEventListener('click', () => {
      if (confirm('Delete this comment and all replies?')) {
        send('comment_delete', { comment_id: root.id });
      }
    });
    actions.appendChild(deleteBtn);

    thread.appendChild(actions);

    // Reply input
    const replyInput = document.createElement('div');
    replyInput.className = 'comment-reply-input';
    replyInput.style.display = 'none';
    const input = document.createElement('input');
    input.placeholder = 'Write a reply...';
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && input.value.trim()) {
        send('comment_reply', { parent_id: root.id, body: input.value.trim() });
        input.value = '';
        replyInput.style.display = 'none';
      }
      if (e.key === 'Escape') {
        replyInput.style.display = 'none';
      }
    });
    replyInput.appendChild(input);
    thread.appendChild(replyInput);

    feed.appendChild(thread);
  }
}

function renderCommentEntry(comment) {
  const entry = document.createElement('div');
  entry.className = 'comment-entry';

  const header = document.createElement('div');
  const author = document.createElement('span');
  author.className = 'comment-author';
  author.style.color = comment.color;
  author.textContent = comment.author;
  header.appendChild(author);

  const time = document.createElement('span');
  time.className = 'comment-time';
  const date = new Date(comment.timestamp);
  time.textContent = date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  header.appendChild(time);

  entry.appendChild(header);

  const body = document.createElement('div');
  body.className = 'comment-body';
  body.textContent = comment.body;
  entry.appendChild(body);

  return entry;
}

function jumpToComment(comment) {
  activeFile = comment.file;
  if (openFiles.has(comment.file)) {
    addTab(comment.file);
    openFileInEditor(comment.file, openFiles.get(comment.file));
    activateTab(comment.file);
    scrollToLine(comment.line);
    setStatus(comment.file);
  } else {
    pendingScroll = { file: comment.file, line: comment.line };
    send('open_file', { path: comment.file });
  }
}

// Add comment from gutter click — exposed globally for CodeMirror
// Unified gutter click handler — routes to tour creation, tour navigation, or comments
window.onGutterClick = function(line) {
  // Get selection range — if user selected multiple lines, use the range
  const sel = getSelectionLines();
  const lineEnd = (sel && sel.from !== sel.to) ? sel.to : 0;

  if (creatingTour) {
    window.addTourStepAtLine(line, lineEnd);
    return;
  }
  // If a tour is active and this line has a step, navigate to it
  if (activeTour) {
    const currentFile = getCurrentPath();
    const idx = activeTour.steps.findIndex(s => s.file === currentFile && s.line === line);
    if (idx >= 0) {
      goToTourStep(idx);
      return;
    }
  }
  window.addCommentAtLine(line, lineEnd);
};

window.addCommentAtLine = function(line, lineEnd) {

  const file = getCurrentPath();
  if (!file) return;

  // Show the comment sidebar
  const sidebar = document.getElementById('comment-sidebar');
  sidebar.classList.remove('hidden');

  // If a comment exists on this line, scroll to it instead of creating a new one
  const existing = comments.find(c => c.file === file && c.line === line && !c.parent_id);
  if (existing) {
    const thread = document.querySelector(`.comment-thread[data-comment-id="${CSS.escape(existing.id)}"]`);
    if (thread) {
      thread.scrollIntoView({ behavior: 'smooth', block: 'center' });
      thread.style.outline = '1px solid #89b4fa';
      setTimeout(() => { thread.style.outline = ''; }, 1500);
      return;
    }
  }

  // Create a temporary input at the bottom of the feed
  const feed = document.getElementById('comment-feed');
  let tempInput = feed.querySelector('.temp-comment-input');
  if (tempInput) tempInput.remove();

  tempInput = document.createElement('div');
  tempInput.className = 'comment-thread temp-comment-input';

  const loc = document.createElement('div');
  loc.className = 'comment-location';
  loc.textContent = formatLocation(file, line, lineEnd);
  tempInput.appendChild(loc);

  const input = document.createElement('input');
  input.placeholder = 'Write a comment...';
  input.style.cssText = 'width:100%;background:#313244;border:1px solid #45475a;color:#cdd6f4;padding:6px 8px;border-radius:4px;font-size:12px;outline:none;';
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && input.value.trim()) {
      const sym = getSymbolAtLine(getView(), line);
      send('comment_add', {
        file, line, line_end: lineEnd || 0,
        body: input.value.trim(),
        symbol_path: sym ? sym.symbolPath : '',
        symbol_offset: sym ? sym.symbolOffset : 0,
      });
      tempInput.remove();
    }
    if (e.key === 'Escape') {
      tempInput.remove();
    }
  });
  tempInput.appendChild(input);
  feed.appendChild(tempInput);
  feed.scrollTop = feed.scrollHeight;
  input.focus();
};

// Toggle comment sidebar
window.toggleCommentSidebar = function() {
  const sidebar = document.getElementById('comment-sidebar');
  sidebar.classList.toggle('hidden');
  sidebar.classList.add('was-toggled');
};

// File tree comment badges
function renderFileTreeCommentBadges() {
  // Clear existing badges
  document.querySelectorAll('.comment-count-badge').forEach(el => el.remove());

  // Count unresolved root comments per file
  const counts = {};
  for (const c of comments) {
    if (!c.parent_id && !c.resolved && !c.orphaned) {
      counts[c.file] = (counts[c.file] || 0) + 1;
    }
  }

  for (const [file, count] of Object.entries(counts)) {
    const item = document.querySelector(`.tree-item[data-file-path="${CSS.escape(file)}"]`);
    if (item) {
      const badge = document.createElement('span');
      badge.className = 'comment-count-badge';
      badge.textContent = count;
      item.appendChild(badge);
    }
  }
}

// Toast notifications
function showToast(comment) {
  const container = document.getElementById('toast-container');
  const toast = document.createElement('div');
  toast.className = 'toast';
  const toastAuthor = document.createElement('span');
  toastAuthor.className = 'toast-author';
  toastAuthor.style.color = comment.color;
  toastAuthor.textContent = comment.author;
  toast.appendChild(toastAuthor);
  toast.appendChild(document.createTextNode(' commented'));
  toast.appendChild(document.createElement('br'));
  const toastLoc = document.createElement('span');
  toastLoc.className = 'toast-location';
  toastLoc.textContent = formatLocation(comment.file, comment.line, comment.line_end);
  toast.appendChild(toastLoc);
  toast.addEventListener('click', () => {
    jumpToComment(comment);
    toast.remove();
  });
  container.appendChild(toast);
  setTimeout(() => toast.remove(), 5000);
}

// --- Guide mode ---

window.toggleGuide = function() {
  if (guideActive) {
    // Stop guiding
    guideActive = false;
    guideName = null;
    guideColor = null;
    lastGuideState = null;
    send('guide_stop', {});
    updateGuideUI();
  } else {
    // Start guiding
    guideActive = true;
    send('guide_start', { name: userName, color: myColor });
    updateGuideUI();
    broadcastGuideState();
  }
};

window.toggleFollow = function() {
  if (!guideName || guideName === userName) return;
  following = !following;
  send('follow_status', { following });
  updateGuideUI();
  if (following && lastGuideState) {
    applyGuideState(lastGuideState);
  }
};

function handleGuideStart(payload) {
  guideName = payload.name;
  guideColor = payload.color;
  followers.clear();
  // Auto-follow if someone else starts guiding (not yourself)
  if (guideName !== userName) {
    following = true;
    send('follow_status', { following: true });
  }
  updateGuideUI();
}

function handleFollowStatus(payload) {
  followers.set(payload.name, payload.following);
  updateGuideUI();
  // Update participant badges with follow indicators
  renderFollowIndicators();
}

function renderFollowIndicators() {
  // Add/remove follow indicators on participant badges
  document.querySelectorAll('.participant-badge .follow-indicator').forEach(el => el.remove());

  if (!guideName) return;

  for (const [name, isFollowing] of followers) {
    const badge = document.querySelector(`.participant-badge[data-name="${CSS.escape(name)}"]`);
    if (badge) {
      const indicator = document.createElement('span');
      indicator.className = 'follow-indicator';
      indicator.textContent = isFollowing ? '\u{1F441}' : '';
      indicator.title = isFollowing ? 'Following the guide' : 'Not following';
      badge.appendChild(indicator);
    }
  }
}

function handleGuideStop() {
  // Don't reset our own guideActive — that's handled by toggleGuide
  if (guideName === userName) return;
  guideName = null;
  guideColor = null;
  following = false;
  lastGuideState = null;
  followers.clear();
  setGuideHighlight(null, null, null);
  setGuideCursorLine(null, null);
  renderFollowIndicators();
  updateGuideUI();
}

function handleGuideState(state) {
  lastGuideState = state;
  if (following && guideName !== userName) {
    applyGuideState(state);
  }
}

function applyGuideState(state) {
  suppressBreakAway = true;
  setTimeout(() => { suppressBreakAway = false; }, 200);

  // Sync tour state from guide
  if (state.tour_id) {
    const guideTour = allTours.find(t => t.id === state.tour_id);
    if (guideTour && (!activeTour || activeTour.id !== state.tour_id)) {
      // Activate the same tour
      document.getElementById('tour-bar').style.display = 'flex';
      populateTourSelect();
      document.getElementById('tour-select').value = state.tour_id;
      activeTour = guideTour;
      renderTourFileTreeIndicators();
    }
    if (guideTour && state.tour_step >= 0 && state.tour_step !== activeTourStep) {
      activeTourStep = state.tour_step;
      const step = guideTour.steps[activeTourStep];
      if (step) {
        document.getElementById('tour-step-panel').style.display = 'block';
        document.getElementById('tour-step-title').textContent = `${activeTourStep + 1}. ${step.title}`;
        document.getElementById('tour-step-desc').textContent = step.description;
        document.getElementById('tour-step-counter').textContent = `${activeTourStep + 1} / ${guideTour.steps.length}`;
        document.getElementById('tour-prev').disabled = activeTourStep === 0;
        document.getElementById('tour-next').disabled = activeTourStep === guideTour.steps.length - 1;
        // Apply tour range highlight on follower after file loads
        pendingTourHighlight = step;
      }
    }
  } else if (activeTour && guideName && guideName !== userName) {
    // Guide stopped their tour — close ours too
    closeTour();
  }

  // Open the file if needed
  if (getCurrentPath() !== state.file) {
    if (openFiles.has(state.file)) {
      activeFile = state.file;
      addTab(state.file);
      openFileInEditor(state.file, openFiles.get(state.file));
      activateTab(state.file);
      setStatus(state.file);
    } else {
      // Request the file, then apply guide state after it loads
      pendingGuideState = state;
      send('open_file', { path: state.file });
      return;
    }
  }

  // Scroll to the guide's top line and apply highlights
  // Use requestAnimationFrame to ensure the editor has settled after file switch
  requestAnimationFrame(() => {
    if (state.top_line) {
      scrollToTopLine(state.top_line);
    }

    // Show guide's cursor line
    if (state.cursor_line) {
      setGuideCursorLine(state.cursor_line, guideColor);
    } else {
      setGuideCursorLine(null, null);
    }

    // Show tour range highlight if a tour step has a range
    if (pendingTourHighlight) {
      const step = pendingTourHighlight;
      pendingTourHighlight = null;
      if (step.line_end && step.line_end > step.line) {
        setGuideHighlight(step.line, step.line_end, TOUR_COLOR);
      }
    } else if (activeTour && activeTourStep >= 0) {
      // Keep tour highlight active while viewing a tour step
      const step = activeTour.steps[activeTourStep];
      if (step && step.line_end && step.line_end > step.line && getCurrentPath() === step.file) {
        setGuideHighlight(step.line, step.line_end, TOUR_COLOR);
      } else if (state.selection_from && state.selection_to) {
        setGuideHighlight(state.selection_from, state.selection_to, guideColor);
      } else {
        setGuideHighlight(null, null, null);
      }
    } else if (state.selection_from && state.selection_to) {
      setGuideHighlight(state.selection_from, state.selection_to, guideColor);
    } else {
      setGuideHighlight(null, null, null);
    }

    refreshTourMarkers();
  });
}

let pendingGuideState = null;
let pendingTourHighlight = null;

// Broadcast guide state — called when the guide scrolls, selects, or switches files
let guideStateRaf = null;
function broadcastGuideState() {
  if (!guideActive) return;
  cancelAnimationFrame(guideStateRaf);
  guideStateRaf = requestAnimationFrame(() => {
    const file = getCurrentPath();
    if (!file) return;
    const sel = getSelectionLines();
    const state = {
      file,
      top_line: getTopVisibleLine(),
      cursor_line: getCursorLine(),
      selection_from: sel ? sel.from : 0,
      selection_to: sel ? sel.to : 0,
    };
    // Include tour state if a tour is active
    if (activeTour) {
      state.tour_id = activeTour.id;
      state.tour_step = activeTourStep;
    }
    send('guide_state', state);
  });
}

function updateGuideUI() {
  const btn = document.getElementById('guide-btn');
  const banner = document.getElementById('guide-banner');
  const bannerText = document.getElementById('guide-banner-text');
  const followBtn = document.getElementById('follow-btn');

  if (!btn || !banner || !bannerText || !followBtn) return;

  if (guideActive) {
    btn.textContent = 'Stop Guiding';
    btn.classList.add('active');
    banner.style.display = 'flex';
    const followCount = [...followers.values()].filter(Boolean).length;
    const totalOthers = participants.length - 1;
    const countText = totalOthers > 0 ? ` (${followCount}/${totalOthers} following)` : '';
    bannerText.textContent = `You are guiding this session${countText}`;
    banner.style.background = myColor + '22';
    banner.style.borderColor = myColor;
    followBtn.style.display = 'none';
  } else if (guideName && guideName !== userName) {
    btn.style.display = 'none';
    banner.style.display = 'flex';
    bannerText.textContent = `${guideName} is guiding`;
    bannerText.style.color = guideColor;
    banner.style.background = guideColor + '22';
    banner.style.borderColor = guideColor;
    followBtn.style.display = '';
    followBtn.textContent = following ? 'Unfollow' : 'Follow';
    followBtn.classList.toggle('active', following);
  } else {
    btn.textContent = 'Guide';
    btn.classList.remove('active');
    btn.style.display = '';
    banner.style.display = 'none';
    followBtn.style.display = 'none';
  }
}

// --- Tour mode ---

let allTours = [];
let activeTour = null;   // current Tour object
let activeTourStep = -1; // current step index

const TOUR_COLOR = '#e5c890'; // yellow from Catppuccin Frappe

function handleTourList(tours) {
  allTours = tours;
  const btn = document.getElementById('tour-toggle-btn');
  if (btn) {
    btn.style.display = '';
  }
  // Refresh the dropdown if tour bar is visible
  const bar = document.getElementById('tour-bar');
  if (bar && bar.style.display === 'flex') {
    const prevId = activeTour ? activeTour.id : pendingTourSelect;
    pendingTourSelect = null;
    populateTourSelect();
    if (prevId) {
      const prevStep = activeTourStep;
      // Update the tour data without navigating
      activeTour = allTours.find(t => t.id === prevId) || null;
      if (activeTour && prevStep >= 0) {
        activeTourStep = Math.min(prevStep, activeTour.steps.length - 1);
      }
      const sel = document.getElementById('tour-select');
      sel.value = prevId;
      refreshTourMarkers();
      refreshCommentGutter();
      renderTourFileTreeIndicators();
    }
  }
}

window.toggleTourBar = function() {
  const bar = document.getElementById('tour-bar');
  if (bar.style.display === 'flex') {
    closeTour();
  } else {
    bar.style.display = 'flex';
    populateTourSelect();
    if (allTours.length > 0) {
      selectTourById(allTours[0].id);
    }
  }
};

function populateTourSelect() {
  const sel = document.getElementById('tour-select');
  sel.innerHTML = '';
  for (const tour of allTours) {
    const opt = document.createElement('option');
    opt.value = tour.id;
    opt.textContent = tour.title;
    sel.appendChild(opt);
  }
}

window.selectTour = function() {
  const sel = document.getElementById('tour-select');
  selectTourById(sel.value);
};

function selectTourById(id) {
  activeTour = allTours.find(t => t.id === id) || null;
  activeTourStep = -1;
  if (activeTour && activeTour.steps.length > 0) {
    goToTourStep(0);
  }
  refreshTourMarkers();
  refreshCommentGutter();
  renderTourFileTreeIndicators();
}

window.closeTour = function() {
  document.getElementById('tour-bar').style.display = 'none';
  document.getElementById('tour-step-panel').style.display = 'none';
  activeTour = null;
  activeTourStep = -1;
  updateTourMarkers([]);
  renderTourFileTreeIndicators();
};

window.tourPrev = function() {
  if (!activeTour || activeTourStep <= 0) return;
  goToTourStep(activeTourStep - 1);
};

window.tourNext = function() {
  if (!activeTour || activeTourStep >= activeTour.steps.length - 1) return;
  goToTourStep(activeTourStep + 1);
};

function goToTourStep(idx) {
  if (!activeTour || idx < 0 || idx >= activeTour.steps.length) return;
  activeTourStep = idx;
  const step = activeTour.steps[idx];

  // Update step panel
  document.getElementById('tour-step-panel').style.display = 'block';
  document.getElementById('tour-step-title').textContent = `${idx + 1}. ${step.title}`;
  document.getElementById('tour-step-desc').textContent = step.description;

  // Update counter and nav buttons
  document.getElementById('tour-step-counter').textContent = `${idx + 1} / ${activeTour.steps.length}`;
  document.getElementById('tour-prev').disabled = idx === 0;
  document.getElementById('tour-next').disabled = idx === activeTour.steps.length - 1;

  // Open the file and scroll to the line
  activeFile = step.file;
  if (openFiles.has(step.file)) {
    addTab(step.file);
    openFileInEditor(step.file, openFiles.get(step.file));
    activateTab(step.file);
    scrollToLine(step.line);
    setStatus(step.file);
    requestAnimationFrame(() => {
      refreshTourMarkers();
      refreshCommentGutter();
      // Highlight range if step has one
      if (step.line_end && step.line_end > step.line) {
        setGuideHighlight(step.line, step.line_end, TOUR_COLOR);
      } else {
        setGuideHighlight(null, null, null);
      }
    });
  } else {
    pendingTourStep = idx;
    send('open_file', { path: step.file });
  }
}

let pendingTourStep = null;

// Gutter marker click — jump to that step
window.onTourMarkerClick = function(lineNum) {
  if (!activeTour) return;
  const currentFile = getCurrentPath();
  const idx = activeTour.steps.findIndex(s => s.file === currentFile && s.line === lineNum);
  if (idx >= 0) {
    goToTourStep(idx);
  }
};

// Refresh tour markers for the current file
function refreshTourMarkers() {
  if (!activeTour) {
    updateTourMarkers([]);
    return;
  }
  const currentFile = getCurrentPath();
  const steps = [];
  for (let i = 0; i < activeTour.steps.length; i++) {
    const step = activeTour.steps[i];
    if (step.file === currentFile) {
      steps.push({ stepNum: i + 1, line: step.line, color: TOUR_COLOR });
    }
  }
  updateTourMarkers(steps);
}

// Show indicators in file tree for files that are in the active tour
function renderTourFileTreeIndicators() {
  document.querySelectorAll('.tree-tour-indicator').forEach(el => el.remove());
  if (!activeTour) return;

  const filesInTour = new Set(activeTour.steps.map(s => s.file));
  for (const file of filesInTour) {
    const item = document.querySelector(`.tree-item[data-file-path="${CSS.escape(file)}"]`);
    if (item) {
      const indicator = document.createElement('span');
      indicator.className = 'tree-tour-indicator';
      indicator.textContent = '\u{1F4D6}';
      item.appendChild(indicator);
    }
  }
}

// --- Tour creation ---

let newTourSteps = [];

let editingTourId = null; // non-null when editing an existing tour
let pendingTourSelect = null; // tour ID to auto-select after tour_list refresh

window.startTourCreation = function() {
  editingTourId = null;
  creatingTour = true;
  newTourSteps = [];
  activeTour = null;
  activeTourStep = -1;
  updateTourMarkers([]);
  showTourCreationPanel('', '');
};

window.deleteCurrentTour = function() {
  const id = editingTourId || (activeTour ? activeTour.id : null);
  if (!id) return;
  const title = (activeTour ? activeTour.title : '') || id;
  if (!confirm(`Delete tour "${title}"?`)) return;
  send('tour_delete', { id });
  if (creatingTour) cancelTourCreation();
  activeTour = null;
  activeTourStep = -1;
  updateTourMarkers([]);
  renderTourFileTreeIndicators();
  document.getElementById('tour-step-panel').style.display = 'none';
};

window.editCurrentTour = function() {
  if (!activeTour) return;
  editingTourId = activeTour.id;
  creatingTour = true;
  newTourSteps = activeTour.steps.map(s => ({ ...s }));
  activeTourStep = -1;
  showTourCreationPanel(activeTour.title, activeTour.description || '');
  renderNewTourSteps();
  refreshCreationMarkers();
};

function showTourCreationPanel(title, desc) {
  const panel = document.getElementById('tour-create-panel');
  panel.style.display = 'block';
  const deleteBtn = editingTourId
    ? `<button class="tour-cancel-btn" style="color:#f38ba8;" onclick="deleteCurrentTour()">Delete Tour</button>`
    : '';
  panel.innerHTML = `
    <input id="new-tour-title" placeholder="Tour title" value="${escapeAttr(title)}" autofocus>
    <input id="new-tour-desc" placeholder="Tour description (optional)" value="${escapeAttr(desc)}">
    <div class="tour-create-hint">Click any line number in the gutter to add a step</div>
    <div id="new-tour-steps"></div>
    <div class="tour-create-actions">
      <button class="tour-save-btn" onclick="saveTour()">Save</button>
      <button class="tour-cancel-btn" onclick="cancelTourCreation()">Cancel</button>
      ${deleteBtn}
    </div>
  `;

  // Hide the regular tour nav and actions
  document.getElementById('tour-select').style.display = 'none';
  document.querySelector('.tour-nav').style.display = 'none';
  document.getElementById('tour-actions').style.display = 'none';
  document.getElementById('tour-step-panel').style.display = 'none';
}

window.cancelTourCreation = function() {
  creatingTour = false;
  editingTourId = null;
  reanchoringStepIdx = null;
  newTourSteps = [];
  document.getElementById('tour-create-panel').style.display = 'none';
  document.getElementById('tour-select').style.display = '';
  document.querySelector('.tour-nav').style.display = '';
  document.getElementById('tour-actions').style.display = 'flex';
  updateTourMarkers([]);
};

// Gutter click during tour creation — add a step
window.addTourStepAtLine = function(line, lineEnd) {
  const file = getCurrentPath();
  if (!file) return;

  // If re-anchoring an existing step, update it instead of adding
  if (reanchoringStepIdx !== null && reanchoringStepIdx < newTourSteps.length) {
    newTourSteps[reanchoringStepIdx].file = file;
    newTourSteps[reanchoringStepIdx].line = line;
    newTourSteps[reanchoringStepIdx].line_end = lineEnd || 0;
    reanchoringStepIdx = null;
    renderNewTourSteps();
    refreshCreationMarkers();
    return;
  }

  newTourSteps.push({ file, line, line_end: lineEnd || 0, title: '', description: '' });
  renderNewTourSteps();
  refreshCreationMarkers();

  // Focus the new step's title input
  const lastInput = document.querySelector(`#new-tour-steps .tour-step-edit:last-child input[data-field="title"]`);
  if (lastInput) {
    lastInput.focus();
    lastInput.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }
};

let reanchoringStepIdx = null; // index of step being re-anchored via gutter click

function renderNewTourSteps() {
  const container = document.getElementById('new-tour-steps');
  if (!container) return;
  container.innerHTML = '';

  for (let i = 0; i < newTourSteps.length; i++) {
    const step = newTourSteps[i];
    const div = document.createElement('div');
    div.className = 'tour-step-edit';
    if (reanchoringStepIdx === i) div.classList.add('reanchoring');
    div.innerHTML = `
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:4px;">
        <div class="step-location"><span class="step-goto" data-idx="${i}" style="cursor:pointer;" title="Go to this step">${i + 1}. ${escapeHtml(step.file.split('/').pop())}</span>:<input type="number" class="step-line-input" value="${step.line}" data-idx="${i}" min="1" style="width:50px;background:#45475a;border:1px solid #585b70;color:#cdd6f4;padding:1px 4px;border-radius:3px;font-size:11px;"></div>
        <div style="display:flex;gap:6px;font-size:11px;">
          ${i > 0 ? `<span class="step-move" data-idx="${i}" data-dir="up" style="cursor:pointer;color:#a6adc8;" title="Move up">\u25B2</span>` : ''}
          ${i < newTourSteps.length - 1 ? `<span class="step-move" data-idx="${i}" data-dir="down" style="cursor:pointer;color:#a6adc8;" title="Move down">\u25BC</span>` : ''}
          <span class="step-reanchor" data-idx="${i}" style="cursor:pointer;color:#89b4fa;">${reanchoringStepIdx === i ? 'click a line...' : 're-anchor'}</span>
          <span class="step-remove" data-idx="${i}" style="cursor:pointer;color:#f38ba8;">remove</span>
        </div>
      </div>
      <input placeholder="Step title" value="${escapeAttr(step.title)}" data-idx="${i}" data-field="title">
      <textarea placeholder="Description" data-idx="${i}" data-field="description">${escapeHtml(step.description)}</textarea>
    `;
    container.appendChild(div);
  }

  // Bind events
  container.querySelectorAll('input[data-field], textarea[data-field]').forEach(el => {
    el.addEventListener('input', (e) => {
      const idx = parseInt(e.target.dataset.idx);
      const field = e.target.dataset.field;
      newTourSteps[idx][field] = e.target.value;
    });
  });
  container.querySelectorAll('.step-line-input').forEach(el => {
    el.addEventListener('change', (e) => {
      const idx = parseInt(e.target.dataset.idx);
      const val = parseInt(e.target.value);
      if (val > 0) {
        newTourSteps[idx].line = val;
        refreshCreationMarkers();
      }
    });
  });
  container.querySelectorAll('.step-remove').forEach(el => {
    el.addEventListener('click', (e) => {
      const idx = parseInt(e.target.dataset.idx);
      newTourSteps.splice(idx, 1);
      if (reanchoringStepIdx === idx) reanchoringStepIdx = null;
      renderNewTourSteps();
      refreshCreationMarkers();
    });
  });
  container.querySelectorAll('.step-move').forEach(el => {
    el.addEventListener('click', (e) => {
      const idx = parseInt(e.target.dataset.idx);
      const dir = e.target.dataset.dir;
      if (dir === 'up' && idx > 0) {
        [newTourSteps[idx - 1], newTourSteps[idx]] = [newTourSteps[idx], newTourSteps[idx - 1]];
      } else if (dir === 'down' && idx < newTourSteps.length - 1) {
        [newTourSteps[idx], newTourSteps[idx + 1]] = [newTourSteps[idx + 1], newTourSteps[idx]];
      }
      renderNewTourSteps();
      refreshCreationMarkers();
    });
  });
  container.querySelectorAll('.step-goto').forEach(el => {
    el.addEventListener('click', (e) => {
      const idx = parseInt(e.target.dataset.idx);
      const step = newTourSteps[idx];
      if (!step) return;
      activeFile = step.file;
      if (openFiles.has(step.file)) {
        addTab(step.file);
        openFileInEditor(step.file, openFiles.get(step.file));
        activateTab(step.file);
        scrollToLine(step.line);
        setStatus(step.file);
        requestAnimationFrame(() => refreshCreationMarkers());
      } else {
        pendingScroll = { file: step.file, line: step.line };
        send('open_file', { path: step.file });
      }
    });
  });
  container.querySelectorAll('.step-reanchor').forEach(el => {
    el.addEventListener('click', (e) => {
      const idx = parseInt(e.target.dataset.idx);
      if (reanchoringStepIdx === idx) {
        reanchoringStepIdx = null; // toggle off
      } else {
        reanchoringStepIdx = idx;
      }
      renderNewTourSteps();
    });
  });
}

function refreshCreationMarkers() {
  const currentFile = getCurrentPath();
  const steps = [];
  for (let i = 0; i < newTourSteps.length; i++) {
    if (newTourSteps[i].file === currentFile) {
      steps.push({ stepNum: i + 1, line: newTourSteps[i].line, color: TOUR_COLOR });
    }
  }
  updateTourMarkers(steps);
}

window.saveTour = function() {
  const title = document.getElementById('new-tour-title')?.value.trim();
  if (!title) {
    document.getElementById('new-tour-title').focus();
    return;
  }
  if (newTourSteps.length === 0) return;

  const tour = {
    id: editingTourId || title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, ''),
    title,
    description: document.getElementById('new-tour-desc')?.value.trim() || '',
    steps: newTourSteps.map(s => ({
      file: s.file,
      line: s.line,
      line_end: s.line_end || 0,
      title: s.title || `Step at ${formatLocation(s.file, s.line, s.line_end)}`,
      description: s.description || '',
    })),
  };

  pendingTourSelect = tour.id;
  send('tour_save', tour);
  cancelTourCreation();
};

function formatLocation(file, line, lineEnd) {
  const name = file.split('/').pop();
  if (lineEnd && lineEnd > line) {
    return `${name}:${line}-${lineEnd}`;
  }
  return `${name}:${line}`;
}

function escapeAttr(s) {
  return s.replace(/"/g, '&quot;').replace(/</g, '&lt;');
}
function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// --- Reconnection ---

function reconnect() {
  const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = `${wsProtocol}//${location.host}/ws/browser?session=${sessionId}`;

  ws = new WebSocket(url);

  ws.onopen = () => {
    // Re-identify
    const identifyMsg = { name: userName };
    if (hostToken) identifyMsg.host_token = hostToken;
    send('identify', identifyMsg);
    document.getElementById('reconnect-banner').style.display = 'none';
    setStatus('Reconnected');
    sendCursorUpdate();
  };

  ws.onclose = () => {
    document.getElementById('reconnect-banner').style.display = 'block';
    setStatus('Reconnecting...');
    setTimeout(() => reconnect(), 2000);
  };

  ws.onerror = () => {};

  ws.onmessage = (event) => {
    try {
      handleMessage(JSON.parse(event.data));
    } catch (e) {
      console.error('Failed to handle message:', e);
    }
  };
}

// --- Quick file picker (Ctrl+P) ---

let filePickerActive = false;
let filePickerIndex = 0;
let filePickerMatches = [];

document.addEventListener('keydown', (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key === 'p') {
    e.preventDefault();
    if (filePickerActive) {
      closeFilePicker();
    } else {
      openFilePicker();
    }
  }
  if (e.key === 'Escape' && filePickerActive) {
    closeFilePicker();
  }
});

function openFilePicker() {
  filePickerActive = true;
  filePickerIndex = 0;
  const overlay = document.getElementById('file-picker-overlay');
  overlay.style.display = 'flex';
  const input = document.getElementById('file-picker-input');
  input.value = '';
  input.focus();
  updateFilePickerResults('');

  input.oninput = () => {
    filePickerIndex = 0;
    updateFilePickerResults(input.value);
  };

  input.onkeydown = (e) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      filePickerIndex = Math.min(filePickerIndex + 1, filePickerMatches.length - 1);
      renderFilePickerResults();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      filePickerIndex = Math.max(filePickerIndex - 1, 0);
      renderFilePickerResults();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (filePickerMatches[filePickerIndex]) {
        openFile(filePickerMatches[filePickerIndex].path);
        closeFilePicker();
      }
    }
  };
}

function closeFilePicker() {
  filePickerActive = false;
  document.getElementById('file-picker-overlay').style.display = 'none';
}

function updateFilePickerResults(query) {
  const files = fileTreeEntries.filter(f => !f.is_dir);
  if (!query) {
    filePickerMatches = files.slice(0, 50);
  } else {
    const lower = query.toLowerCase();
    filePickerMatches = files
      .map(f => ({ ...f, score: fuzzyScore(f.path.toLowerCase(), lower) }))
      .filter(f => f.score > 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, 50);
  }
  renderFilePickerResults();
}

function renderFilePickerResults() {
  const container = document.getElementById('file-picker-results');
  container.innerHTML = '';
  const query = document.getElementById('file-picker-input').value.toLowerCase();

  for (let i = 0; i < filePickerMatches.length; i++) {
    const item = document.createElement('div');
    item.className = `file-picker-item${i === filePickerIndex ? ' active' : ''}`;
    if (query) {
      item.innerHTML = highlightMatch(filePickerMatches[i].path, query);
    } else {
      item.textContent = filePickerMatches[i].path;
    }
    const idx = i;
    item.addEventListener('click', () => {
      openFile(filePickerMatches[idx].path);
      closeFilePicker();
    });
    item.addEventListener('mouseenter', () => {
      filePickerIndex = idx;
      renderFilePickerResults();
    });
    container.appendChild(item);
  }

  // Scroll active item into view
  const active = container.querySelector('.active');
  if (active) active.scrollIntoView({ block: 'nearest' });
}

function fuzzyScore(str, query) {
  let si = 0, qi = 0, score = 0;
  while (si < str.length && qi < query.length) {
    if (str[si] === query[qi]) {
      score++;
      // Bonus for consecutive matches
      if (si > 0 && str[si - 1] === query[qi - 1]) score++;
      // Bonus for matching after separator
      if (si === 0 || str[si - 1] === '/' || str[si - 1] === '.') score += 2;
      qi++;
    }
    si++;
  }
  return qi === query.length ? score : 0;
}

function highlightMatch(path, query) {
  let result = '';
  let qi = 0;
  for (let si = 0; si < path.length && qi <= query.length; si++) {
    if (qi < query.length && path[si].toLowerCase() === query[qi]) {
      result += `<span class="file-picker-match">${escapeHtml(path[si])}</span>`;
      qi++;
    } else {
      result += escapeHtml(path[si]);
    }
  }
  return result;
}

// Close picker on overlay click
document.getElementById('file-picker-overlay').addEventListener('click', (e) => {
  if (e.target === e.currentTarget) closeFilePicker();
});

// --- Theme toggle ---

window.toggleTheme = function() {
  const current = getEditorTheme();
  const next = current === 'dark' ? 'light' : 'dark';
  setEditorTheme(next);
  document.documentElement.className = `theme-${next}`;
  localStorage.setItem('pairpad-theme', next);
  updateThemeButton();
};

function updateThemeButton() {
  const btn = document.getElementById('theme-btn');
  if (btn) {
    btn.textContent = getEditorTheme() === 'dark' ? '\u263E' : '\u2600';
  }
}

// Apply saved theme on load
(function() {
  const saved = localStorage.getItem('pairpad-theme') || 'dark';
  document.documentElement.className = `theme-${saved}`;
  if (saved !== 'dark') {
    // Editor theme will be applied when initEditor is called
    // For now just set the variable so it picks up the right theme
    setEditorTheme(saved);
  }
  // Set button icon after DOM is ready
  requestAnimationFrame(updateThemeButton);
})();

// --- Auto-join from URL hash ---

if (location.hash && location.hash.length > 1) {
  document.getElementById('session-input').value = location.hash.slice(1);
  window.submitSession();
}
