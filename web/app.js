'use strict';

// Service Registry 控制面板：纯原生 JS，所有数据都通过公开只读接口拉取。
// 写操作（创建命名空间 / 轮换令牌 / 删除实例）需要令牌，填写在右上角。

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

let TOKEN_KEY = 'service-registry-token';
$('#token').value = localStorage.getItem(TOKEN_KEY) || '';
$('#token').addEventListener('change', () => {
  localStorage.setItem(TOKEN_KEY, $('#token').value);
});

function authHeaders() {
  const t = $('#token').value.trim();
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

function toast(msg, bad) {
  const t = $('#toast');
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

// ---- 页签 ----
let activeTab = 'overview';
document.querySelectorAll('.tab').forEach((btn) => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tab').forEach((b) => b.classList.remove('tab--active'));
    document.querySelectorAll('.tabpanel').forEach((p) => p.classList.remove('tabpanel--active'));
    btn.classList.add('tab--active');
    activeTab = btn.dataset.tab;
    $('#tab-' + activeTab).classList.add('tabpanel--active');
    refreshActive();
  });
});

function refreshActive() {
  switch (activeTab) {
    case 'overview': return renderOverview();
    case 'services': return renderServices();
    case 'instances': return renderInstances();
    case 'changes': return renderChanges();
    case 'namespaces': return renderNamespaces();
    default: return Promise.resolve();
  }
}

setInterval(() => {
  if ($('#autorefresh').checked && activeTab !== 'stream') refreshActive();
}, 3000);

// ---- 概览 ----
async function renderOverview() {
  const [health, meta] = await Promise.all([api('/health'), api('/v1/meta')]);
  const badge = $('#health-badge');
  if (health.ok) {
    badge.textContent = '运行中';
    badge.className = 'badge badge--ok';
  } else {
    badge.textContent = '不可用';
    badge.className = 'badge badge--bad';
  }
  if (!meta.ok) { toast('读取 /v1/meta 失败：' + JSON.stringify(meta.data), true); return; }
  const m = meta.data;
  const wa = $('#write-auth-badge');
  if (m.writeAuth === 'open') {
    wa.textContent = '写接口开放（无需令牌）';
    wa.className = 'badge badge--warn';
    wa.title = 'REGISTRY_WRITE_AUTH=open：任何人都能登记/修改/删除元信息。'
      + '收紧方式：backend/.env 里改成 token 后重启。';
  } else {
    wa.textContent = '写接口需令牌';
    wa.className = 'badge badge--ok';
    wa.title = 'REGISTRY_WRITE_AUTH=token：写操作需要 admin 令牌或命名空间令牌。';
  }
  $('#meta-version').textContent = 'v' + m.version;
  $('#meta-revision').textContent = 'revision ' + m.revision;
  $('#meta-uptime').textContent = 'uptime ' + m.uptimeSeconds + 's';

  const cards = [
    ['命名空间', m.counts.namespaces],
    ['服务契约', m.counts.services],
    ['实例', m.counts.instances],
    ['API 端点', m.counts.endpoints],
    ['全局 revision', m.revision],
    ['变更记录', m.counts.changes],
  ];
  const box = $('#overview-cards');
  box.innerHTML = cards.map(([label, value]) =>
    `<div class="card"><div class="card__label">${esc(label)}</div><div class="card__value">${esc(value)}</div></div>`).join('');
  $('#overview-semantics').textContent = m.semantics;

  const base = location.origin;
  $('#overview-pull').textContent = [
    `# 1) 冷启动：拉全量快照（带 ETag，之后可只判 304）`,
    `curl -sS ${base}/v1/snapshot -H "If-None-Match: <上次的 ETag>"`,
    ``,
    `# 2) 增量：按 revision 游标拉变更（?wait=30s 为 long-poll，挂了等新变更）`,
    `curl -sS '${base}/v1/changes?since=<last>&wait=30s&limit=200'`,
    ``,
    `# 3) 实时：SSE 订阅（面板"实时事件流"页签就是它）`,
    `curl -N ${base}/v1/events`,
    ``,
    `# 4) 发现：找服务 / 找实例 / 反查接口提供方`,
    `curl -sS '${base}/v1/services?tag=events'`,
    `curl -sS '${base}/v1/namespaces/default/services/event-center/instances?pick=random'`,
    `curl -sS '${base}/v1/search/apis?method=GET&path=/v1/streams/abc/events'`,
  ].join('\n');
}

// ---- 服务目录 ----
function serviceMatches(svc, filter) {
  if (!filter) return true;
  const hay = [svc.namespace, svc.name, svc.description, svc.owner, svc.version].join(' ').toLowerCase();
  return hay.includes(filter.toLowerCase());
}

let lastServices = [];

function syncServiceSelect(services) {
  lastServices = services;
  const sel = $('#inst-service');
  const keep = sel.value;
  sel.innerHTML = '<option value="">选择一个服务…</option>' + services.map((s) =>
    `<option value="${esc(s.namespace)}/${esc(s.name)}">${esc(s.namespace)}/${esc(s.name)} (${esc(s.instanceCount)} 实例)</option>`).join('');
  if (services.some((s) => s.namespace + '/' + s.name === keep)) sel.value = keep;
}

async function renderServices() {
  const res = await api('/v1/services');
  if (!res.ok) { toast('读取服务列表失败：' + JSON.stringify(res.data), true); return; }
  const services = res.data.services || [];

  const tagSelect = $('#svc-tag-filter');
  const tags = new Set();
  services.forEach((s) => (s.tags || []).forEach((t) => tags.add(t)));
  const keep = tagSelect.value;
  tagSelect.innerHTML = '<option value="">全部标签</option>' +
    [...tags].sort().map((t) => `<option value="${esc(t)}">${esc(t)}</option>`).join('');
  tagSelect.value = tags.has(keep) ? keep : '';

  const filter = $('#svc-filter').value.trim();
  const tagFilter = tagSelect.value;
  const shown = services.filter((s) =>
    serviceMatches(s, filter) && (!tagFilter || (s.tags || []).includes(tagFilter)));

  const box = $('#svc-list');
  box.innerHTML = '';
  box.appendChild(el('div', 'muted', `共 ${services.length} 个服务，显示 ${shown.length} 个`));
  shown.forEach((svc) => box.appendChild(serviceCard(svc)));
  syncServiceSelect(services);
}

function serviceCard(svc) {
  const api = svc.api || {};
  const endpoints = api.endpoints || [];
  const card = el('div', 'item');
  const specBadge = api.hasSpec
    ? `<span class="badge badge--info">OpenAPI ${esc(shortHash(api.specHash))} · ${esc(api.specBytes)}B</span>`
    : '<span class="badge badge--warn">仅显式端点</span>';
  const tags = (svc.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join('');
  const protocols = (api.protocols || []).map((p) => `<span class="tag">${esc(p)}</span>`).join('');

  card.innerHTML = `
    <div class="item__head">
      <span class="item__title">${esc(svc.namespace)}/${esc(svc.name)}</span>
      ${svc.version ? `<span class="item__meta">v${esc(svc.version)}</span>` : ''}
      <span class="badge ${svc.instanceCount > 0 ? 'badge--ok' : 'badge--warn'}">${esc(svc.instanceCount)} 个实例</span>
      <span class="badge badge--muted">${esc(endpoints.length)} 个端点</span>
      ${specBadge}
      <span class="item__meta">revision ${esc(svc.revision || 0)} · 更新 ${esc(fmtTime(svc.updatedAt))}</span>
    </div>
    ${svc.description ? `<p class="item__desc">${esc(svc.description)}</p>` : ''}
    <div class="item__meta" style="margin-top:6px">
      ${tags}${protocols}${svc.owner ? `<span class="tag">owner:${esc(svc.owner)}</span>` : ''}
      ${svc.healthPath ? `<span class="tag">health:${esc(svc.healthPath)}</span>` : ''}
      ${svc.registeredBy ? `<span class="tag">来源:${esc(svc.registeredBy)}</span>` : ''}
    </div>`;

  const details = el('details');
  details.innerHTML = `<summary>展开：对外 API（${endpoints.length} 个端点）与调用示例</summary>`;
  const body = el('div', 'item__body');
  body.appendChild(endpointsTable(endpoints, api.docsUrl, api.specUrl, svc));
  details.appendChild(body);
  details.addEventListener('toggle', async () => {
    if (!details.open || details.dataset.loaded) return;
    details.dataset.loaded = '1';
    await fillCallExample(body, svc, endpoints);
  });
  card.appendChild(details);

  const actions = el('div', 'actions');
  actions.style.marginTop = '10px';
  const ns = encodeURIComponent(svc.namespace);
  const name = encodeURIComponent(svc.name);
  if (api.hasSpec) {
    const specLink = el('a', 'btn btn--small', '下载 OpenAPI');
    specLink.href = `/v1/namespaces/${ns}/services/${name}/spec?download=1`;
    actions.appendChild(specLink);
  }
  const instBtn = el('button', 'btn btn--small', '看实例');
  instBtn.addEventListener('click', () => {
    $('#inst-service').value = svc.namespace + '/' + svc.name;
    document.querySelector('.tab[data-tab="instances"]').click();
  });
  actions.appendChild(instBtn);
  const del = el('button', 'btn btn--small btn--danger', '删除契约');
  del.addEventListener('click', async () => {
    if (!confirm(`确认删除 ${svc.namespace}/${svc.name}（连带其实例与端点索引）？`)) return;
    const r = await api(`/v1/namespaces/${ns}/services/${name}`, { method: 'DELETE' });
    if (r.ok) { toast('已删除'); renderServices(); } else { toast('删除失败：' + JSON.stringify(r.data), true); }
  });
  actions.appendChild(del);
  card.appendChild(actions);
  return card;
}


function endpointsTable(endpoints, docsUrl, specUrl, svc) {
  const wrap = el('div');
  if (!endpoints.length) {
    wrap.innerHTML = '<p class="note">该服务没有登记端点索引。</p>';
  } else {
    const rows = endpoints.map((e) => `
      <tr>
        <td>${methodBadge(e.method)}</td>
        <td class="mono">${esc(e.path)}</td>
        <td>${esc(e.summary || '')}</td>
        <td>${(e.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join('')}</td>
        <td class="mono">${(e.auth || []).map(esc).join(', ')}</td>
      </tr>`).join('');
    wrap.innerHTML = `
      <table>
        <thead><tr><th>方法</th><th>路径</th><th>说明</th><th>标签</th><th>鉴权</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>`;
  }
  const ns = encodeURIComponent(svc.namespace);
  const name = encodeURIComponent(svc.name);
  const links = [];
  if (docsUrl) links.push(`<a href="${esc(docsUrl)}" target="_blank" rel="noopener" class="btn btn--small">API 文档</a>`);
  if (specUrl) links.push(`<a href="${esc(specUrl)}" target="_blank" rel="noopener" class="btn btn--small">外部 spec 链接</a>`);
  links.push(`<a href="/v1/namespaces/${ns}/services/${name}/spec" target="_blank" rel="noopener" class="btn btn--small">查看内联 spec</a>`);
  const linkRow = el('div', 'actions');
  linkRow.style.marginTop = '8px';
  linkRow.innerHTML = links.join(' ');
  wrap.appendChild(linkRow);
  return wrap;
}

async function fillCallExample(body, svc, endpoints) {
  const ns = encodeURIComponent(svc.namespace);
  const name = encodeURIComponent(svc.name);
  const res = await api(`/v1/namespaces/${ns}/services/${name}/instances`);
  const instances = (res.ok && res.data.instances) || [];
  const base = instances.length
    ? `${instances[0].scheme}://${instances[0].host}:${instances[0].port}`
    : 'http://<实例地址>';
  const sample = endpoints.find((e) => e.method === 'GET') || endpoints[0];
  const ep = sample || { method: 'GET', path: svc.healthPath || '/' };
  const path = ep.path.replace(/\{[^}]+\}/g, 'VALUE');
  const authHeader = (sample && sample.auth && sample.auth.length)
    ? ` \\\n  -H 'Authorization: Bearer <token>'   # 鉴权方式见上表`
    : '';
  const lines = [
    `# base URL 取自已登记的实例（本中心只存元信息，不保证它可达）`,
    `curl -sS -X ${ep.method} '${base}${path}'${authHeader}`,
  ];
  if (!instances.length) {
    lines.push('', `# 该服务还没有登记实例：先用 PUT /v1/namespaces/${svc.namespace}/services/${svc.name}/instances 声明实例集合`);
  }
  body.appendChild(el('h2', null, '调用示例'));
  body.appendChild(el('pre', 'code', lines.join('\n')));
}

// ---- API 检索 ----
async function renderSearch() {
  const method = $('#api-method').value;
  const path = $('#api-path').value.trim();
  const match = $('#api-match').value;
  const qs = new URLSearchParams();
  if (method) qs.set('method', method);
  if (path) qs.set('path', path);
  if (match) qs.set('match', match);
  const res = await api('/v1/search/apis?' + qs.toString());
  const box = $('#api-results');
  box.innerHTML = '';
  if (!res.ok) { toast('检索失败：' + JSON.stringify(res.data), true); return; }
  const matches = res.data.matches || [];
  if (!matches.length) {
    box.appendChild(el('p', 'note', '没有命中。提示：查具体路径时会自动尝试模板匹配（/v1/x/{id}）；也可用 /v1/x/** 做前缀检索。'));
    return;
  }
  box.appendChild(el('div', 'muted', `命中 ${matches.length} 条${res.data.hasMore ? '（还有更多）' : ''}`));
  const rows = matches.map((m) => `
    <tr>
      <td>${methodBadge(m.endpoint.method)}</td>
      <td class="mono">${esc(m.endpoint.path)}</td>
      <td class="mono">${esc(m.namespace)}/${esc(m.service)}</td>
      <td>${esc(m.serviceVersion || '')}</td>
      <td>${esc(m.instanceCount)}</td>
      <td><span class="badge badge--muted">${esc(m.matchType)}</span></td>
      <td>${esc(m.endpoint.summary || '')}</td>
    </tr>`).join('');
  const wrap = el('div', 'item');
  wrap.innerHTML = `<table>
    <thead><tr><th>方法</th><th>登记的路径</th><th>服务</th><th>版本</th><th>实例</th><th>命中方式</th><th>说明</th></tr></thead>
    <tbody>${rows}</tbody></table>`;
  box.appendChild(wrap);
}

// ---- 实例 ----
async function renderInstances() {
  const box = $('#inst-list');
  const sel = $('#inst-service').value;
  if (!sel) {
    const svcRes = await api('/v1/services');
    if (svcRes.ok) syncServiceSelect(svcRes.data.services || []);
    box.innerHTML = '<p class="note">先选择一个服务。</p>';
    return;
  }
  const [ns, svc] = sel.split('/');
  const res = await api(`/v1/namespaces/${encodeURIComponent(ns)}/services/${encodeURIComponent(svc)}/instances`);
  box.innerHTML = '';
  if (!res.ok) { toast('读取实例失败：' + JSON.stringify(res.data), true); return; }
  const instances = res.data.instances || [];
  box.appendChild(el('p', 'note',
    `${ns}/${svc} 共 ${instances.length} 个实例。本中心只记录登记结果，不探活——可用性由消费方自行校验（契约里的 healthPath 是元信息）。`));
  if (!instances.length) { return; }
  const rows = instances.map((i) => `
    <tr>
      <td class="mono">${esc(i.id)}</td>
      <td class="mono">${esc(i.scheme)}://${esc(i.host)}:${esc(i.port)}</td>
      <td class="mono">${esc(JSON.stringify(i.metadata || {}))}</td>
      <td>${esc(fmtTime(i.updatedAt))}</td>
      <td class="mono">${esc(i.registeredBy || '')}</td>
      <td><button class="btn btn--small btn--danger" data-id="${esc(i.id)}">注销</button></td>
    </tr>`).join('');
  const wrap = el('div', 'item');
  wrap.innerHTML = `<table>
    <thead><tr><th>ID</th><th>地址</th><th>metadata</th><th>更新时间</th><th>来源</th><th></th></tr></thead>
    <tbody>${rows}</tbody></table>`;
  wrap.querySelectorAll('button[data-id]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const id = btn.dataset.id;
      if (!confirm('确认注销实例 ' + id + '？')) return;
      const r = await api(`/v1/namespaces/${encodeURIComponent(ns)}/services/${encodeURIComponent(svc)}/instances/${encodeURIComponent(id)}`, { method: 'DELETE' });
      if (r.ok) { toast('已注销'); renderInstances(); } else { toast('注销失败：' + JSON.stringify(r.data), true); }
    });
  });
  box.appendChild(wrap);
}


// ---- 变更与审计 ----
async function renderChanges() {
  const since = $('#chg-since').value.trim();
  const qs = new URLSearchParams({ limit: '200' });
  if (since) qs.set('since', since);
  const res = await api('/v1/changes?' + qs.toString());
  const box = $('#chg-list');
  box.innerHTML = '';
  if (!res.ok) { toast('读取变更失败：' + JSON.stringify(res.data), true); return; }
  const filter = $('#chg-filter').value.trim().toLowerCase();
  const changes = (res.data.changes || []).filter((c) => {
    if (!filter) return true;
    return [c.ref, c.actor, c.detail, c.entity, c.op].join(' ').toLowerCase().includes(filter);
  });
  box.appendChild(el('p', 'note',
    `当前 revision ${res.data.revision}；显示 ${changes.length} 条${res.data.hasMore ? '（还有更多，可用 since/limit 翻页）' : ''}。` +
    `每一条都同时是增量拉取的游标与审计记录。`));
  if (!changes.length) return;
  const rows = changes.slice().reverse().map((c) => `
    <tr>
      <td class="mono">${esc(c.revision)}</td>
      <td>${esc(fmtTime(c.at))}</td>
      <td class="mono">${esc(c.entity)}</td>
      <td class="mono">${esc(c.ref)}</td>
      <td>${esc(c.op)}</td>
      <td class="mono">${esc(c.actor || '')}</td>
      <td>${esc(c.detail || '')}</td>
    </tr>`).join('');
  const wrap = el('div', 'item');
  wrap.innerHTML = `<table>
    <thead><tr><th>rev</th><th>时间</th><th>实体</th><th>引用</th><th>操作</th><th>谁</th><th>说明</th></tr></thead>
    <tbody>${rows}</tbody></table>`;
  box.appendChild(wrap);
}

// ---- 命名空间 ----
async function renderNamespaces() {
  const res = await api('/v1/namespaces');
  const box = $('#ns-list');
  box.innerHTML = '';
  if (!res.ok) { toast('读取命名空间失败：' + JSON.stringify(res.data), true); return; }
  const list = res.data.namespaces || [];
  const rows = list.map((n) => `
    <tr>
      <td class="mono">${esc(n.name)}</td>
      <td>${esc(n.description || '')}</td>
      <td>${n.tokenSet ? '<span class="badge badge--ok">已配置</span>' : '<span class="badge badge--warn">未配置</span>'}</td>
      <td>${esc(n.serviceCount)}</td>
      <td>${esc(n.instanceCount)}</td>
      <td>${esc(fmtTime(n.createdAt))}</td>
      <td>
        <button class="btn btn--small" data-act="rotate" data-ns="${esc(n.name)}">轮换令牌</button>
        <button class="btn btn--small" data-act="clear" data-ns="${esc(n.name)}">清除令牌</button>
      </td>
    </tr>`).join('');
  const wrap = el('div', 'item');
  wrap.innerHTML = `<table>
    <thead><tr><th>名称</th><th>描述</th><th>注册令牌</th><th>服务</th><th>实例</th><th>创建时间</th><th></th></tr></thead>
    <tbody>${rows}</tbody></table>`;
  wrap.querySelectorAll('button[data-act]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const ns = btn.dataset.ns;
      const url = `/v1/namespaces/${encodeURIComponent(ns)}/token`;
      const r = btn.dataset.act === 'rotate'
        ? await api(url, { method: 'POST' })
        : await api(url, { method: 'DELETE' });
      if (!r.ok) { toast('操作失败：' + JSON.stringify(r.data), true); return; }
      const out = $('#ns-token-out');
      out.hidden = false;
      out.textContent = r.data.token
        ? `命名空间 ${ns} 的新令牌（只显示这一次，请立即保存）：\n${r.data.token}`
        : `命名空间 ${ns} 已清除令牌：此后只能由 admin 令牌写入。`;
      renderNamespaces();
    });
  });
  box.appendChild(wrap);
}

// ---- 实时事件流 ----
let eventSource = null;
function toggleStream() {
  const status = $('#stream-status');
  const log = $('#stream-log');
  if (eventSource) {
    eventSource.close();
    eventSource = null;
    status.textContent = '已断开';
    return;
  }
  eventSource = new EventSource('/v1/events');
  status.textContent = '已连接';
  eventSource.addEventListener('hello', (e) => {
    log.textContent += `· 已连接，当前 revision=${JSON.parse(e.data).revision}\n`;
  });
  eventSource.addEventListener('change', (e) => {
    const c = JSON.parse(e.data);
    log.textContent += `[${c.revision}] ${c.op} ${c.entity} ${c.ref} — ${c.detail || ''}\n`;
    log.scrollTop = log.scrollHeight;
  });
  eventSource.onerror = () => {
    status.textContent = '连接异常（浏览器会自动重连）';
  };
}

// ---- 事件绑定与启动 ----
$('#svc-refresh').addEventListener('click', renderServices);
$('#svc-filter').addEventListener('input', renderServices);
$('#svc-tag-filter').addEventListener('change', renderServices);
$('#api-search').addEventListener('click', renderSearch);
$('#api-path').addEventListener('keydown', (e) => { if (e.key === 'Enter') renderSearch(); });
$('#inst-refresh').addEventListener('click', renderInstances);
$('#inst-service').addEventListener('change', renderInstances);
$('#chg-refresh').addEventListener('click', renderChanges);
$('#chg-filter').addEventListener('input', renderChanges);
$('#stream-toggle').addEventListener('click', toggleStream);
$('#ns-create').addEventListener('click', async () => {
  const name = $('#ns-name').value.trim();
  const description = $('#ns-desc').value.trim();
  if (!name) { toast('请填写命名空间名称', true); return; }
  const r = await api('/v1/namespaces', { method: 'POST', body: { name, description } });
  if (!r.ok) { toast('创建失败：' + JSON.stringify(r.data), true); return; }
  const out = $('#ns-token-out');
  out.hidden = false;
  out.textContent = `命名空间 ${name} 已创建。注册令牌（只显示这一次，请立即保存）：\n${r.data.token}`;
  $('#ns-name').value = '';
  $('#ns-desc').value = '';
  renderNamespaces();
});

renderOverview();

