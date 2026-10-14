/* node/worker.cjs — 随包 Node 工作进程。
 *
 * 这是真正的本机进程:它唯一的活儿就是 spawn 包内的 mail-app-cli,把它当成
 * 「标准输入一份 JSON 文档、标准输出一个结果信封」的普通子进程。协议是逐行
 * JSON-RPC 2.0,stdout 只能写协议消息,日志一律走 stderr。
 *
 * 方法:
 *   mail/health  → { ok, binary, exists, version? }
 *   mail/schema  → { ok, schema }                     (结果在 worker 内缓存)
 *   mail/run     → { ok, exitCode, envelope, stderr } (params: { action, params })
 */

const readline = require('node:readline');
const path = require('node:path');
const os = require('node:os');
const fs = require('node:fs');
const { spawn } = require('node:child_process');

const BUNDLED_BIN = path.join(__dirname, '..', 'bin', 'mail-app-cli');
const FALLBACK_BIN = path.join(os.homedir(), '.local', 'bin', 'mail-app-cli');

const HEARTBEAT_MS = 10000;
const RUN_HARD_MS = 600000; // 兜底自杀,比主机的 maxTotalMs 上限更宽,只用来防孤儿进程

let binCache = null;

function resolveBinary() {
  if (binCache) return binCache;
  const candidates = [BUNDLED_BIN, FALLBACK_BIN];
  for (const candidate of candidates) {
    try {
      if (fs.existsSync(candidate)) {
        fs.accessSync(candidate, fs.constants.X_OK);
        binCache = { path: candidate, bundled: candidate === BUNDLED_BIN };
        return binCache;
      }
    } catch (e) { /* 试下一个 */ }
  }
  binCache = { path: BUNDLED_BIN, bundled: true, missing: true };
  return binCache;
}

function tail(text, limit) {
  const s = String(text == null ? '' : text).trim();
  const max = limit || 2000;
  return s.length > max ? s.slice(s.length - max) : s;
}

function reply(message) {
  process.stdout.write(JSON.stringify(message) + '\n');
}

function ok(id, result) {
  reply({ jsonrpc: '2.0', id, result });
}

function fail(id, code, message, data) {
  const error = { code, message };
  if (data !== undefined) error.data = data;
  reply({ jsonrpc: '2.0', id, error });
}

function heartbeat(action) {
  reply({ jsonrpc: '2.0', method: 'progress', params: { action } });
}

// 跑一次 mail-app-cli:JSON 文档走 stdin,信封走 stdout。
function runCli(doc, options) {
  const opts = options || {};
  const bin = resolveBinary();
  if (bin.missing) {
    return Promise.resolve({
      ok: false,
      code: 'BINARY_MISSING',
      message:
        'the bundled mail-app-cli is missing or not executable (' + bin.path + '). ' +
        'Rebuild the plugin: go build -o ghost/bin/mail-app-cli . && chmod 755 ghost/bin/mail-app-cli'
    });
  }

  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(bin.path, [], {
        stdio: ['pipe', 'pipe', 'pipe'],
        cwd: opts.cwd || os.homedir()
      });
    } catch (e) {
      resolve({ ok: false, code: 'SPAWN_FAILED', message: String((e && e.message) || e) });
      return;
    }

    let stdout = '';
    let stderr = '';
    let settled = false;

    const hb = setInterval(() => { heartbeat(opts.action || ''); }, opts.heartbeatMs || HEARTBEAT_MS);
    const hardKill = setTimeout(() => {
      try { child.kill('SIGKILL'); } catch (e) { /* ignore */ }
      finish({
        ok: false,
        code: 'CHILD_TIMEOUT',
        message: 'mail-app-cli was still running after ' + (opts.hardMs || RUN_HARD_MS) + 'ms and was killed'
      });
    }, opts.hardMs || RUN_HARD_MS);

    function finish(value) {
      if (settled) return;
      settled = true;
      clearInterval(hb);
      clearTimeout(hardKill);
      resolve(value);
    }

    child.on('error', (e) => {
      finish({ ok: false, code: 'SPAWN_FAILED', message: String((e && e.message) || e) });
    });
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('close', (code) => {
      const text = stdout.trim();
      if (!text) {
        finish({
          ok: false,
          code: 'EMPTY_OUTPUT',
          message: 'mail-app-cli exited with code ' + code + ' and printed nothing',
          stderr: tail(stderr)
        });
        return;
      }
      let envelope;
      try {
        envelope = JSON.parse(text);
      } catch (e) {
        finish({
          ok: false,
          code: 'BAD_OUTPUT',
          message: 'mail-app-cli printed something that is not JSON: ' + tail(text, 500),
          stderr: tail(stderr)
        });
        return;
      }
      finish({ ok: true, exitCode: code, envelope, stderr: tail(stderr) });
    });

    try {
      child.stdin.end(JSON.stringify(doc));
    } catch (e) {
      finish({ ok: false, code: 'SPAWN_FAILED', message: String((e && e.message) || e) });
    }
  });
}

// attachments.save 会写出真实文件:相对路径/省略 output 都落到用户主目录,
// 并把绝对路径回给调用方,免得文件悄悄落在不知道哪儿的 cwd 里。
function resolveSaveOutput(action, params) {
  if (action !== 'attachments.save') return params;
  const out = { ...params };
  let target = typeof out.output === 'string' && out.output.trim() ? out.output.trim() : '';
  if (!target) target = typeof out.name === 'string' && out.name.trim() ? out.name.trim() : 'attachment';
  if (!path.isAbsolute(target)) target = path.join(os.homedir(), target);
  out.output = target;
  return out;
}

async function handleRun(params) {
  const action = String((params && params.action) || '');
  const raw = params && params.params;
  const clean = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw : {};
  const doc = { action, params: resolveSaveOutput(action, clean) };
  const result = await runCli(doc, { action, hardMs: RUN_HARD_MS });
  if (result.ok) delete result.stderr; // 成功的 stderr 只是噪音,别塞进上下文
  return result;
}

let schemaCache = null;

async function handleSchema() {
  if (schemaCache) return { ok: true, schema: schemaCache };
  const result = await runCli({ action: 'schema' }, { action: 'schema', hardMs: 60000, heartbeatMs: 60000 });
  if (!result.ok) return result;
  const results = result.envelope && result.envelope.results;
  const data = results && results[0] && results[0].data;
  if (!data || !Array.isArray(data.actions)) {
    return { ok: false, code: 'SCHEMA_UNAVAILABLE', message: 'mail-app-cli did not return a schema document' };
  }
  const bin = resolveBinary();
  schemaCache = data;
  return { ok: true, schema: data, binary: bin.path, bundled: !!bin.bundled };
}

async function handleHealth() {
  const bin = resolveBinary();
  const out = { ok: true, binary: bin.path, bundled: !!bin.bundled, exists: !bin.missing };
  if (bin.missing) return out;
  // --version 不是 JSON 协议动作,单独问一次
  const version = await new Promise((resolve) => {
    try {
      const child = spawn(bin.path, ['--version'], { stdio: ['ignore', 'pipe', 'ignore'] });
      let text = '';
      child.stdout.on('data', (c) => { text += c; });
      child.on('close', () => resolve(text.trim()));
      child.on('error', () => resolve(''));
    } catch (e) { resolve(''); }
  });
  try { out.version = JSON.parse(version); } catch (e) { out.version = version; }
  return out;
}

readline.createInterface({ input: process.stdin }).on('line', async (line) => {
  let request;
  try {
    request = JSON.parse(line);
  } catch (e) {
    reply({ jsonrpc: '2.0', id: null, error: { code: -32700, message: 'Parse error' } });
    return;
  }

  const id = request.id;
  try {
    if (request.method === 'mail/health') return ok(id, await handleHealth());
    if (request.method === 'mail/schema') {
      return ok(id, await handleSchema());
    }
    if (request.method === 'mail/run') {
      const r = await handleRun(request.params || {});
      return ok(id, r);
    }
    return fail(id, -32601, 'Method not found: ' + String(request.method));
  } catch (e) {
    return ok(id, { ok: false, code: 'INTERNAL', message: String((e && e.message) || e) });
  }
});
