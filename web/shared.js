'use strict';

// 两个页面共用的最小工具集（原生 JS，无框架、无构建步骤）：
//   · index.html + app.js          —— 控制面板（服务目录 / 服务树 / 实例 / 变更 / 命名空间 / 事件流）
//   · contract.html + contract.js  —— 契约编辑页（独立页面，登记与编辑都在这里）
//
// 放在一起是因为两边都要用：令牌、API 调用、toast、部门目录（组织接口）、表单反馈。
// 表单反馈这块是踩过坑的：本地校验失败曾经只写一个 12px 灰字，用户看到的是"点了没反应"。
// 所以统一走 formFail —— 红色常驻提示 + toast + 标红并聚焦出错字段，绝不静默。

// ---- DOM 小工具 ----
const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, text) => {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text !== undefined) node.textContent = text;
  return node;
};
const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
}[c]));
function show(node, on) { if (node) node.hidden = !on; }

// ---- 令牌 ----
// 只存在浏览器本地（localStorage），两个页面同源共享：在面板里填过一次，编辑页就不用再填。
const TOKEN_KEY = 'service-registry-token';
function getToken() { return localStorage.getItem(TOKEN_KEY) || ''; }
function setToken(v) { localStorage.setItem(TOKEN_KEY, String(v || '').trim()); }
function mountTokenInput(sel) {
  const input = $(sel);
  if (!input) return;
  input.value = getToken();
  // 监听 input 而不是 change：点提交按钮之前不一定失焦，等 change 就晚了（会拿旧令牌去写）。
  input.addEventListener('input', () => setToken(input.value));
}

function authHeaders() {
  const t = getToken().trim();
  return t ? { 'X-Registry-Token': t } : {};
}

async function api(path, opts = {}) {
  const init = { method: opts.method || 'GET', headers: { ...authHeaders() } };
  if (opts.body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.body);
  }
  const res = await fetch(path, init);
  let data = null;
  const text = await res.text();
  if (text) {
    try { data = JSON.parse(text); } catch (_) { data = { raw: text }; }
  }
  return { ok: res.ok, status: res.status, data };
}

async function fetchText(path) {
  const res = await fetch(path, { headers: { ...authHeaders() } });
  return res.ok ? res.text() : '';
}

// ---- 展示小工具 ----
function toast(msg, bad) {
  const t = $('#toast');
  if (!t) return;
  t.textContent = msg;
  t.className = bad ? 'toast toast--bad' : 'toast';
  t.hidden = false;
  clearTimeout(toast._timer);
  toast._timer = setTimeout(() => { t.hidden = true; }, bad ? 8000 : 4000);
}

function fmtTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  return d.toLocaleString();
}

function shortHash(h) { return h ? h.slice(0, 8) : ''; }

function methodBadge(m) {
  return `<span class="method method--${m}">${esc(m)}</span>`;
}

function parseList(s) {
  return String(s || '').split(',').map((x) => x.trim()).filter(Boolean);
}

function parseKV(s) {
  const out = {};
  parseList(s).forEach((pair) => {
    const i = pair.indexOf('=');
    if (i > 0) out[pair.slice(0, i).trim()] = pair.slice(i + 1).trim();
  });
  return out;
}

function errText(data) {
  if (data && data.error && data.error.message) return data.error.message;
  return JSON.stringify(data);
}

// ---- 主题（深色 / 浅色）----
// styles.css 里只有一套颜色令牌（:root 是深色，[data-theme="light"] 是浅色）。
// 没选过就跟着系统走；选过就一直按选的来（两个页面共用，切一次两边都生效）。
const THEME_KEY = 'service-registry-theme';
function currentTheme() {
  const saved = localStorage.getItem(THEME_KEY);
  if (saved === 'light' || saved === 'dark') return saved;
  const prefersLight = typeof matchMedia === 'function'
    && matchMedia('(prefers-color-scheme: light)').matches;
  return prefersLight ? 'light' : 'dark';
}
function applyTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme === 'light' ? 'light' : 'dark');
  const btn = $('#theme-toggle');
  if (!btn) return;
  btn.textContent = theme === 'light' ? '☾' : '☀';
  btn.title = theme === 'light' ? '切到深色主题' : '切到浅色主题';
}
function mountThemeToggle(sel) {
  applyTheme(currentTheme());
  const btn = $(sel);
  if (!btn) return;
  btn.addEventListener('click', () => {
    const next = document.documentElement.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
    localStorage.setItem(THEME_KEY, next);
    applyTheme(next);
  });
}

// ---- 部门（数据来自组织架构服务，本中心只取回来透出） ----
// 服务契约的「部门」属性以**组织架构服务**（organization）为权威：
// 本中心不维护部门，只把组织接口的 `GET /api/v1/departments` 目录取回来，供
// ① 契约编辑页的下拉 ② 服务目录按部门过滤 ③ 卡片与检索结果展示 用。
//
// 下拉选项的 key 用 `id:<部门ID>` / `name:<部门名>`（而不是下标），
// 这样"只有名字"的部门（组织接口不可达时按声明值保存的）也不会在下拉里丢掉，
// 重新渲染（自动刷新每 3s）时用户的选择也不会被重置。
let deptCatalog = { departments: [], loaded: false };

function deptText(d) {
  const id = String((d && d.id) || '').trim();
  const name = String((d && d.name) || '').trim();
  if (id && name) return `${name}（${id}）`;
  return name || id;
}

// deptByKey 把下拉的 key 还原成 {id, name}：key 里带的就是权威值，不查目录也能还原。
function deptByKey(key) {
  const k = String(key || '');
  if (k.startsWith('id:')) {
    const id = k.slice(3);
    const hit = (deptCatalog.departments || []).find((d) => String(d.id || '').trim() === id);
    return { id, name: (hit && String(hit.name || '').trim()) || '' };
  }
  if (k.startsWith('name:')) return { id: '', name: k.slice(5) };
  return null;
}

// 一个服务归属的部门：ID 优先（跨改名稳定），其次名字。
function svcDeptKey(svc) {
  const id = String((svc && svc.departmentId) || '').trim();
  const name = String((svc && svc.departmentName) || '').trim();
  if (id) return 'id:' + id;
  if (name) return 'name:' + name;
  return '';
}

function svcDeptMatches(svc, key) {
  if (!key) return true;
  const d = deptByKey(key);
  if (!d) return true;
  if (d.id) return String(svc.departmentId || '').trim() === d.id;
  return String(svc.departmentName || '').trim() === d.name;
}

function deptTag(svc) {
  // 卡片上显示**这个服务自己存的**部门（ID + 名称），不查目录：
  // 目录可能暂时取不到，但"它登记的是哪个部门"是事实，必须照实显示。
  const label = deptText({ id: svc && svc.departmentId, name: svc && svc.departmentName });
  if (!label) return '';
  return `<span class="tag" title="${esc(`归属部门：${label}（数据来自组织接口，非本中心自编）`)}">部门:${esc(label)}</span>`;
}

// loadDepartments 拉一次部门目录。force=true 时带 ?refresh=1 跳过服务端 TTL 缓存。
// 刻意不抛异常：组织接口不可达时目录里会带上 error/available，页面照常可用。
async function loadDepartments(force) {
  const res = await api('/v1/departments' + (force ? '?refresh=1' : ''));
  if (!res.ok || !res.data || typeof res.data !== 'object') {
    deptCatalog = {
      departments: [], loaded: true, enabled: true, available: false,
      error: res.ok ? '响应不是 JSON 目录' : errText(res.data),
    };
    return deptCatalog;
  }
  const cat = res.data;
  cat.departments = Array.isArray(cat.departments) ? cat.departments : [];
  cat.loaded = true;
  deptCatalog = cat;
  return cat;
}

// deptChoices 把"组织接口里的部门"和"服务契约上实际用到的部门"合成候选：
// 后者是为了不让组织接口不可达时按声明值保存的部门从界面上消失（否则编辑时会被清掉）。
function deptChoices(services) {
  const map = new Map();
  const add = (id, name) => {
    id = String(id || '').trim();
    name = String(name || '').trim();
    if (!id && !name) return;
    const key = id ? 'id:' + id : 'name:' + name;
    const cur = map.get(key);
    if (!cur) { map.set(key, { key, id, name }); return; }
    if (!cur.name && name) cur.name = name; // 同一个 ID 在别处带了名字，补上
    if (!cur.id && id) cur.id = id;
  };
  (deptCatalog.departments || []).forEach((d) => add(d.id, d.name));
  (services || []).forEach((s) => add(s.departmentId, s.departmentName));
  return [...map.values()].sort((a, b) => deptText(a).localeCompare(deptText(b), 'zh'));
}

// fillDeptSelects 刷新页面上的部门下拉（契约编辑页的表单 / 服务目录过滤 / API 检索过滤），
// 并尽量保留用户当前的选择（选项没变时下拉内容不变）。页面上没有的下拉直接跳过。
function fillDeptSelects(services) {
  const choices = deptChoices(services);
  const opts = choices.map((c) => `<option value="${esc(c.key)}">${esc(deptText(c))}</option>`).join('');
  const fill = (sel, emptyText) => {
    if (!sel) return;
    const keep = sel.value;
    sel.innerHTML = `<option value="">${esc(emptyText)}</option>` + opts;
    sel.value = choices.some((c) => c.key === keep) ? keep : '';
  };
  fill($('#svc-form-dept'), '（不指定部门）');
  fill($('#svc-dept-filter'), '全部部门');
  fill($('#api-dept'), '全部部门');
}

// renderDeptNote 把"部门目录的成色"写在契约编辑页里：
// 下拉为空时用户至少能看到是"没配置组织接口"还是"组织接口暂时不可达"。
function renderDeptNote() {
  const node = $('#svc-form-dept-note');
  if (!node) return;
  const c = deptCatalog;
  const n = (c.departments || []).length;
  const say = (msg, warn) => {
    node.textContent = msg;
    node.className = 'note grow' + (warn ? ' note--warn' : '');
  };
  if (!c.enabled) {
    say('组织接口未配置（REGISTRY_ORG_URL）：部门只当标签保存，不做对齐。', true);
    return;
  }
  if (c.available) {
    say(`已从组织接口同步 ${n} 个部门${c.cached ? '（缓存）' : ''}`
      + (c.url ? ` · ${c.url}` : '')
      + (c.fetchedAt ? ` · ${fmtTime(c.fetchedAt)}` : ''));
    return;
  }
  say(`组织接口暂时不可达：${c.error || '未知原因'}`
    + (n ? `；下面用的是上次同步的 ${n} 个部门（stale）` : '；此时部门按声明值保存，不影响登记'), true);
}


// ---- 表单反馈：绝不静默 ----
// 教训：本地校验失败时只写一个 12px 灰字，看起来就是"点了没反应、也不提示哪里错"。
// 现在统一走 formFail：红色常驻提示 + toast（表单不在视口也能看到）+ 标出出错字段。
function setHint(node, msg, kind) {
  if (!node) return;
  node.textContent = msg || '';
  node.className = 'hint' + (kind ? ' hint--' + kind : '');
}
function clearFieldErrors(scope) {
  if (!scope) return;
  scope.querySelectorAll('.field-error').forEach((n) => n.classList.remove('field-error'));
}
function markField(field) {
  if (!field) return;
  field.classList.add('field-error');
  if (field.focus) field.focus();
  if (field.scrollIntoView) field.scrollIntoView({ block: 'nearest' });
}
function formFail(hintNode, msg, fieldEl) {
  setHint(hintNode, '⚠ ' + msg, 'error');
  toast(msg, true);
  markField(fieldEl);
}
function formOK(hintNode, msg) {
  setHint(hintNode, '✓ ' + msg, 'ok');
}
// 提交期间禁用按钮并改文案：避免连点，也避免"点了没反应"的错觉。
async function withBusy(btn, busyText, fn) {
  const old = btn.textContent;
  btn.disabled = true;
  btn.textContent = busyText;
  try {
    return await fn();
  } finally {
    btn.disabled = false;
    btn.textContent = old;
  }
}
// 任何未捕获的脚本异常都变成可见提示（否则用户只会看到"没反应"）。
window.addEventListener('error', (e) => {
  toast('页面脚本出错：' + ((e && (e.message || e.error)) || 'unknown'), true);
});
window.addEventListener('unhandledrejection', (e) => {
  const r = e && e.reason;
  toast('页面请求出错：' + ((r && r.message) || r), true);
});

