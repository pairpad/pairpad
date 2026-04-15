import { EditorView, keymap, lineNumbers, highlightActiveLineGutter, highlightSpecialChars, drawSelection, highlightActiveLine, rectangularSelection, crosshairCursor, gutter, GutterMarker, Decoration } from '@codemirror/view';
import { EditorState, StateField, StateEffect, RangeSet } from '@codemirror/state';
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

// --- Comment gutter ---

const commentMarkerEffect = StateEffect.define();

class CommentGutterMarker extends GutterMarker {
  constructor(color) {
    super();
    this.color = color;
  }
  toDOM() {
    const el = document.createElement('div');
    el.style.cssText = `width:6px;height:6px;border-radius:50%;background:${this.color || '#89b4fa'};margin:auto;cursor:pointer;`;
    return el;
  }
}

const commentMarkerField = StateField.define({
  create() { return RangeSet.empty; },
  update(markers, tr) {
    for (const e of tr.effects) {
      if (e.is(commentMarkerEffect)) {
        return e.value;
      }
    }
    return markers.map(tr.changes);
  },
});

const commentGutter = gutter({
  class: 'cm-comment-gutter',
  markers: (view) => view.state.field(commentMarkerField),
  domEventHandlers: {
    click(view, line) {
      const lineNum = view.state.doc.lineAt(line.from).number;
      if (window.addCommentAtLine) {
        window.addCommentAtLine(lineNum);
      }
      return true;
    },
  },
});

// Update comment markers for the current file
export function updateCommentMarkers(commentLines) {
  if (!view) return;
  const markers = [];
  for (const { line, color } of commentLines) {
    if (line >= 1 && line <= view.state.doc.lines) {
      const lineInfo = view.state.doc.line(line);
      markers.push(new CommentGutterMarker(color).range(lineInfo.from));
    }
  }
  markers.sort((a, b) => a.from - b.from);
  view.dispatch({
    effects: commentMarkerEffect.of(RangeSet.of(markers)),
  });
}

// Create the base extensions shared across all editor instances
function baseExtensions(onSave) {
  return [
    commentMarkerField,
    commentGutter,
    peerHighlightField,
    guideHighlightField,
    guideCursorField,
    lineNumbers(),
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
    oneDark,
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
