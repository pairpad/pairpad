// Symbol extraction from CodeMirror's Lezer parse tree.
// Used to anchor annotations to structural code elements
// (functions, methods, classes, types) instead of line numbers.

// Node type names that represent declaration-level constructs.
// These are consistent across Lezer grammars for supported languages.
const DECLARATION_TYPES = new Set([
  // Go
  'FunctionDeclaration', 'MethodDeclaration', 'TypeDeclaration',
  'TypeSpec',
  // JavaScript/TypeScript
  'FunctionDeclaration', 'MethodDeclaration', 'ClassDeclaration',
  'VariableDeclaration', 'ExportDeclaration',
  'ArrowFunction', 'FunctionExpression',
  // Python
  'FunctionDefinition', 'ClassDefinition',
  // Rust
  'FunctionItem', 'StructItem', 'EnumItem', 'ImplItem', 'TraitItem',
  // Java
  'MethodDeclaration', 'ClassDeclaration', 'InterfaceDeclaration',
  // C/C++
  'FunctionDefinition', 'StructSpecifier', 'ClassSpecifier',
]);

// Node type names that contain the identifier of a declaration.
const NAME_TYPES = new Set([
  'VariableName', 'PropertyName', 'VariableDefinition',
  'TypeName', 'TypeIdentifier', 'Identifier',
  'PropertyDefinition', 'FieldDefinition',
  'Definition', 'Name',
]);

/**
 * Find the enclosing symbol for a position in the editor.
 * Returns { symbolPath, symbolOffset } or null if no symbol found.
 *
 * @param {EditorView} view - The CodeMirror editor view
 * @param {number} line - 1-based line number
 * @returns {{ symbolPath: string, symbolOffset: number } | null}
 */
export function getSymbolAtLine(view, line) {
  if (!view || !view.state) return null;

  const tree = ensureParsed(view.state);
  if (!tree) return null;

  const lineInfo = view.state.doc.line(Math.min(line, view.state.doc.lines));
  const pos = lineInfo.from;

  // Walk up from the position to find the enclosing declaration
  let node = tree.resolveInner(pos, 1);
  let declaration = null;

  while (node) {
    if (DECLARATION_TYPES.has(node.type.name)) {
      declaration = node;
      break;
    }
    node = node.parent;
  }

  if (!declaration) return null;

  // Extract the name from the declaration
  const name = extractName(view.state, declaration);
  if (!name) return null;

  const symbolPath = `${declaration.type.name}:${name}`;
  const symbolOffset = pos - declaration.from;

  return { symbolPath, symbolOffset };
}

/**
 * Find a symbol by its path in the current parse tree.
 * Returns the character position of the symbol, or -1 if not found.
 *
 * @param {EditorView} view - The CodeMirror editor view
 * @param {string} symbolPath - e.g. "FunctionDeclaration:initDatabase"
 * @returns {number} Character position of the symbol start, or -1
 */
function findSymbol(view, symbolPath) {
  if (!view || !view.state || !symbolPath) return -1;

  const tree = ensureParsed(view.state);
  if (!tree) return -1;

  const [typeName, name] = symbolPath.split(':', 2);
  if (!typeName || !name) return -1;

  let found = -1;

  tree.iterate({
    enter(node) {
      if (found >= 0) return false; // stop after first match
      if (node.type.name === typeName) {
        const nodeName = extractNameFromNode(view.state, node);
        if (nodeName === name) {
          found = node.from;
          return false;
        }
      }
    },
  });

  return found;
}

/**
 * Re-anchor an annotation using its symbol path.
 * Returns the new line number (1-based) or null if symbol not found.
 *
 * @param {EditorView} view - The CodeMirror editor view
 * @param {string} symbolPath - e.g. "FunctionDeclaration:initDatabase"
 * @param {number} symbolOffset - character offset from symbol start
 * @returns {number | null} New 1-based line number, or null
 */
export function reanchorBySymbol(view, symbolPath, symbolOffset) {
  const symbolPos = findSymbol(view, symbolPath);
  if (symbolPos < 0) return null;

  // Apply the offset within the symbol
  const targetPos = Math.min(symbolPos + symbolOffset, view.state.doc.length);
  return view.state.doc.lineAt(targetPos).number;
}

// --- Internal helpers ---

function ensureParsed(state) {
  // Force a full parse if the tree is partial
  const tree = state.tree;
  if (!tree || tree.length < state.doc.length) {
    // Tree is still being parsed incrementally — use what we have
    return state.tree;
  }
  return tree;
}

function extractName(state, node) {
  return extractNameFromNode(state, { node });
}

function extractNameFromNode(state, treeNode) {
  const node = treeNode.node || treeNode;
  // Look for a name child node
  let cursor = node.cursor();
  if (!cursor.firstChild()) return null;

  do {
    if (NAME_TYPES.has(cursor.type.name)) {
      return state.sliceDoc(cursor.from, cursor.to);
    }
  } while (cursor.nextSibling());

  return null;
}
