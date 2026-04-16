import { EditorView, keymap, lineNumbers, highlightActiveLineGutter, highlightSpecialChars, drawSelection, highlightActiveLine, rectangularSelection, crosshairCursor, gutter, GutterMarker, Decoration } from '@codemirror/view';
import { EditorState, StateField, StateEffect, RangeSet, Compartment } from '@codemirror/state';
import { defaultKeymap, indentWithTab, history, historyKeymap } from '@codemirror/commands';
import { syntaxHighlighting, defaultHighlightStyle, indentOnInput, bracketMatching, foldGutter, foldKeymap } from '@codemirror/language';
import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search';
import { oneDark } from '@codemirror/theme-one-dark';

// Language imports
import { javascript } from '@codemirror/lang-javascript';
import { html } from '@codemirror/lang-html';
import { css } from '@codemirror/lang-css';
import { json } from '@codemirror/lang-json';
import { markdown } from '@codemirror/lang-markdown';
import { python } from '@codemirror/lang-python';
import { go } from '@codemirror/lang-go';
import { rust } from '@codemirror/lang-rust';
import { java } from '@codemirror/lang-java';
import { cpp } from '@codemirror/lang-cpp';
import { xml } from '@codemirror/lang-xml';
import { yaml } from '@codemirror/lang-yaml';

// Map file extensions to language support
const languageMap = {
  'js': javascript,
  'mjs': javascript,
  'cjs': javascript,
  'jsx': () => javascript({ jsx: true }),
  'ts': () => javascript({ typescript: true }),
  'tsx': () => javascript({ typescript: true, jsx: true }),
  'html': html,
  'htm': html,
  'css': css,
  'scss': css,
  'less': css,
  'json': json,
  'md': markdown,
  'markdown': markdown,
  'py': python,
  'go': go,
  'rs': rust,
  'java': java,
  'c': cpp,
  'h': cpp,
  'cpp': cpp,
  'cc': cpp,
  'cxx': cpp,
  'hpp': cpp,
  'xml': xml,
  'svg': xml,
  'yaml': yaml,
  'yml': yaml,
  'toml': json, // close enough for highlighting
  'mod': go,    // go.mod
  'sum': () => null, // go.sum, no highlighting
  'Makefile': () => null,
};

function getLanguageExtension(filename) {
  // Check full filename first (e.g. "Makefile")
  const langFn = languageMap[filename];
  if (langFn) {
    const result = langFn();
    return result ? [result] : [];
  }

  // Then check extension
  const ext = filename.split('.').pop();
  const fn = languageMap[ext];
  if (fn) {
    const result = fn();
    return result ? [result] : [];
  }

  return [];
}

// --- Unified marker gutter ---
// A single gutter that shows either comment dots or tour step numbers,
// depending on what's active. Clicking the line number gutter handles actions.

const markerEffect = StateEffect.define();

class CommentMarker extends GutterMarker {
  constructor(color, rangePos) {
    super();
    this.color = color;
    this.rangePos = rangePos; // 'start', 'mid', 'end', or null (single line)
  }
  toDOM() {
    const el = document.createElement('div');
    if (this.rangePos === 'mid') {
      el.style.cssText = `width:2px;height:100%;background:${this.color || '#89b4fa'};margin:0 auto;`;
    } else {
      el.style.cssText = `width:6px;height:6px;border-radius:50%;background:${this.color || '#89b4fa'};margin:auto;`;
    }
    return el;
  }
}

class TourMarker extends GutterMarker {
  constructor(stepNum, color) {
    super();
    this.stepNum = stepNum;
    this.color = color;
  }
  toDOM() {
    const el = document.createElement('div');
    el.style.cssText = `width:16px;height:16px;border-radius:50%;background:${this.color};color:#1e1e2e;font-size:9px;font-weight:700;display:flex;align-items:center;justify-content:center;`;
    el.textContent = this.stepNum;
    return el;
  }
}

const markerField = StateField.define({
  create() { return RangeSet.empty; },
  update(markers, tr) {
    for (const e of tr.effects) {
      if (e.is(markerEffect)) {
        return e.value;
      }
    }
    return markers.map(tr.changes);
  },
});

const unifiedGutter = gutter({
  class: 'cm-marker-gutter',
  markers: (view) => view.state.field(markerField),
  domEventHandlers: {
    click(view, line) {
      const lineNum = view.state.doc.lineAt(line.from).number;
      if (window.onGutterClick) {
        window.onGutterClick(lineNum);
      }
      return true;
    },
  },
});

// Update the unified gutter markers. Tour markers take priority over comment markers.
export function updateCommentMarkers(commentLines) {
  currentCommentMarkers = commentLines;
  refreshUnifiedMarkers();
}

export function updateTourMarkers(steps) {
  currentTourMarkers = steps;
  refreshUnifiedMarkers();
}

let currentCommentMarkers = [];
let currentTourMarkers = [];

function refreshUnifiedMarkers() {
  if (!view) return;
  const markers = [];

  // Tour markers take priority
  if (currentTourMarkers.length > 0) {
    for (const { stepNum, line, color } of currentTourMarkers) {
      if (line >= 1 && line <= view.state.doc.lines) {
        const lineInfo = view.state.doc.line(line);
        markers.push(new TourMarker(stepNum, color).range(lineInfo.from));
      }
    }
  }

  // Comment markers (show on lines that don't already have a tour marker)
  // Each entry: { line, lineEnd, color }
  const tourLines = new Set(currentTourMarkers.map(s => s.line));
  for (const entry of currentCommentMarkers) {
    const { line, lineEnd, color } = entry;
    const end = (lineEnd && lineEnd > line) ? lineEnd : line;

    for (let l = line; l <= end; l++) {
      if (tourLines.has(l)) continue;
      if (l < 1 || l > view.state.doc.lines) continue;
      const lineInfo = view.state.doc.line(l);
      let rangePos = null;
      if (end > line) {
        if (l === line) rangePos = 'start';
        else if (l === end) rangePos = 'end';
        else rangePos = 'mid';
      }
      markers.push(new CommentMarker(color, rangePos).range(lineInfo.from));
    }
  }

  markers.sort((a, b) => a.from - b.from);
  view.dispatch({
    effects: markerEffect.of(RangeSet.of(markers)),
  });
}

// --- Theme management ---

const themeCompartment = new Compartment();
const editableCompartment = new Compartment();
let currentTheme = 'dark';

const lightTheme = EditorView.theme({
  '&': { backgroundColor: '#ffffff', color: '#24292e' },
  '.cm-gutters': { backgroundColor: '#f6f8fa', color: '#6a737d', borderRight: '1px solid #e1e4e8' },
  '.cm-activeLineGutter': { backgroundColor: '#e8eaed' },
  '.cm-activeLine': { backgroundColor: '#f6f8fa' },
  '.cm-selectionBackground': { backgroundColor: '#b4d5fe' },
  '&.cm-focused .cm-selectionBackground': { backgroundColor: '#b4d5fe' },
  '.cm-cursor': { borderLeftColor: '#24292e' },
}, { dark: false });

export function setEditorTheme(theme) {
  currentTheme = theme;
  if (view) {
    view.dispatch({
      effects: themeCompartment.reconfigure(theme === 'dark' ? oneDark : lightTheme),
    });
  }
}

export function getEditorTheme() {
  return currentTheme;
}

export function setEditorEditable(editable) {
  if (view) {
    view.dispatch({
      effects: editableCompartment.reconfigure(EditorView.editable.of(editable)),
    });
  }
}

// Create the base extensions shared across all editor instances
function baseExtensions(onSave) {
  return [
    markerField,
    unifiedGutter,
    peerHighlightField,
    guideHighlightField,
    guideCursorField,
    lineNumbers({
      domEventHandlers: {
        click(view, line) {
          const lineNum = view.state.doc.lineAt(line.from).number;
          if (window.onGutterClick) {
            window.onGutterClick(lineNum);
          }
          return true;
        },
      },
    }),
    highlightActiveLineGutter(),
    highlightSpecialChars(),
    history(),
    foldGutter(),
    drawSelection(),
    indentOnInput(),
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    bracketMatching(),
    closeBrackets(),
    rectangularSelection(),
    crosshairCursor(),
    highlightActiveLine(),
    highlightSelectionMatches(),
    themeCompartment.of(currentTheme === 'dark' ? oneDark : lightTheme),
    editableCompartment.of(EditorView.editable.of(true)),
    keymap.of([
      ...closeBracketsKeymap,
      ...defaultKeymap,
      ...searchKeymap,
      ...historyKeymap,
      ...foldKeymap,
      indentWithTab,
      { key: 'Mod-s', run: () => { onSave(); return true; } },
    ]),
    EditorView.theme({
      '&': { height: '100%' },
      '.cm-scroller': { overflow: 'auto' },
    }),
    EditorView.updateListener.of((update) => {
      if (update.selectionSet || update.docChanged) {
        onCursorCallback();
      }
    }),
  ];
}

// Singleton editor view
let view = null;
let currentPath = null;

// Store EditorState per file so undo history and scroll position survive tab switches
const fileStates = new Map();

export function initEditor(container, onSave) {
  const state = EditorState.create({
    doc: '',
    extensions: baseExtensions(onSave),
  });

  view = new EditorView({
    state,
    parent: container,
  });

  return view;
}

export function openFileInEditor(path, content) {
  if (!view) return;

  // Save current file's state before switching
  if (currentPath) {
    fileStates.set(currentPath, view.state);
  }

  currentPath = path;

  // Restore previous state for this file if we have one
  if (fileStates.has(path)) {
    view.setState(fileStates.get(path));
    // Clear stale markers — they'll be repopulated by requestAnimationFrame
    clearAllMarkers();
    return;
  }

  // Create new state for this file
  const filename = path.split('/').pop();
  const state = EditorState.create({
    doc: content,
    extensions: [
      ...baseExtensions(() => onSaveCallback()),
      ...getLanguageExtension(filename),
    ],
  });

  view.setState(state);
}

function clearAllMarkers() {
  if (!view) return;
  view.dispatch({
    effects: [
      markerEffect.of(RangeSet.empty),
      guideHighlightEffect.of(Decoration.none),
      guideCursorEffect.of(Decoration.none),
      peerHighlightEffect.of(Decoration.none),
    ],
  });
}

export function updateFileContent(path, content) {
  if (path === currentPath && view) {
    // Replace the entire document content while preserving as much state as possible
    const currentContent = view.state.doc.toString();
    if (currentContent !== content) {
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: content },
      });
    }
  } else if (fileStates.has(path)) {
    // Update the cached state for a background tab
    fileStates.delete(path); // Force reload on next open
  }
}

export function getEditorContent() {
  if (!view) return null;
  return view.state.doc.toString();
}

export function closeFileInEditor(path) {
  fileStates.delete(path);
  if (currentPath === path) {
    currentPath = null;
  }
}

export function getCurrentPath() {
  return currentPath;
}

export function getView() {
  return view;
}

// Callback holders — set by the app
let onSaveCallback = () => {};
export function setOnSave(fn) {
  onSaveCallback = fn;
}

let onCursorCallback = () => {};
export function setOnCursorChange(fn) {
  onCursorCallback = fn;
}

// Scroll the editor to a specific line number (1-based).
export function scrollToLine(line) {
  if (!view) return;
  const lineInfo = view.state.doc.line(Math.min(line, view.state.doc.lines));
  view.dispatch({
    selection: { anchor: lineInfo.from },
    effects: EditorView.scrollIntoView(lineInfo.from, { y: 'center' }),
  });
  view.focus();
}

// Get the current cursor line (1-based).
export function getCursorLine() {
  if (!view) return 1;
  const pos = view.state.selection.main.head;
  return view.state.doc.lineAt(pos).number;
}

// --- Guide mode support ---

// Get the top visible line (1-based).
export function getTopVisibleLine() {
  if (!view) return 1;
  const rect = view.dom.getBoundingClientRect();
  const pos = view.posAtCoords({ x: rect.left, y: rect.top + 5 });
  if (pos == null) return 1;
  return view.state.doc.lineAt(pos).number;
}

// Get the current selection range as line numbers (1-based). Returns null if no selection.
export function getSelectionLines() {
  if (!view) return null;
  const sel = view.state.selection.main;
  if (sel.from === sel.to) return null;
  return {
    from: view.state.doc.lineAt(sel.from).number,
    to: view.state.doc.lineAt(sel.to).number,
  };
}

// Scroll to make a specific line appear at the top of the viewport.
export function scrollToTopLine(line) {
  if (!view) return;
  const clampedLine = Math.max(1, Math.min(line, view.state.doc.lines));
  const lineInfo = view.state.doc.line(clampedLine);
  view.dispatch({
    effects: EditorView.scrollIntoView(lineInfo.from, { y: 'start' }),
  });
}

// Guide highlight decoration
const guideHighlightEffect = StateEffect.define();

const guideHighlightField = StateField.define({
  create() { return Decoration.none; },
  update(deco, tr) {
    for (const e of tr.effects) {
      if (e.is(guideHighlightEffect)) {
        return e.value;
      }
    }
    return deco.map(tr.changes);
  },
  provide: f => EditorView.decorations.from(f),
});

// Peer selection highlights (always-on presence)
const peerHighlightEffect = StateEffect.define();

const peerHighlightField = StateField.define({
  create() { return Decoration.none; },
  update(deco, tr) {
    for (const e of tr.effects) {
      if (e.is(peerHighlightEffect)) {
        return e.value;
      }
    }
    return deco.map(tr.changes);
  },
  provide: f => EditorView.decorations.from(f),
});

// Set all peer selection highlights at once. Each entry: { fromLine, toLine, color }
export function setPeerHighlights(selections) {
  if (!view) return;

  if (!selections || selections.length === 0) {
    view.dispatch({ effects: peerHighlightEffect.of(Decoration.none) });
    return;
  }

  const markers = [];
  for (const { fromLine, toLine, color } of selections) {
    if (fromLine < 1 || toLine < 1 || fromLine > view.state.doc.lines) continue;
    const from = view.state.doc.line(Math.min(fromLine, view.state.doc.lines)).from;
    const to = view.state.doc.line(Math.min(toLine, view.state.doc.lines)).to;
    markers.push(
      Decoration.mark({
        attributes: { style: `background: ${color}20; border-bottom: 1px solid ${color}55;` },
      }).range(from, to)
    );
  }

  markers.sort((a, b) => a.from - b.from || a.to - b.to);
  view.dispatch({
    effects: peerHighlightEffect.of(Decoration.set(markers)),
  });
}

// Guide cursor line decoration
const guideCursorEffect = StateEffect.define();

const guideCursorField = StateField.define({
  create() { return Decoration.none; },
  update(deco, tr) {
    for (const e of tr.effects) {
      if (e.is(guideCursorEffect)) {
        return e.value;
      }
    }
    return deco.map(tr.changes);
  },
  provide: f => EditorView.decorations.from(f),
});

// Show or clear the guide's cursor line indicator.
export function setGuideCursorLine(line, color) {
  if (!view) return;

  if (!line) {
    view.dispatch({ effects: guideCursorEffect.of(Decoration.none) });
    return;
  }

  const clampedLine = Math.max(1, Math.min(line, view.state.doc.lines));
  const lineInfo = view.state.doc.line(clampedLine);

  const deco = Decoration.line({
    class: 'cm-guide-cursor-line',
    attributes: { style: `background: ${color}15; border-left: 2px solid ${color};` },
  });

  view.dispatch({
    effects: guideCursorEffect.of(Decoration.set([deco.range(lineInfo.from)])),
  });
}

// Set or clear the guide highlight range (line numbers, 1-based).
export function setGuideHighlight(fromLine, toLine, color) {
  if (!view) return;

  if (!fromLine || !toLine) {
    // Clear highlight
    view.dispatch({ effects: guideHighlightEffect.of(Decoration.none) });
    return;
  }

  const from = view.state.doc.line(Math.max(1, Math.min(fromLine, view.state.doc.lines))).from;
  const to = view.state.doc.line(Math.max(1, Math.min(toLine, view.state.doc.lines))).to;

  const mark = Decoration.mark({
    class: 'cm-guide-highlight',
    attributes: { style: `background: ${color}22; outline: 1px solid ${color}44;` },
  });

  view.dispatch({
    effects: guideHighlightEffect.of(Decoration.set([mark.range(from, to)])),
  });
}
