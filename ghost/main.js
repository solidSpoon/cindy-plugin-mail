/* main.js — mail-app-cli 的 Cindy 电子脑。
 *
 * 沙箱红线:这里没有文件系统、没有进程、没有通用网络。所有本机动作(执行随包
 * mail-app-cli、因而驱动 Mail.app)都经 cindy.node.request 下发到 node/worker.cjs,
 * 由那个真正的本地进程去 spawn 包内二进制。
 *
 * 操作名 = CLI 的 action,args = CLI 的 params 对象,原样透传,不在这里重写规则。
 */

// ─────────────────────────── 类目表(操作名的权威归属) ───────────────────────

var CATEGORIES = [
  {
    key: 'read',
    label: { en: 'Read', 'zh-CN': '读取' },
    when: {
      en: 'Accounts, mailboxes, message lists, cross-account views, search, one full message, attachment lists.',
      'zh-CN': '账户、邮箱、邮件列表、跨账户视图、搜索、单封完整邮件、附件清单。'
    },
    actions: [
      'accounts.list', 'mailboxes.list', 'messages.list', 'messages.unified',
      'messages.show', 'messages.search', 'messages.resolve', 'attachments.list'
    ],
    rules: {
      en: [
        'Start cheap: mailboxes.list with unreadOnly:true is the overview; messages.unified with box + since + format:"compact" covers every account in one call.',
        'format:"compact" returns 8 fields per message instead of 16 - use it for anything you will scan. Ids are JSON strings ("7847"), never numbers.',
        'messages.search matches subject and sender only and never reads a body. Reading a body at all - messages.show, or withContent:true - can block Mail.app\'s whole scripting interface while a download stalls and can take minutes; use it only for messages that matter.',
        'Caches: accounts and mailboxes 60s, message lists 5 min. Pass noCache:true when freshness matters.',
        'messages.resolve reports where ids currently live and changes nothing.'
      ],
      'zh-CN': [
        '先用便宜的调用:mailboxes.list + unreadOnly:true 是总览;messages.unified 带 box + since + format:"compact" 一次覆盖所有账户。',
        'format:"compact" 每封只回 8 个字段(默认 16 个),要扫的列表都用它。id 是 JSON 字符串("7847"),不是数字。',
        'messages.search 只匹配主题和发件人,从不读正文。任何读正文的调用——messages.show,或 withContent:true——都可能在正文下载卡住时堵死 Mail.app 整个脚本接口,耗时以分钟计;只对真正要紧的邮件用。',
        '缓存:账户/邮箱 60 秒,邮件列表 5 分钟。要新鲜数据传 noCache:true。',
        'messages.resolve 只报告 id 现在在哪,什么也不改。'
      ]
    }
  },
  {
    key: 'write',
    label: { en: 'Organize & draft', 'zh-CN': '整理与草稿' },
    when: {
      en: 'Mark read/flagged, move, archive, quarantine (the safe non-delete), mark whole mailboxes read, compose a draft.',
      'zh-CN': '标记已读/旗标、移动、归档、隔离(不删除的安全替代)、整箱标记已读、写一封草稿。'
    },
    actions: [
      'messages.patch', 'messages.move', 'messages.archive', 'messages.quarantine',
      'mailboxes.markRead', 'draft'
    ],
    rules: {
      en: [
        'This plugin cannot send mail and cannot delete mail. draft composes into Mail.app\'s Drafts mailbox and stops; the user reviews and presses Send. messages.quarantine is the replacement for deleting: a reversible move. Never say mail was sent or deleted.',
        'Every mutation takes dryRun:true. Anything touching more than one message: dry-run, show the user the list, then apply. The host also asks the user for confirmation on move / archive / quarantine / multi-message patch / markRead.',
        'Mutations assign NEW ids to the affected messages. After move / archive / quarantine, re-list the destination instead of reusing the old ids.',
        'A move is read back and reported as confirmed / notPersisted. A non-empty notPersisted means MOVE_NOT_PERSISTED and nothing moved - do not report success.',
        'in is {"account":"...","mailbox":"..."} (both required). Omit it to resolve ids globally; Mail.app ids are unique database-wide, so one call can mix accounts.',
        'set is {"read":true,"flagged":true} - a field left out is left untouched. select for markRead is a union: mode "mailbox" (account+mailbox) or mode "special" (kinds: trash|junk|archive); mixing the two is a decode error.',
        'Gmail: Mail.app cannot archive Gmail safely. messages.archive returns GMAIL_ARCHIVE_UNSUPPORTED; use messages.quarantine instead.',
        'The holding mailbox for quarantine must already exist on the server - it is created by the user in Mail.app, not by this tool. Missing or local-only: HOLDING_BOX_UNAVAILABLE.'
      ],
      'zh-CN': [
        '本插件不能发信,也不能删信。draft 只把信写进 Mail.app 的草稿箱就停下,由用户自己按发送。messages.quarantine 是删除的安全替代:一次可撤销的移动。绝不要说"已发送"或"已删除"。',
        '每个写操作都接受 dryRun:true。涉及多封邮件时:先 dry-run、把清单给用户看、再执行。move / archive / quarantine / 多封 patch / markRead 主机还会弹确认框。',
        '写操作会给被处理的邮件分配新 id。move / archive / quarantine 之后要重新列目标邮箱,不要复用旧 id。',
        '移动会被回读校验并报告 confirmed / notPersisted。notPersisted 非空即 MOVE_NOT_PERSISTED、其实没移动——不要报告成功。',
        'in 是 {"account":"...","mailbox":"..."}(两个都必填)。省略 in 则全局解析 id;Mail.app 的 id 全库唯一,一次调用可以跨账户。',
        'set 是 {"read":true,"flagged":true},没写的字段不动。markRead 的 select 是联合体:mode "mailbox"(account+mailbox)或 mode "special"(kinds: trash|junk|archive);两种模式混用是解码错误。',
        'Gmail:Mail.app 无法安全归档 Gmail。messages.archive 返回 GMAIL_ARCHIVE_UNSUPPORTED;改用 messages.quarantine。',
        'quarantine 的持有邮箱必须已在服务端存在——由用户在 Mail.app 里创建,本工具不创建。缺失或只在本地: HOLDING_BOX_UNAVAILABLE。'
      ]
    }
  },
  {
    key: 'attachment',
    label: { en: 'Attachments to disk', 'zh-CN': '附件落盘' },
    when: {
      en: 'Save one attachment of one message to a real file on this Mac.',
      'zh-CN': '把某封邮件的某个附件存成本机上的真实文件。'
    },
    actions: ['attachments.save'],
    rules: {
      en: [
        'attachments.list first to learn the exact attachment name; attachments.save then writes a real file and returns { saved, name, id }.',
        'output is a filesystem path. A relative path or an omitted output resolves against the user\'s home directory; pass an absolute path when it matters. Files written outside the working directory still land on this Mac - tell the user the absolute path that came back in saved.',
        'This is the one operation here with a filesystem side effect. Do not invent an output path the user did not ask for.'
      ],
      'zh-CN': [
        '先用 attachments.list 拿到附件的准确名字;attachments.save 会写出真实文件并返回 { saved, name, id }。',
        'output 是文件系统路径。相对路径或省略 output 会解析到用户主目录;在意位置就传绝对路径。写到工作目录外的文件仍会落在这台机器上——把返回的 saved 绝对路径告诉用户。',
        '这是本插件唯一带文件系统副作用的操作。不要自作主张编一个用户没要求的输出路径。'
      ]
    }
  },
  {
    key: 'system',
    label: { en: 'Tool & environment', 'zh-CN': '工具与环境' },
    when: {
      en: 'Self-describe the tool (schema), ask Mail.app to fetch new mail, hand a message to the user in Mail.app.',
      'zh-CN': '自描述接口(schema)、让 Mail.app 立刻收信、把某封邮件在 Mail.app 里交给用户。'
    },
    actions: ['schema', 'sync', 'open'],
    rules: {
      en: [
        'schema returns every action with its parameter object and the shared defs, generated by reflection over the same structs the decoder uses - it cannot drift. Prefer it over guessing a parameter.',
        'sync asks Mail.app to check for new mail now; it returns before the fetch completes.',
        'open activates Mail.app and optionally focuses a mailbox or message - useful to hand a message to the user instead of describing it.'
      ],
      'zh-CN': [
        'schema 返回每个 action 的参数对象和共享 defs,由解码器所用的同一批结构体反射生成,不会漂移。拿不准参数就用它,不要猜。',
        'sync 让 Mail.app 现在就去收信;它在收信完成前就返回。',
        'open 会激活 Mail.app,可聚焦某个邮箱或某封邮件——把邮件交给用户看,比用文字描述它好。'
      ]
    }
  }
];

var ENVELOPE_NOTE = {
  en: 'Result envelope: { ok, okCount, failed, results[] }; each result has ok, dryRun, data and error{code,hint}. Branch on error.code (INVALID_REQUEST, UNKNOWN_ACTION, ACCOUNT_NOT_FOUND, MAILBOX_NOT_FOUND, MESSAGE_MISSING, PARTIAL_FAILURE, HOLDING_BOX_UNAVAILABLE, GMAIL_ARCHIVE_UNSUPPORTED, MOVE_NOT_PERSISTED, DRAFT_UNVERIFIED, MAIL_SCRIPT_ERROR, INTERNAL), never on the prose.',
  'zh-CN': '结果信封:{ ok, okCount, failed, results[] };每个 result 有 ok、dryRun、data 和 error{code,hint}。请按 error.code 分支(INVALID_REQUEST、UNKNOWN_ACTION、ACCOUNT_NOT_FOUND、MAILBOX_NOT_FOUND、MESSAGE_MISSING、PARTIAL_FAILURE、HOLDING_BOX_UNAVAILABLE、GMAIL_ARCHIVE_UNSUPPORTED、MOVE_NOT_PERSISTED、DRAFT_UNVERIFIED、MAIL_SCRIPT_ERROR、INTERNAL),不要解析文案。'
};

var MANUAL_HINT = {
  en: 'Full workflows, caching and the pitfalls table: ghost_manual({ ghost_id: "mail-app-cli", path: "operations" }).',
  'zh-CN': '完整工作流、缓存与踩坑表: ghost_manual({ ghost_id: "mail-app-cli", path: "operations" })。'
};

// 二进制不可用时的降级目录(名字与一行摘要),让 list_tools 仍能回答并报出故障。
var FALLBACK_ACTIONS = {
  'accounts.list': 'List the Mail.app accounts',
  'attachments.list': 'List the attachments of one message',
  'attachments.save': 'Save one attachment to disk',
  'draft': 'Compose a message into Mail.app\'s Drafts mailbox (never sends)',
  'mailboxes.list': 'List mailboxes with unread counts',
  'mailboxes.markRead': 'Mark every message in whole mailboxes read (or unread)',
  'messages.archive': 'Move messages to the account\'s Archive mailbox',
  'messages.list': 'List messages in one mailbox',
  'messages.move': 'Move messages to another mailbox (new ids in the destination)',
  'messages.patch': 'Set read/flagged state on messages in one Mail.app call',
  'messages.quarantine': 'Park messages in a holding mailbox (safe replacement for delete)',
  'messages.resolve': 'Report where message ids currently live, changing nothing',
  'messages.search': 'Search subject and sender across mailboxes',
  'messages.show': 'Show one message in full, including body and thread headers',
  'messages.unified': 'Read one cross-account view (inbox, unread, flagged, sent, drafts, trash, junk)',
  'open': 'Activate Mail.app, optionally focusing a mailbox or message',
  'schema': 'Describe every action, its parameter object and the tool\'s constraints',
  'sync': 'Ask Mail.app to check for new mail now'
};

// 「危险」写操作:不再是 dryRun 时先请用户点一次确认。
function confirmPlan(name, params) {
  if (!params || params.dryRun === true) return null;
  var ids = Array.isArray(params.ids) ? params.ids : [];
  var n = ids.length;
  var many = n + ' message' + (n === 1 ? '' : 's');
  switch (name) {
    case 'messages.move':
      return {
        body: pick({
          en: 'Move ' + (n ? many : 'these messages') + ' to "' + String(params.to || '') + '"? Moved messages get new ids and leave the mailbox they are in now.',
          'zh-CN': '把 ' + (n ? n + ' 封邮件' : '这些邮件') + '移动到「' + String(params.to || '') + '」?移动后它们会获得新 id 并离开当前邮箱。'
        }),
        confirmText: pick({ en: 'Move', 'zh-CN': '移动' }),
        danger: true
      };
    case 'messages.archive':
      return {
        body: pick({
          en: 'Archive ' + (n ? many : 'these messages') + '? Nothing is deleted, but they leave the inbox.',
          'zh-CN': '把 ' + (n ? n + ' 封邮件' : '这些邮件') + '归档?不会删除,但它们会离开收件箱。'
        }),
        confirmText: pick({ en: 'Archive', 'zh-CN': '归档' }),
        danger: true
      };
    case 'messages.quarantine':
      return {
        body: pick({
          en: 'Park ' + (n ? many : 'these messages') + ' in the holding mailbox "' + String(params.box || '待删除') + '"? This is a reversible move, not a delete.',
          'zh-CN': '把 ' + (n ? n + ' 封邮件' : '这些邮件') + '移入持有邮箱「' + String(params.box || '待删除') + '」?这是可撤销的移动,不是删除。'
        }),
        confirmText: pick({ en: 'Park', 'zh-CN': '移入' }),
        danger: true
      };
    case 'messages.patch':
      if (n > 1) {
        return {
          body: pick({
            en: 'Change read/flagged state on ' + many + '?',
            'zh-CN': '修改 ' + n + ' 封邮件的已读/旗标状态?'
          }),
          confirmText: pick({ en: 'Apply', 'zh-CN': '应用' }),
          danger: true
        };
      }
      return null;
    case 'mailboxes.markRead':
      return {
        body: pick({
          en: 'Mark every message in the selected mailbox(es) ' + (params.unread ? 'unread' : 'read') + '? This can touch thousands of messages at once.',
          'zh-CN': '把所选邮箱里的全部邮件标记为' + (params.unread ? '未读' : '已读') + '?一次可能涉及上千封。'
        }),
        confirmText: pick({ en: 'Mark', 'zh-CN': '标记' }),
        danger: true
      };
    default:
      return null;
  }
}

// 读正文的调用可能要等 Mail.app 下载,给足绝对上限;其余保持短。
function absoluteCapMs(name, params) {
  var bodyRisk = name === 'messages.show' ||
    (name.indexOf('messages.') === 0 && params && params.withContent === true);
  return bodyRisk ? 300000 : 120000;
}

// ─────────────────────────── 宿主上下文与语言 ───────────────────────────

var LOCALE = 'en';
var localeReady = (async function () {
  try {
    if (!cindy.request) return;
    var r = await Promise.race([
      cindy.request({ kind: 'app-context' }),
      new Promise(function (res) { setTimeout(function () { res(null); }, 2000); })
    ]);
    if (r && r.ok && r.context && r.context.locale) LOCALE = r.context.locale;
  } catch (e) { /* 回落英文 */ }
})();

function pick(v) {
  if (v == null) return '';
  if (typeof v === 'string') return v;
  return v[LOCALE] || v.en || '';
}

function rulesOf(list) {
  return (Array.isArray(list) ? list : []).map(function (s) { return '- ' + pick(s); }).join('\n');
}

// ─────────────────────────── Node 工作进程 ───────────────────────────

var schemaCache = null;
var schemaFailed = null;

async function loadSchema() {
  if (schemaCache) return schemaCache;
  if (schemaFailed) return null;
  var r;
  try {
    r = await cindy.node.request({ method: 'mail/schema', timeoutMs: 60000 });
  } catch (e) {
    schemaFailed = String((e && e.message) || e);
    return null;
  }
  if (!r || !r.ok || !r.result || !r.result.ok) {
    schemaFailed = (r && (r.message || (r.result && r.result.message))) || 'mail/schema failed';
    return null;
  }
  schemaCache = r.result.schema;
  return schemaCache;
}

function allNames(schema) {
  if (schema && Array.isArray(schema.actions)) return schema.actions.map(function (a) { return a.name; });
  return Object.keys(FALLBACK_ACTIONS);
}

function summaryOf(schema, name) {
  if (schema && Array.isArray(schema.actions)) {
    for (var i = 0; i < schema.actions.length; i++) {
      if (schema.actions[i].name === name) return schema.actions[i].summary;
    }
  }
  return FALLBACK_ACTIONS[name] || '';
}

function paramsOf(schema, name) {
  if (!schema || !Array.isArray(schema.actions)) return null;
  for (var i = 0; i < schema.actions.length; i++) {
    if (schema.actions[i].name === name) return schema.actions[i].params || null;
  }
  return null;
}

// 只把这一类目真正引用到的共享 defs 带上,别把整份 defs 倒进上下文。
function referencedDefs(params, schema) {
  var defs = (schema && schema.defs) || {};
  var out = {};
  var queue = [];
  function walk(p) {
    if (!p || typeof p !== 'object') return;
    if (Array.isArray(p)) {
      for (var i = 0; i < p.length; i++) walk(p[i]);
      return;
    }
    if (typeof p.ref === 'string') queue.push(p.ref);
    if (p.properties) {
      for (var k in p.properties) if (Object.prototype.hasOwnProperty.call(p.properties, k)) walk(p.properties[k]);
    }
    if (p.items) walk(p.items);
  }
  walk(params);
  while (queue.length) {
    var name = queue.shift();
    if (!name || out[name] || !defs[name]) continue;
    out[name] = defs[name];
    walk(defs[name]);
  }
  return out;
}

async function sendResult(msg, ok, result, err) {
  var payload = { type: 'tool-result', callId: msg.callId, ok: !!ok };
  if (ok) payload.result = result;
  else {
    payload.errorCode = (err && err.errorCode) || 'INTERNAL';
    payload.message = (err && err.message) || 'unknown failure';
  }
  await cindy.send(payload);
}

// ─────────────────────────── list_tools ───────────────────────────

async function handleListTools(msg) {
  var args = msg.args || {};
  var category = typeof args.category === 'string' ? args.category.trim() : '';
  var schema = await loadSchema();

  if (!category) {
    var overview = {
      ok: true,
      runtime: schema ? 'live' : 'degraded',
      categories: CATEGORIES.map(function (c) {
        return { key: c.key, label: pick(c.label), when: pick(c.when), count: c.actions.length, actions: c.actions };
      }),
      hint: 'Call list_tools(category=<key>) for each operation\'s parameters plus the RULES for that category; then call_tool(name, args).',
      rules: { envelope: pick(ENVELOPE_NOTE), manual: pick(MANUAL_HINT) }
    };
    if (!schema) {
      overview.warning = 'The bundled mail-app-cli could not be read (' + (schemaFailed || 'unknown') + '). The operation list below is a static fallback; call_tool will fail until this is fixed.';
    }
    return sendResult(msg, true, overview);
  }

  var cat = null;
  for (var i = 0; i < CATEGORIES.length; i++) if (CATEGORIES[i].key === category) cat = CATEGORIES[i];
  if (!cat) {
    return sendResult(msg, false, null, {
      errorCode: 'UNKNOWN_CATEGORY',
      message: 'no category "' + category + '"; valid categories: ' + CATEGORIES.map(function (c) { return c.key; }).join(', ')
    });
  }

  var tools = cat.actions.map(function (name) {
    return {
      name: name,
      summary: summaryOf(schema, name),
      params: paramsOf(schema, name)
    };
  });
  var defs = referencedDefs(tools.map(function (t) { return t.params; }), schema);

  var out = { ok: true, category: cat.key, label: pick(cat.label), tools: tools };
  if (Object.keys(defs).length) out.defs = defs;
  out.rules = { envelope: pick(ENVELOPE_NOTE), manual: pick(MANUAL_HINT) };
  out.rules[cat.key] = rulesOf(pick(cat.rules));
  if (!schema) out.warning = 'Static fallback list: the bundled mail-app-cli could not be read (' + (schemaFailed || 'unknown') + ').';
  return sendResult(msg, true, out);
}

// ─────────────────────────── call_tool ───────────────────────────

async function handleCallTool(msg) {
  var args = msg.args || {};
  var name = typeof args.name === 'string' ? args.name.trim() : '';
  var params = (args.args && typeof args.args === 'object' && !Array.isArray(args.args)) ? args.args : {};

  if (!name) {
    return sendResult(msg, false, null, {
      errorCode: 'UNKNOWN_OPERATION',
      message: 'name is required. Call list_tools() for the categories and list_tools(category=...) for operation names.'
    });
  }

  var schema = await loadSchema();
  var known = allNames(schema);
  if (known.indexOf(name) === -1) {
    return sendResult(msg, false, null, {
      errorCode: 'UNKNOWN_OPERATION',
      message: 'no such operation "' + name + '". Available: ' + known.join(', ') +
        '. Call list_tools(category=...) for parameters and rules before retrying.'
    });
  }

  var plan = confirmPlan(name, params);
  if (plan) {
    var answer;
    try {
      answer = await cindy.confirm(plan);
    } catch (e) {
      answer = { ok: false, errorCode: 'INTERNAL' };
    }
    if (!answer || !answer.ok) {
      return sendResult(msg, false, null, {
        errorCode: 'CONFIRM_UNAVAILABLE',
        message: 'could not ask the user for confirmation (' + ((answer && answer.errorCode) || 'no answer') + '); nothing was changed. Report that, and do not retry.'
      });
    }
    if (!answer.confirmed) {
      return sendResult(msg, false, null, {
        errorCode: 'CONFIRM_DENIED',
        message: 'the user declined this operation. Nothing was changed. Do not ask again or retry; just tell the user it was left alone.'
      });
    }
  }

  var call;
  try {
    call = await cindy.node.request({
      method: 'mail/run',
      params: { action: name, params: params },
      timeoutMs: 30000,
      maxTotalMs: absoluteCapMs(name, params)
    });
  } catch (e) {
    call = { ok: false, message: String((e && e.message) || e) };
  }

  if (!call || !call.ok) {
    return sendResult(msg, false, null, {
      errorCode: 'NODE_REQUEST_FAILED',
      message: (call && call.message) || 'the Node worker did not answer'
    });
  }

  var res = call.result;
  if (!res || !res.ok) {
    var detail = [(res && res.message) || 'mail-app-cli failed'];
    if (res && res.stderr) detail.push(String(res.stderr).slice(0, 800));
    return sendResult(msg, false, null, {
      errorCode: (res && res.code) || 'RUN_FAILED',
      message: detail.join('\n')
    });
  }
  return sendResult(msg, true, res.envelope);
}

// ─────────────────────────── 事件入口 ───────────────────────────

cindy.onHostMessage(async function (msg) {
  if (!msg) return;

  if (msg.type === 'host-context-changed') {
    if (msg.context && msg.context.locale) LOCALE = msg.context.locale;
    return;
  }
  if (msg.type !== 'tool-call') return;

  await localeReady;
  try {
    if (msg.tool === 'list_tools') return await handleListTools(msg);
    if (msg.tool === 'call_tool') return await handleCallTool(msg);
  } catch (e) {
    return await sendResult(msg, false, null, {
      errorCode: 'INTERNAL',
      message: String((e && e.message) || e)
    });
  }
});
