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
  const hay = [svc.namespace, svc.name, svc.description, svc.owner, svc.version, svc.gitRepoUrl]
    .join(' ').toLowerCase();
  return hay.includes(filter.toLowerCase());
}

// 仓库地址在卡片上要短：https://github.com/org/repo.git → github.com/org/repo；
// scp 风格 git@github.com:org/repo.git → github.com/org/repo（host 后的冒号归一成斜杠，读起来一致）。
function repoLabel(url) {
  const raw = String(url || '').replace(/\.git$/, '');
  const scp = raw.match(/^[^@/\s]+@([^:/\s]+):(.+)$/);
  if (scp) return `${scp[1]}/${scp[2]}`;
  return raw.replace(/^[A-Za-z][A-Za-z0-9+.-]*:\/\//, '');
}

// 只有 http(s) 才做成可点链接：scp 风格（git@host:org/repo）和 ssh:// 浏览器打不开，
// 硬做成 <a href> 会变成个坏链接（被当成相对路径）。其余照常显示，只是不可点（悬停看原文）。
function repoIsClickable(url) { return /^https?:\/\//i.test(String(url || '')); }

function repoTag(url) {
  const title = `代码仓库：${esc(url)}`;
  if (repoIsClickable(url)) {
    return `<a class="tag tag--link" href="${esc(url)}" target="_blank" rel="noopener" title="${title}">repo:${esc(repoLabel(url))}</a>`;
  }
  return `<span class="tag" title="${title}（scp/ssh 地址：浏览器打不开，用 git clone）">repo:${esc(repoLabel(url))}</span>`;
}

let lastServices = [];

// 自动刷新（默认 3s，见文件末尾的 setInterval）会反复重建列表。
// 早期实现每次都把 #svc-list 整个换掉，于是用户刚点开的「展开：对外 API」会在下一次
// 刷新时被自动收起来（症状：点开看两眼，自己合上了）。三件事一起保证"点开就一直开着"：
//   ① lastServicesSig：数据 + 过滤条件没变就完全不碰 DOM（不闪、不丢滚动位置）；
//   ② expandedServices：按 ns/name 记住用户点开的卡片，重建后照样是展开的；
//   ③ serviceCards：数据没变的卡片直接复用原来的 DOM 节点（省一次实例请求，也不闪）。
let lastServicesSig = null;
const expandedServices = new Set();
const serviceCards = new Map();

function svcKey(svc) { return svc.namespace + '/' + svc.name; }

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
  const tags = [...new Set(services.flatMap((s) => s.tags || []))].sort();
  const filter = $('#svc-filter').value.trim();
  const tagFilter = tagSelect.value;
  const shown = services.filter((s) =>
    serviceMatches(s, filter) && (!tagFilter || (s.tags || []).includes(tagFilter)));

  // ① 数据与过滤条件都没变：直接返回，一行 DOM 都不动。
  //    （自动刷新每 3s 跑一次，早期这里是"重建整棵树"，于是展开的卡片被自动收起。）
  const sig = JSON.stringify({ filter, tagFilter, tags, services });
  if (sig === lastServicesSig) return;
  lastServicesSig = sig;

  const keep = tagSelect.value;
  tagSelect.innerHTML = '<option value="">全部标签</option>' +
    tags.map((t) => `<option value="${esc(t)}">${esc(t)}</option>`).join('');
  tagSelect.value = tags.includes(keep) ? keep : '';

  const box = $('#svc-list');
  box.innerHTML = '';
  box.appendChild(el('div', 'muted', `共 ${services.length} 个服务，显示 ${shown.length} 个`));
  // ③ 内容没变的卡片复用同一个节点（展开状态、调用示例都留在原地）。
  const next = new Map();
  shown.forEach((svc) => {
    const key = svcKey(svc);
    const cardSig = JSON.stringify(svc);
    const prev = serviceCards.get(key);
    const node = prev && prev.sig === cardSig ? prev.node : serviceCard(svc);
    next.set(key, { sig: cardSig, node });
    box.appendChild(node);
  });
  serviceCards.clear();
  next.forEach((v, k) => serviceCards.set(k, v));
  syncServiceSelect(services);
}

function serviceCard(svc) {
  const api = svc.api || {};
  const endpoints = api.endpoints || [];
  const card = el('div', 'item');
  const key = svcKey(svc);
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
      ${svc.gitRepoUrl ? repoTag(svc.gitRepoUrl) : ''}
      ${svc.registeredBy ? `<span class="tag">来源:${esc(svc.registeredBy)}</span>` : ''}
    </div>`;

  const details = el('details');
  const summary = el('summary');
  const summaryTail = `对外 API（${endpoints.length} 个端点）与调用示例`;
  const setSummary = (open) => { summary.textContent = (open ? '收起：' : '展开：') + summaryTail; };
  setSummary(false);
  details.appendChild(summary);
  const body = el('div', 'item__body');
  body.appendChild(endpointsTable(endpoints, api.docsUrl, api.specUrl, svc));
  details.appendChild(body);

  const loadExample = () => {
    if (details.dataset.loaded) return Promise.resolve();
    details.dataset.loaded = '1';
    return fillCallExample(body, svc, endpoints);
  };

  details.addEventListener('toggle', () => {
    setSummary(details.open);
    if (details.open) {
      expandedServices.add(key); // ② 记住"用户点开了它"，刷新后不要自动收起来
      return loadExample();
    }
    expandedServices.delete(key);
  });
  // ② 自动刷新重建过节点：按用户之前的操作恢复成展开，而不是一律收起。
  if (expandedServices.has(key)) {
    details.open = true;
    setSummary(true);
    loadExample();
  }
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
  const editBtn = el('button', 'btn btn--small', '编辑契约');
  editBtn.addEventListener('click', () => openServiceForm(svc));
  actions.appendChild(editBtn);
  const addInstBtn = el('button', 'btn btn--small', '＋ 加实例');
  addInstBtn.addEventListener('click', () => {
    $('#inst-service').value = svc.namespace + '/' + svc.name;
    document.querySelector('.tab[data-tab="instances"]').click();
    openInstanceForm();
  });
  actions.appendChild(addInstBtn);
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
  if (svc.gitRepoUrl && repoIsClickable(svc.gitRepoUrl)) {
    links.push(`<a href="${esc(svc.gitRepoUrl)}" target="_blank" rel="noopener" class="btn btn--small">代码仓库</a>`);
  }
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

// ---- 面板写入口：登记 / 编辑服务契约 ----
function show(node, on) { node.hidden = !on; }
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
async function fetchText(path) {
  const res = await fetch(path, { headers: { ...authHeaders() } });
  return res.ok ? res.text() : '';
}

async function fillNamespaceSelect(prefer) {
  const sel = $('#svc-form-ns');
  const res = await api('/v1/namespaces');
  const list = (res.ok && res.data.namespaces) || [];
  sel.innerHTML = list.map((n) => `<option value="${esc(n.name)}">${esc(n.name)}</option>`).join('');
  if (prefer && list.some((n) => n.name === prefer)) sel.value = prefer;
  return list;
}

function epRow(ep) {
  const methods = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'];
  const tr = document.createElement('tr');
  tr.innerHTML = `
    <td><select class="ep-method">${methods.map((m) =>
      `<option${ep && ep.method === m ? ' selected' : ''}>${m}</option>`).join('')}</select></td>
    <td><input class="ep-path" placeholder="/v1/things/{id}" value="${esc(ep ? ep.path : '')}" /></td>
    <td><input class="ep-summary" placeholder="说明" value="${esc(ep && ep.summary ? ep.summary : '')}" /></td>
    <td><button class="btn btn--small btn--danger" type="button">删</button></td>`;
  tr.querySelector('button').addEventListener('click', () => tr.remove());
  return tr;
}

function setEpRows(list) {
  const tbody = $('#svc-form-eps tbody');
  tbody.innerHTML = '';
  (list && list.length ? list : [null]).forEach((ep) => tbody.appendChild(epRow(ep)));
}

function setApiMode(mode) {
  $('#svc-form-mode').value = mode;
  show($('#svc-form-spec-wrap'), mode === 'spec');
  show($('#svc-form-manual-wrap'), mode === 'manual');
}

// ---- 表单反馈：绝不静默 ----
// 教训：本地校验失败时只写一个 12px 灰字，看起来就是"点了没反应、也不提示哪里错"。
// 现在统一走 formFail：红色常驻提示 + toast（表单不在视口也能看到）+ 标出出错字段。
function setHint(node, msg, kind) {
  node.textContent = msg || '';
  node.className = 'hint' + (kind ? ' hint--' + kind : '');
}
function clearFieldErrors(scope) {
  scope.querySelectorAll('.field-error').forEach((el) => el.classList.remove('field-error'));
}
function markField(el) {
  if (!el) return;
  el.classList.add('field-error');
  if (el.focus) el.focus();
  if (el.scrollIntoView) el.scrollIntoView({ block: 'nearest' });
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
  toast('面板脚本出错：' + ((e && (e.message || e.error)) || 'unknown'), true);
});
window.addEventListener('unhandledrejection', (e) => {
  const r = e && e.reason;
  toast('面板请求出错：' + ((r && r.message) || r), true);
});

// 最小可用的 OpenAPI 模板：让"不粘贴 spec 直接提交"也能成功，而不是卡在校验上。
function specTemplate(name) {
  return [
    'openapi: 3.0.3',
    'info:',
    '  title: ' + (name || 'my-service'),
    '  version: 0.1.0',
    'description: 面板生成的最小模板 —— 把 paths 换成真实接口，或整段替换为你们仓库里的 openapi.yaml',
    'paths:',
    '  /health:',
    '    get:',
    '      summary: 健康检查',
    '',
  ].join('\n');
}
let lastSpecTemplate = '';
function prefillSpecTemplate(force) {
  const spec = $('#svc-form-spec');
  const tpl = specTemplate($('#svc-form-name').value.trim());
  if (force || !spec.value.trim() || spec.value === lastSpecTemplate) {
    spec.value = tpl;
  }
  lastSpecTemplate = tpl;
}

// openServiceForm(null) = 登记新服务；传服务对象 = 编辑（服务名不可改：它是身份）。
async function openServiceForm(svc) {
  await fillNamespaceSelect(svc ? svc.namespace : undefined);
  const f = {
    ns: $('#svc-form-ns'), name: $('#svc-form-name'), version: $('#svc-form-version'),
    owner: $('#svc-form-owner'), health: $('#svc-form-health'), desc: $('#svc-form-desc'),
    tags: $('#svc-form-tags'), docs: $('#svc-form-docs'), repo: $('#svc-form-repo'),
    specurl: $('#svc-form-specurl'), spec: $('#svc-form-spec'),
  };
  if (svc) {
    $('#svc-form-title').textContent = `编辑服务契约 ${svc.namespace}/${svc.name}`;
    f.ns.value = svc.namespace;
    f.name.value = svc.name;
    f.name.disabled = true;
    f.version.value = svc.version || '';
    f.owner.value = svc.owner || '';
    f.health.value = svc.healthPath || '';
    f.desc.value = svc.description || '';
    f.repo.value = svc.gitRepoUrl || '';
    f.tags.value = (svc.tags || []).join(',');
    f.docs.value = (svc.api && svc.api.docsUrl) || '';
    f.specurl.value = (svc.api && svc.api.specUrl) || '';
    f.spec.value = '';
    if (svc.api && svc.api.hasSpec) {
      const raw = await fetchText(`/v1/namespaces/${encodeURIComponent(svc.namespace)}/services/${encodeURIComponent(svc.name)}/spec`);
      f.spec.value = raw;
      setApiMode('spec');
    } else {
      setEpRows(svc.api ? svc.api.endpoints : []);
      setApiMode('manual');
    }
  } else {
    $('#svc-form-title').textContent = '登记服务契约';
    f.name.disabled = false;
    ['name', 'version', 'owner', 'health', 'desc', 'tags', 'docs', 'repo', 'specurl', 'spec'].forEach((k) => { f[k].value = ''; });
    setEpRows(null);
    setApiMode('spec');
    prefillSpecTemplate(true); // 预填最小模板：不粘贴 spec 也能直接提交成功
  }
  setHint($('#svc-form-hint'), '');
  show($('#svc-form-card'), true);
  show($('#inst-form-card'), false);
  show($('#inst-batch-card'), false);
}

async function submitServiceForm() {
  const card = $('#svc-form-card');
  const hint = $('#svc-form-hint');
  clearFieldErrors(card);

  const nsEl = $('#svc-form-ns');
  const nameEl = $('#svc-form-name');
  const specEl = $('#svc-form-spec');
  const ns = nsEl.value;
  const name = nameEl.value.trim();

  // 校验失败一律 formFail：红色常驻提示 + toast + 标红并聚焦出错字段。
  if (!ns) {
    formFail(hint, '请选择命名空间（下拉为空说明还没有命名空间，先去「命名空间」页签新建一个）', nsEl);
    return;
  }
  if (!name) {
    formFail(hint, '服务名必填（小写字母/数字/._-，例如 event-center）', nameEl);
    return;
  }
  if (!/^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$/.test(name)) {
    formFail(hint, '服务名不合法：只能用小写字母/数字/._-，且以字母或数字开头结尾（≤63 字符）', nameEl);
    return;
  }

  const apiPart = { protocols: ['http'] };
  const docs = $('#svc-form-docs').value.trim();
  const specurl = $('#svc-form-specurl').value.trim();
  if (docs) apiPart.docsUrl = docs;
  if (specurl) apiPart.specUrl = specurl;

  // 代码仓库地址：本地先拦一道，错误直接指到字段（服务端也会校验，规则一致）。
  const repoEl = $('#svc-form-repo');
  const repo = repoEl.value.trim();
  if (repo && !/^[A-Za-z][A-Za-z0-9+.-]*:\/\/\S+$/.test(repo) && !/^[^\s@/]+@[^\s:/]+:\S+$/.test(repo)) {
    formFail(hint, 'gitRepoUrl 不合法：要仓库地址，例如 https://github.com/org/repo.git、' +
      'ssh://git@host/org/repo.git 或 git@host:org/repo.git（本中心只存不克隆）', repoEl);
    return;
  }

  if ($('#svc-form-mode').value === 'spec') {
    const spec = specEl.value;
    if (!spec.trim()) {
      formFail(hint, 'API 原文为空：粘贴 OpenAPI（YAML/JSON），或点「预填最小模板」，或切到「手工声明端点」', specEl);
      return;
    }
    apiPart.spec = spec;
  } else {
    const eps = [];
    for (const tr of [...document.querySelectorAll('#svc-form-eps tbody tr')]) {
      const pathEl = tr.querySelector('.ep-path');
      const path = pathEl.value.trim();
      if (!path) continue; // 空行忽略
      if (!path.startsWith('/')) {
        formFail(hint, '端点路径必须以 / 开头，当前是：' + path, pathEl);
        return;
      }
      eps.push({
        method: tr.querySelector('.ep-method').value,
        path,
        summary: tr.querySelector('.ep-summary').value.trim(),
      });
    }
    if (!eps.length) {
      formFail(hint, '至少声明一个端点：点「＋ 加一行」，填上 method 与 path（如 GET /health）');
      return;
    }
    apiPart.endpoints = eps;
  }

  const healthEl = $('#svc-form-health');
  const health = healthEl.value.trim();
  if (health && !health.startsWith('/')) {
    formFail(hint, 'healthPath 必须以 / 开头（它是元信息，供消费方/看门狗自行探活）：' + health, healthEl);
    return;
  }

  const body = {
    version: $('#svc-form-version').value.trim(),
    owner: $('#svc-form-owner').value.trim(),
    description: $('#svc-form-desc').value.trim(),
    healthPath: health,
    api: apiPart,
  };
  if (repo) body.gitRepoUrl = repo;
  const tags = parseList($('#svc-form-tags').value);
  if (tags.length) body.tags = tags;

  await withBusy($('#svc-form-submit'), '提交中…', async () => {
    const res = await api(
      `/v1/namespaces/${encodeURIComponent(ns)}/services/${encodeURIComponent(name)}`,
      { method: 'PUT', body });
    if (!res.ok) {
      formFail(hint, '登记失败：' + errText(res.data));
      return;
    }
    const endpoints = (res.data.service.api.endpoints || []).length;
    setHint(hint, '');
    toast(`已${res.data.created ? '登记' : '更新'} ${ns}/${name}（${endpoints} 个端点）`);
    show(card, false);
    renderServices();
  });
}

// ---- 面板写入口：新增实例 / 声明式批量同步 ----
function currentService() {
  const sel = $('#inst-service').value;
  if (!sel) { toast('先在上面的下拉里选一个服务', true); return null; }
  const [ns, svc] = sel.split('/');
  return { ns, svc, base: `/v1/namespaces/${encodeURIComponent(ns)}/services/${encodeURIComponent(svc)}` };
}

function openInstanceForm() {
  const target = currentService();
  if (!target) return;
  show($('#svc-form-card'), false);
  show($('#inst-batch-card'), false);
  $('#inst-form-title').textContent = `新增实例 → ${target.ns}/${target.svc}`;
  $('#inst-form-scheme').value = 'http';
  ['host', 'port', 'meta'].forEach((k) => { $('#inst-form-' + k).value = ''; });
  setHint($('#inst-form-hint'), '');
  show($('#inst-form-card'), true);
}

async function submitInstanceForm() {
  const target = currentService();
  if (!target) return;
  const card = $('#inst-form-card');
  const hint = $('#inst-form-hint');
  clearFieldErrors(card);

  const hostEl = $('#inst-form-host');
  const portEl = $('#inst-form-port');
  const host = hostEl.value.trim();
  const port = Number(portEl.value);
  if (!host) { formFail(hint, 'host 必填（主机名或 IP，不要带 http:// 与路径）', hostEl); return; }
  if (/[:/]/.test(host.replace(/^\[.*\]$/, ''))) {
    formFail(hint, 'host 只能是主机名或 IP，不要带协议与路径：' + host, hostEl);
    return;
  }
  if (!port || port < 1 || port > 65535) { formFail(hint, 'port 必须在 1-65535 之间', portEl); return; }

  const body = { scheme: $('#inst-form-scheme').value, host, port };
  const meta = parseKV($('#inst-form-meta').value);
  if (Object.keys(meta).length) body.metadata = meta;

  await withBusy($('#inst-form-submit'), '提交中…', async () => {
    const res = await api(`${target.base}/instances`, { method: 'POST', body });
    if (!res.ok) { formFail(hint, '新增实例失败：' + errText(res.data)); return; }
    setHint(hint, '');
    toast(`已登记实例 ${body.scheme}://${host}:${port}`);
    show(card, false);
    renderInstances();
  });
}

function openBatchForm() {
  const target = currentService();
  if (!target) return;
  show($('#svc-form-card'), false);
  show($('#inst-form-card'), false);
  setHint($('#inst-batch-hint'), '');
  show($('#inst-batch-card'), true);
}

async function submitBatchForm() {
  const target = currentService();
  if (!target) return;
  const card = $('#inst-batch-card');
  const hint = $('#inst-batch-hint');
  clearFieldErrors(card);
  const jsonEl = $('#inst-batch-json');
  const raw = jsonEl.value.trim();
  if (!raw) {
    formFail(hint, '请贴实例数组，例如 [{"scheme":"http","host":"10.0.0.7","port":9099}]', jsonEl);
    return;
  }
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch (e) {
    formFail(hint, 'JSON 解析失败：' + e.message, jsonEl);
    return;
  }
  const items = Array.isArray(parsed) ? parsed : (parsed && parsed.instances);
  if (!Array.isArray(items)) {
    formFail(hint, '需要数组：直接贴 [ {...} ]，或用 {"instances":[...]} 包一层', jsonEl);
    return;
  }

  await withBusy($('#inst-batch-submit'), '同步中…', async () => {
    const res = await api(`${target.base}/instances`, { method: 'PUT', body: { instances: items } });
    if (!res.ok) { formFail(hint, '同步失败：' + errText(res.data)); return; }
    const d = res.data;
    setHint(hint, '');
    toast(`同步完成：新增 ${d.created}、更新 ${d.updated}、摘除 ${d.deleted}、未变 ${d.unchanged}`);
    show(card, false);
    renderInstances();
  });
}

$('#svc-refresh').addEventListener('click', renderServices);
$('#svc-filter').addEventListener('input', renderServices);
$('#svc-tag-filter').addEventListener('change', renderServices);
$('#api-search').addEventListener('click', renderSearch);
$('#api-path').addEventListener('keydown', (e) => { if (e.key === 'Enter') renderSearch(); });
$('#inst-refresh').addEventListener('click', renderInstances);
$('#inst-service').addEventListener('change', renderInstances);
$('#svc-new').addEventListener('click', () => openServiceForm(null));
$('#svc-form-cancel').addEventListener('click', () => show($('#svc-form-card'), false));
$('#svc-form-submit').addEventListener('click', submitServiceForm);
$('#svc-form-ep-add').addEventListener('click', () => $('#svc-form-eps tbody').appendChild(epRow(null)));
$('#svc-form-mode').addEventListener('change', (e) => setApiMode(e.target.value));
$('#svc-form-spec-template').addEventListener('click', () => prefillSpecTemplate(true));
$('#svc-form-name').addEventListener('input', () => prefillSpecTemplate(false));
$('#inst-new').addEventListener('click', openInstanceForm);
$('#inst-form-cancel').addEventListener('click', () => show($('#inst-form-card'), false));
$('#inst-form-submit').addEventListener('click', submitInstanceForm);
$('#inst-batch').addEventListener('click', openBatchForm);
$('#inst-batch-cancel').addEventListener('click', () => show($('#inst-batch-card'), false));
$('#inst-batch-submit').addEventListener('click', submitBatchForm);
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

