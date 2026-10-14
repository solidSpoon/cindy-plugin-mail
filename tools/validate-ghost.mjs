#!/usr/bin/env node
/*
 * Pre-flight validation for the Cindy plugin source directory.
 *
 * This is NOT a replacement for the host's authoritative packaging
 * (ghost_forge_pack / ghost_forge_install). It catches the mistakes that are
 * cheap to make and expensive to discover late:
 *
 *   - manifest fields the packer rejects outright (schemaVersion, id charset,
 *     reserved id prefixes, missing minCindyVersion)
 *   - a declared path that is not actually in the package
 *   - a bundled native binary that lost its executable bit (Forge does not fix
 *     this; the guide puts the responsibility on the author)
 *   - locale files that do not line up with the manifest: unknown top-level
 *     keys, tool names that do not exist, a tool description for a tool that is
 *     not declared, or a JSON Pointer that resolves to a parameter field which
 *     is not in the manifest (a locale may only override fields that exist)
 *
 * Usage: node tools/validate-ghost.mjs [pluginDir]
 */

import fs from 'node:fs';
import path from 'node:path';

const dir = path.resolve(process.argv[2] || 'ghost');
const problems = [];
const notes = [];
const bad = (m) => problems.push(m);
const note = (m) => notes.push(m);

const MANIFEST = path.join(dir, 'ghost.json');
if (!fs.existsSync(MANIFEST)) {
  console.error(`FAIL  ${MANIFEST} does not exist`);
  process.exit(1);
}

let m;
try {
  m = JSON.parse(fs.readFileSync(MANIFEST, 'utf8'));
} catch (e) {
  console.error(`FAIL  ghost.json is not valid JSON: ${e.message}`);
  process.exit(1);
}

const ID_RE = /^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/;
const TOOL_RE = /^[a-z0-9][a-z0-9_-]{0,63}$/;
const KEY_RE = /^[a-z][a-z0-9_]{0,31}$/;
const SEMVER_RE = /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/;
const RESERVED = new Set(['__proto__', 'constructor', 'prototype']);
const LOCALE_TOP_KEYS = new Set([
  'name', 'description', 'whenToUse', 'tools', 'panel', 'mainView',
  'network', 'node', 'setup'
]);

// ── manifest ────────────────────────────────────────────────────────────────

if (m.schemaVersion !== 3) bad(`schemaVersion must be 3, got ${JSON.stringify(m.schemaVersion)}`);
if (typeof m.minCindyVersion !== 'string' || !SEMVER_RE.test(m.minCindyVersion)) {
  bad(`minCindyVersion must be a semver string (required by schemaVersion 3), got ${JSON.stringify(m.minCindyVersion)}`);
}
if (typeof m.id !== 'string' || !ID_RE.test(m.id)) {
  bad(`id must be 1-32 chars of [a-z0-9-] starting and ending alphanumeric, got ${JSON.stringify(m.id)}`);
} else if (/^(cindy|filo|xd)-/.test(m.id)) {
  bad(`id "${m.id}" uses a prefix reserved for official plugins (cindy- / filo- / xd-)`);
}
if (typeof m.version !== 'string' || !SEMVER_RE.test(m.version)) {
  bad(`version must be semver, got ${JSON.stringify(m.version)}`);
}
for (const field of ['name', 'description', 'whenToUse', 'entry', 'command']) {
  if (typeof m[field] !== 'string' || !m[field].trim()) bad(`${field} is required and must be a non-empty string`);
}
if (typeof m.description === 'string' && m.description.length > 300) bad(`description is ${m.description.length} chars (max 300)`);
if (typeof m.whenToUse === 'string' && m.whenToUse.length > 300) bad(`whenToUse is ${m.whenToUse.length} chars (max 300)`);

if (fs.existsSync(path.join(dir, 'cindy-signatures.json'))) {
  note('package is signed (cindy-signatures.json present): any file change must be re-signed by the release pipeline');
}

// declared files must exist in the package
const declared = [['entry', m.entry], ['icon', m.icon], ['settingsHtml', m.settingsHtml], ['mainView.html', m.mainView?.html], ['panel.html', m.panel?.html]];
for (const [label, rel] of declared) {
  if (typeof rel !== 'string' || !rel) continue;
  if (path.isAbsolute(rel) || rel.split('/').includes('..')) bad(`${label} path "${rel}" must be a relative path inside the package`);
  else if (!fs.existsSync(path.join(dir, rel))) bad(`${label} points at "${rel}", which is not in the package`);
}

// ── tools ───────────────────────────────────────────────────────────────────

const tools = Array.isArray(m.tools) ? m.tools : [];
if (!tools.length) bad('tools is empty: declare at least one tool, or drop the field and ship a manual-only plugin');
if (tools.length > 16) bad(`tools has ${tools.length} entries (host hard limit is 16)`);
const toolNames = new Set();
for (const t of tools) {
  if (!t || typeof t !== 'object') { bad('tools[] contains a non-object entry'); continue; }
  if (typeof t.name !== 'string' || !TOOL_RE.test(t.name)) bad(`tool name ${JSON.stringify(t.name)} must match ${TOOL_RE}`);
  else if (RESERVED.has(t.name)) bad(`tool name "${t.name}" is a reserved key`);
  else if (toolNames.has(t.name)) bad(`tool name "${t.name}" is declared twice`);
  else toolNames.add(t.name);
  if (typeof t.description !== 'string' || !t.description.trim()) bad(`tool "${t.name}" needs a description`);
  if (!t.parameters || typeof t.parameters !== 'object') bad(`tool "${t.name}" needs a parameters object`);
}

// ── node worker ─────────────────────────────────────────────────────────────

if (m.node) {
  const n = m.node;
  if (typeof n.entry !== 'string' || !/\.(c)?js$/.test(n.entry)) bad(`node.entry must be a package-relative .js/.cjs, got ${JSON.stringify(n.entry)}`);
  else if (!fs.existsSync(path.join(dir, n.entry))) bad(`node.entry "${n.entry}" is not in the package`);
  if (!['json-rpc-stdio', 'mcp-stdio'].includes(n.protocol)) bad(`node.protocol must be json-rpc-stdio or mcp-stdio, got ${JSON.stringify(n.protocol)}`);
  if (n.lifecycle === 'resident' && n.idleTimeoutSeconds !== undefined) bad('node.idleTimeoutSeconds must not be declared when lifecycle is "resident"');
  for (const extra of Array.isArray(n.entries) ? n.entries : []) {
    if (!fs.existsSync(path.join(dir, extra))) bad(`node.entries["${extra}"] is not in the package`);
  }
}

// ── manual ──────────────────────────────────────────────────────────────────

for (const item of m.manual?.items || []) {
  if (typeof item?.dir !== 'string' || !item.dir) { bad('manual.items[].dir is required'); continue; }
  const manualDir = path.join(dir, item.dir);
  const entry = path.join(manualDir, 'MANUAL.md');
  if (!fs.existsSync(entry)) { bad(`manual item "${item.name}" has no MANUAL.md at ${item.dir}/MANUAL.md`); continue; }
  if (fs.statSync(entry).size > 64 * 1024) bad(`manual/${item.dir}/MANUAL.md exceeds 64KB`);
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.isSymbolicLink()) bad(`manual may not contain symlinks: ${path.relative(dir, p)}`);
      else if (!e.name.endsWith('.md')) bad(`manual may only contain .md files: ${path.relative(dir, p)}`);
    }
  };
  walk(manualDir);
}

// ── locales ─────────────────────────────────────────────────────────────────

function pointerResolve(root, pointer) {
  if (pointer === '') return root;
  if (!pointer.startsWith('/')) return undefined;
  let cur = root;
  for (const raw of pointer.slice(1).split('/')) {
    const key = raw.replace(/~1/g, '/').replace(/~0/g, '~');
    if (cur === null || typeof cur !== 'object' || !(key in cur)) return undefined;
    cur = cur[key];
  }
  return cur;
}

if (m.locales) {
  if (typeof m.locales.en !== 'string') bad('locales is declared, so it must include "en"');
  for (const [loc, rel] of Object.entries(m.locales)) {
    const file = path.join(dir, rel);
    if (!fs.existsSync(file)) { bad(`locales.${loc} points at ${rel}, which is not in the package`); continue; }
    let l;
    try {
      l = JSON.parse(fs.readFileSync(file, 'utf8'));
    } catch (e) {
      bad(`locales.${loc} (${rel}) is not valid JSON: ${e.message}`);
      continue;
    }
    for (const key of Object.keys(l)) {
      if (!LOCALE_TOP_KEYS.has(key)) bad(`locales.${loc}: unknown top-level key "${key}"`);
    }
    for (const [tool, entry] of Object.entries(l.tools || {})) {
      if (!toolNames.has(tool)) { bad(`locales.${loc}: tools["${tool}"] does not match any declared tool`); continue; }
      const manifestTool = tools.find((t) => t.name === tool);
      if (entry.description !== undefined && typeof entry.description !== 'string') bad(`locales.${loc}: tools["${tool}"].description must be a string`);
      for (const [pointer, override] of Object.entries(entry.parameters || {})) {
        const target = pointerResolve(manifestTool.parameters, pointer);
        if (target === undefined) { bad(`locales.${loc}: tools["${tool}"].parameters["${pointer}"] does not resolve in the manifest`); continue; }
        if (target === null || typeof target !== 'object') { bad(`locales.${loc}: tools["${tool}"].parameters["${pointer}"] resolves to a non-object in the manifest`); continue; }
        for (const field of Object.keys(override)) {
          if (!(field in target)) bad(`locales.${loc}: tools["${tool}"].parameters["${pointer}"].${field} is not a field of that manifest parameter (a locale may only override fields that already exist)`);
        }
      }
    }
  }
}

// ── bundled executables ─────────────────────────────────────────────────────

const binDir = path.join(dir, 'bin');
if (fs.existsSync(binDir)) {
  for (const e of fs.readdirSync(binDir, { withFileTypes: true })) {
    if (!e.isFile()) continue;
    const p = path.join(binDir, e.name);
    const mode = fs.statSync(p).mode & 0o777;
    if (!(mode & 0o111)) bad(`bin/${e.name} is not executable (mode ${mode.toString(8)}): Forge copies Unix permissions as-is, so fix it with chmod 755`);
    else {
      const magic = Buffer.alloc(4);
      const fd = fs.openSync(p, 'r');
      fs.readSync(fd, magic, 0, 4, 0);
      fs.closeSync(fd);
      const v = magic.readUInt32BE(0);
      if (v === 0xcafebabe) note(`bin/${e.name} is a universal Mach-O (fat) binary`);
      else if (v === 0xcffaedfe || v === 0xcefaedfe) note(`bin/${e.name} is a single-architecture Mach-O (not universal)`);
    }
  }
}

// ── report ──────────────────────────────────────────────────────────────────

console.log(`validating ${path.relative(process.cwd(), dir) || '.'}  (id=${m.id} version=${m.version} node=${m.node ? 'yes' : 'no'})`);
for (const n of notes) console.log(`  note  ${n}`);
if (problems.length) {
  for (const p of problems) console.error(`  FAIL  ${p}`);
  console.error(`\n${problems.length} problem(s). Ghost Forge would reject this package, or it would install but misbehave.`);
  process.exit(1);
}
console.log('  OK    manifest, paths, tools, manual, locales and bundled executables');
