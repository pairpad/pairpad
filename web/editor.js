import { EditorView, keymap, lineNumbers, highlightActiveLineGutter, highlightSpecialChars, drawSelection, highlightActiveLine, rectangularSelection, crosshairCursor } from '@codemirror/view';
import { EditorState } from '@codemirror/state';
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

// Create the base extensions shared across all editor instances
function baseExtensions(onSave) {
  return [
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

// Callback holder — set by the app
let onSaveCallback = () => {};
export function setOnSave(fn) {
  onSaveCallback = fn;
}
