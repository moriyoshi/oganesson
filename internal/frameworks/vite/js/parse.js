// Stands in for Rolldown's parse module (rolldown/dist/shared/parse-*.mjs),
// whose parseSync / parse call the native Oxc parser: acorn (vendored),
// the ESTree parser Rollup itself used. Vite parses JavaScript snippets with
// it (import.meta.glob options, import() expressions, dynamic import
// variables). TypeScript syntax is not supported.
import * as acorn from "og:acorn";

function parseResult(sourceText, options = {}) {
  try {
    const program = acorn.parse(sourceText, {
      ecmaVersion: "latest",
      sourceType: options.sourceType === "script" ? "script" : "module",
      allowHashBang: true,
      allowAwaitOutsideFunction: true,
      preserveParens: !!options.preserveParens,
      ranges: !!options.range,
    });
    return { program, module: moduleRecord(program, sourceText), comments: [], errors: [] };
  } catch (e) {
    if (!(e instanceof SyntaxError)) throw e;
    const pos = typeof e.pos === "number" ? e.pos : 0;
    return {
      program: { type: "Program", body: [], sourceType: "module", start: 0, end: sourceText.length },
      module: {},
      comments: [],
      errors: [{ severity: "Error", message: e.message.replace(/ \(\d+:\d+\)$/, ""), labels: [{ start: pos, end: pos }], helpMessage: null, codeframe: null }],
    };
  }
}

// moduleRecord is Oxc's EcmaScriptModule record of a program (what
// oxc-parser returns as `module`), field for field: static imports and
// exports with their entries, dynamic imports, import.meta uses. A local
// export of an imported binding is an indirect export, listed under its
// import statement as Oxc does. Nuxt reads it (a plugin's default export,
// the imports it rewrites).
function moduleRecord(program, code) {
  const span = (n) => ({ start: n.start, end: n.end });
  const none = { kind: "None", name: null, start: null, end: null };
  const nameOf = (n) => (n.type === "Literal" ? n.value : n.name);
  const request = (src) => ({ value: src.value, ...span(src) });
  const staticImports = [], dynamicImports = [], importMetas = [];
  const exportsAt = new Map(); // statement start -> staticExports item
  const item = (node) => {
    if (!exportsAt.has(node.start)) exportsAt.set(node.start, { ...span(node), entries: [] });
    return exportsAt.get(node.start);
  };
  const imported = new Map(); // local name -> { decl, importName }
  let hasModuleSyntax = false;
  for (const node of program.body) {
    if (node.type !== "ImportDeclaration") continue;
    hasModuleSyntax = true;
    const entries = node.specifiers.map((sp) => {
      const importName =
        sp.type === "ImportDefaultSpecifier" ? { kind: "Default", name: null, ...span(sp.local) }
        : sp.type === "ImportNamespaceSpecifier" ? { kind: "NamespaceObject", name: null, start: null, end: null }
        : { kind: "Name", name: nameOf(sp.imported), ...span(sp.imported) };
      imported.set(sp.local.name, { decl: node, importName });
      return { importName, localName: { value: sp.local.name, ...span(sp.local) }, isType: false };
    });
    staticImports.push({ ...span(node), moduleRequest: request(node.source), entries });
  }
  for (const node of program.body) {
    switch (node.type) {
      case "ExportNamedDeclaration": {
        hasModuleSyntax = true;
        if (node.declaration) {
          const names = [];
          const collect = (p) => {
            if (!p) return;
            if (p.type === "Identifier") names.push(p);
            else if (p.type === "ObjectPattern") p.properties.forEach((q) => collect(q.type === "RestElement" ? q.argument : q.value));
            else if (p.type === "ArrayPattern") p.elements.forEach((q) => collect(q && q.type === "RestElement" ? q.argument : q));
            else if (p.type === "AssignmentPattern") collect(p.left);
          };
          if (node.declaration.type === "VariableDeclaration") node.declaration.declarations.forEach((d) => collect(d.id));
          else if (node.declaration.id) names.push(node.declaration.id);
          const it = item(node);
          for (const id of names) {
            it.entries.push({ ...span(node.declaration), moduleRequest: null, importName: none, exportName: { kind: "Name", name: id.name, ...span(id) }, localName: { kind: "Name", name: id.name, ...span(id) }, isType: false });
          }
          break;
        }
        for (const sp of node.specifiers) {
          const exported = nameOf(sp.exported), local = nameOf(sp.local);
          const exportName = exported === "default" ? { kind: "Default", name: null, ...span(sp.exported) } : { kind: "Name", name: exported, ...span(sp.exported) };
          const via = !node.source && imported.get(local);
          if (via && via.importName.kind !== "NamespaceObject") {
            // An imported binding re-exported: an indirect export.
            item(via.decl).entries.push({ ...span(sp), moduleRequest: request(via.decl.source), importName: via.importName, exportName, localName: none, isType: false });
          } else if (node.source) {
            item(node).entries.push({ ...span(sp), moduleRequest: request(node.source), importName: { kind: "Name", name: local, ...span(sp.local) }, exportName, localName: none, isType: false });
          } else {
            item(node).entries.push({ ...span(sp), moduleRequest: null, importName: none, exportName, localName: { kind: "Name", name: local, ...span(sp.local) }, isType: false });
          }
        }
        break;
      }
      case "ExportDefaultDeclaration": {
        hasModuleSyntax = true;
        const d = node.declaration;
        const kw = code.indexOf("default", node.start);
        item(node).entries.push({
          ...span(d), moduleRequest: null, importName: none,
          exportName: { kind: "Default", name: null, start: kw, end: kw + "default".length },
          localName: d.id ? { kind: "Name", name: d.id.name, ...span(d.id) } : { kind: "Default", name: null, ...span(d) },
          isType: false,
        });
        break;
      }
      case "ExportAllDeclaration":
        hasModuleSyntax = true;
        item(node).entries.push({
          ...span(node), moduleRequest: request(node.source),
          importName: node.exported ? { kind: "All", name: null, start: null, end: null } : { kind: "AllButDefault", name: null, start: null, end: null },
          exportName: node.exported ? { kind: "Name", name: nameOf(node.exported), ...span(node.exported) } : none,
          localName: none, isType: false,
        });
        break;
    }
  }
  // import() and import.meta anywhere in the program.
  const walk = (n) => {
    if (!n || typeof n.type !== "string") return;
    if (n.type === "ImportExpression") dynamicImports.push({ ...span(n), moduleRequest: span(n.source) });
    else if (n.type === "MetaProperty" && n.meta.name === "import") importMetas.push(span(n));
    for (const k in n) {
      const v = n[k];
      if (k === "type" || k === "start" || k === "end" || !v || typeof v !== "object") continue;
      if (Array.isArray(v)) v.forEach(walk);
      else walk(v);
    }
  };
  walk(program);
  if (importMetas.length) hasModuleSyntax = true;
  const staticExports = [...exportsAt.values()].filter((e) => e.entries.length).sort((a, b) => a.start - b.start);
  return { hasModuleSyntax, staticImports, staticExports, dynamicImports, importMetas };
}

// The module's exports: parseSync and parse.
export function n(filename, sourceText, options) {
  return parseResult(sourceText, options);
}
export async function t(filename, sourceText, options) {
  return parseResult(sourceText, options);
}
