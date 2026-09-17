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
    case 'tree': return renderServiceTree();
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

  const org = m.organization || {};
  const cards = [
    ['命名空间', m.counts.namespaces],
    ['服务契约', m.counts.services],
    ['实例', m.counts.instances],
    ['API 端点', m.counts.endpoints],
    ['全局 revision', m.revision],
    ['变更记录', m.counts.changes],
    // 部门数据的来源（组织架构服务）——面板里的部门下拉就是它。
    ['部门（组织接口）', org.enabled ? '已配置' : '未配置'],
  ];
  const box = $('#overview-cards');
  box.innerHTML = cards.map(([label, value]) =>
    `<div class="card"><div class="card__label">${esc(label)}</div><div class="card__value">${esc(value)}</div></div>`).join('');
  const lastCard = box.lastElementChild; // 真实 DOM 里就是刚渲染的最后一张卡片
  if (org.enabled && lastCard) {
    lastCard.title = `部门目录来自组织接口：${org.url || ''}${org.departmentsPath || ''}`
      + `（本中心只读、缓存 ${org.cacheTtlSeconds}s，不维护部门数据）`;
  }
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
    `curl -sS '${base}/v1/services?department=D0001'`,
    `curl -sS '${base}/v1/namespaces/default/services/event-center/instances?pick=random'`,
    `curl -sS '${base}/v1/search/apis?method=GET&path=/v1/streams/abc/events'`,
    ``,
    `# 5) 部门目录：数据来自组织架构服务（organization），本中心只透出（短 TTL 缓存）`,
    `curl -sS ${base}/v1/departments`,
  ].join('\n');
}

// ---- 部门（数据来自组织架构服务，本中心只取回来透出） ----
// 服务契约的「部门」属性以**组织架构服务**（organization）为权威：
// 本中心不维护部门，只把组织接口的 `GET /api/v1/departments` 目录取回来，供
// ① 登记/编辑表单的下拉 ② 服务目录按部门过滤 ③ 卡片与检索结果展示 用。
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
// 刻意不抛异常：组织接口不可达时目录里会带上 error/available，面板照常可用。
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

// fillDeptSelects 刷新三处下拉（表单 / 服务目录过滤 / API 检索过滤），
// 并尽量保留用户当前的选择（选项没变时下拉内容不变）。
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

// renderDeptNote 把"部门目录的成色"写在表单里：
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

// ---- 服务目录 ----
function serviceMatches(svc, filter) {
  if (!filter) return true;
  const hay = [svc.namespace, svc.name, svc.description, svc.owner, svc.version, svc.gitRepoUrl,
    svc.departmentName, svc.departmentId]
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

  // 部门目录（组织接口）——失败不影响列表，只影响下拉里能选什么。
  await loadDepartments(false);
  fillDeptSelects(services);

  const tagSelect = $('#svc-tag-filter');
  const tags = [...new Set(services.flatMap((s) => s.tags || []))].sort();
  const filter = $('#svc-filter').value.trim();
  const tagFilter = tagSelect.value;
  const deptFilter = $('#svc-dept-filter').value;
  const shown = services.filter((s) =>
    serviceMatches(s, filter) && (!tagFilter || (s.tags || []).includes(tagFilter))
    && svcDeptMatches(s, deptFilter));

  // ① 数据与过滤条件都没变：直接返回，一行 DOM 都不动。
  //    （自动刷新每 3s 跑一次，早期这里是"重建整棵树"，于是展开的卡片被自动收起。）
  const sig = JSON.stringify({ filter, tagFilter, deptFilter, tags, services });
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
      ${deptTag(svc)}
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

// ---- 服务树（独立页签） ----
// 把「组织架构（部门）」与「服务契约」两张数据拼成一棵树：
//
//   部门 A（组织目录里的部门，0 个服务也列出来，好回答"这个组织里有什么"）
//     ├─ 子部门 A1（组织接口给了 parentId 时才有这一层）
//     │   └─ default/event-center  v0.1.0 · 1 实例 · 1 端点   ← 展开就是它的对外 API 端点表
//     └─ …
//   未归属部门（契约上没填 departmentId/departmentName 的服务，永远排最后）
//
// 三条「不能丢」的语义：
//   ① **默认收起**：部门多的时候先看骨架，点 ▸ 才展开（服务节点同理）；
//   ② 开合是**用户的状态**：自动刷新（3s）重建 DOM 后必须原样保留；数据没变时连 DOM 都不碰 ——
//      与「服务目录」卡片的展开是同一套做法（那里踩过"点开看两眼就被自动收起来"的坑）；
//   ③ 部门节点的来源是**并集**：组织目录里的部门（哪怕 0 个服务）∪ 契约上实际用到的部门
//      （组织接口不可达时按声明值保存的那些也要看得见）。
const treeExpanded = new Set();       // 用户点开的节点（自动刷新后要恢复的就是它）
const treeFilterExpanded = new Set(); // 过滤时为「命中项」临时展开的节点（清空过滤即失效）
let treeIndex = new Map();            // key → {key, node, kids, caret}（「全部展开/收起」用）
let treeForceExpand = false;          // 过滤中：新建的节点默认展开（否则命中项还埋在折叠里）
let lastTreeSig = null;

const TREE_UNASSIGNED = 'dept:__unassigned__'; // 「未归属部门」的哨兵 key

const treeDeptLabel = (g) => deptText({ id: g.id, name: g.name });
const treeDeptKey = (g) => 'dept:' + (g.id || g.name);
const treeSvcKey = (svc) => 'svc:' + svcKey(svc);
const treeDeptSort = (a, b) => treeDeptLabel(a).localeCompare(treeDeptLabel(b), 'zh');
const treeOpen = (key) => treeExpanded.has(key) || treeFilterExpanded.has(key);

// treeSvcHit / treeDeptHit：过滤框的匹配规则（大小写不敏感的子串，与「服务目录」一致）。
function treeSvcHit(svc, f) {
  if (!f) return true;
  return [svc.namespace, svc.name, svc.owner, svc.version, svc.description, svc.gitRepoUrl,
    svc.departmentId, svc.departmentName, (svc.tags || []).join(' ')]
    .join(' ').toLowerCase().includes(f);
}

function treeDeptHit(g, f) {
  if (!f) return true;
  return (treeDeptLabel(g) + ' ' + g.id + ' ' + g.name).toLowerCase().includes(f);
}

// treeGroups 把服务按归属部门分组，并把组织目录里"还没有服务"的部门也建成空组。
// 同一个部门在契约上可能只带 ID 或只带名字，所以用 ID 与名字两套索引互相兜底
// （只带名字、而组织目录里有它的服务，也会归到目录里那个部门下）。
function treeGroups(services, cat) {
  const groups = [];
  const byId = new Map();
  const byName = new Map();
  const add = (id, name, meta) => {
    id = String(id || '').trim();
    name = String(name || '').trim();
    const g = {
      id, name,
      type: String((meta && meta.type) || '').trim(),
      parentId: String((meta && meta.parentId) || '').trim(),
      declared: !!(meta && meta.declared), // 组织目录里"建过档"的部门（不是只被契约引用）
      services: [], children: [],
    };
    groups.push(g);
    if (id) byId.set(id.toLowerCase(), g);
    if (name) byName.set(name.toLowerCase(), g);
    return g;
  };
  (cat.departments || []).forEach((d) => add(d.id, d.name, { type: d.type, parentId: d.parentId, declared: true }));
  (services || []).forEach((svc) => {
    const id = String(svc.departmentId || '').trim();
    const name = String(svc.departmentName || '').trim();
    if (!id && !name) return; // 没有部门字段：另外归到「未归属部门」
    const g = (id && byId.get(id.toLowerCase())) || (name && byName.get(name.toLowerCase())) || add(id, name, null);
    g.services.push(svc);
  });
  return groups;
}

// treeRoots 把部门拼成层级：组织接口给了 parentId 就挂到父下面（父不存在/自己指向自己当根）。
// 互相成环的那部分会被"断链"提升为根 —— 宁可摆得不理想，也不能让一棵子树从界面上消失。
function treeRoots(groups) {
  const byId = new Map();
  groups.forEach((g) => { if (g.id) byId.set(g.id.toLowerCase(), g); });
  const roots = [];
  groups.forEach((g) => {
    const p = g.parentId ? byId.get(g.parentId.toLowerCase()) : null;
    if (p && p !== g) p.children.push(g); else roots.push(g);
  });
  const seen = new Set();
  const walk = (g) => { if (seen.has(g)) return; seen.add(g); g.children.forEach(walk); };
  roots.forEach(walk);
  groups.forEach((g) => {
    if (seen.has(g)) return;
    const p = g.parentId ? byId.get(g.parentId.toLowerCase()) : null;
    if (p && p !== g) p.children = p.children.filter((c) => c !== g); // 断环
    roots.push(g);
    walk(g);
  });
  return roots;
}

// applyTreeOpen 只改这一个节点的 DOM（caret 文案 + 子容器显隐），不整棵重建 ——
// 点开一个部门不该让其它节点重新渲染（也就不闪、不丢滚动位置）。
function applyTreeOpen(entry) {
  const open = treeOpen(entry.key);
  entry.caret.textContent = open ? '▾' : '▸';
  entry.kids.hidden = !open;
  entry.row.setAttribute('aria-expanded', open ? 'true' : 'false');
  if (open) entry.node.classList.add('tree__node--open');
  else entry.node.classList.remove('tree__node--open');
}

// treeNode 造一个节点：一行（可点）+ 子容器（默认收起），并登记进 treeIndex。
function treeNode(key, label, badges, children, cls) {
  const node = el('div', 'tree__node' + (cls ? ' ' + cls : ''));
  node.dataset.key = key;
  const row = el('div', 'tree__row');
  row.dataset.key = key;
  row.tabIndex = 0;
  row.setAttribute('role', 'button');
  row.appendChild(el('span', 'tree__caret', '▸'));
  row.appendChild(el('span', 'tree__label', label));
  const extra = el('span', 'tree__extra');
  extra.innerHTML = badges || '';
  row.appendChild(extra);
  node.appendChild(row);

  const kids = el('div', 'tree__children');
  (children || []).forEach((c) => kids.appendChild(c));
  node.appendChild(kids);

  const entry = { key, node, row, kids, caret: row.children[0] };
  treeIndex.set(key, entry);
  if (treeForceExpand) treeFilterExpanded.add(key); // 过滤时命中的节点自动展开

  const flip = (ev) => {
    // 行里也可能有链接（代码仓库 / API 文档）：点链接就只走链接，别顺手把节点开合掉。
    if (ev && ev.target && String(ev.target.tagName || '').toLowerCase() === 'a') return;
    const open = !treeOpen(key);
    if (open) treeExpanded.add(key);
    else { treeExpanded.delete(key); treeFilterExpanded.delete(key); }
    applyTreeOpen(entry);
  };
  row.addEventListener('click', flip);
  row.addEventListener('keydown', (ev) => { if (ev && (ev.key === 'Enter' || ev.key === ' ')) flip(); });

  applyTreeOpen(entry);
  return node;
}

// treeSvcNode 是叶子：服务。展开就是它的对外 API 端点表 + 仓库/spec 链接
// （端点早就随列表返回了，这里不再多打一次请求）。
function treeSvcNode(svc) {
  const api = svc.api || {};
  const endpoints = api.endpoints || [];
  const badges = [
    svc.version ? `<span class="tag">v${esc(svc.version)}</span>` : '',
    `<span class="badge ${svc.instanceCount > 0 ? 'badge--ok' : 'badge--warn'}">${esc(svc.instanceCount)} 实例</span>`,
    `<span class="badge badge--muted">${esc(endpoints.length)} 端点</span>`,
    svc.owner ? `<span class="tag">owner:${esc(svc.owner)}</span>` : '',
    svc.gitRepoUrl ? repoTag(svc.gitRepoUrl) : '',
    svc.healthPath ? `<span class="tag">health:${esc(svc.healthPath)}</span>` : '',
    `<span class="item__meta">revision ${esc(svc.revision || 0)}</span>`,
  ].join('');
  return treeNode(treeSvcKey(svc), svcKey(svc), badges,
    [endpointsTable(endpoints, api.docsUrl, api.specUrl, svc)], 'tree__node--svc');
}

// treeDeptNode 渲染一个部门节点（子部门 + 自己名下的服务）。
// 过滤时：部门名命中 → 它名下的服务全显示；没命中 → 只显示命中的服务；
// 自己、服务、子部门都没命中 → 整支不显示（返回 null）。
function treeDeptNode(g, f) {
  const kids = [];
  g.children.slice().sort(treeDeptSort).forEach((c) => {
    const node = treeDeptNode(c, f);
    if (node) kids.push(node);
  });
  const hit = treeDeptHit(g, f);
  const services = (hit ? g.services : g.services.filter((s) => treeSvcHit(s, f)))
    .slice().sort((a, b) => svcKey(a).localeCompare(svcKey(b), 'zh'));
  if (f && !hit && !services.length && !kids.length) return null;

  services.forEach((svc) => kids.push(treeSvcNode(svc)));
  if (!services.length && !kids.length) kids.push(el('div', 'tree__empty', '该部门下暂无登记的服务'));

  const n = g.services.length;
  const badges = [
    n ? `<span class="badge badge--ok">${n} 个服务</span>` : '<span class="badge badge--muted">0 个服务</span>',
    f && !hit ? `<span class="tag">命中 ${services.length}/${n}</span>` : '',
    g.type ? `<span class="tag">${esc(g.type)}</span>` : '',
    (!g.declared && deptCatalog.enabled && deptCatalog.available)
      ? '<span class="tag" title="组织接口的部门目录里没有它：这份归属来自契约上的声明值">仅契约声明</span>' : '',
  ].join('');
  return treeNode(treeDeptKey(g), treeDeptLabel(g), badges, kids, 'tree__node--dept');
}

// treeUnassignedNode 是「未归属部门」：契约上没填部门字段的服务。
// 它不在组织架构里，单独一组、永远排最后，免得这些服务在树上"消失"。
function treeUnassignedNode(services) {
  const kids = services.slice().sort((a, b) => svcKey(a).localeCompare(svcKey(b), 'zh')).map(treeSvcNode);
  const badges = [
    `<span class="badge badge--warn">${services.length} 个服务</span>`,
    '<span class="tag" title="登记/编辑契约时补上归属部门，它们就会自动归到对应部门下">未填归属部门</span>',
  ].join('');
  return treeNode(TREE_UNASSIGNED, '未归属部门', badges, kids, 'tree__node--dept tree__node--unassigned');
}

// renderTreeNote 写清楚"这棵树的数据成色与规模"：
// 组织接口可用时列出的部门是组织架构的真实全量（0 个服务的也在），不可用时只有契约上出现过的部门。
function renderTreeNote(services) {
  const node = $('#tree-note');
  if (!node) return;
  const c = deptCatalog;
  const depts = (c.departments || []).length;
  const unassigned = services.filter((s) =>
    !String(s.departmentId || '').trim() && !String(s.departmentName || '').trim()).length;
  const counts = `${services.length} 个服务 / ${depts} 个部门`
    + (unassigned ? `（另有 ${unassigned} 个未填归属部门）` : '');
  let src, warn = false;
  if (!c.enabled) {
    src = '组织接口未配置（REGISTRY_ORG_URL）：只能列出契约上出现过的部门，"还没有服务的部门"列不出来';
    warn = true;
  } else if (c.available) {
    src = `部门目录来自组织接口${c.url ? ' ' + c.url : ''}${c.cached ? '（TTL 缓存）' : ''}`
      + (c.fetchedAt ? ' · ' + fmtTime(c.fetchedAt) : '');
  } else {
    src = `组织接口暂时不可达（${c.error || '未知原因'}）：`
      + (depts ? `用的是上次同步的 ${depts} 个部门` : '只能列出契约上出现过的部门');
    warn = true;
  }
  node.className = 'note grow' + (warn ? ' note--warn' : '');
  node.textContent = counts + ' · ' + src;
}

// renderTreeBody 重建整棵树（只有数据/过滤条件真的变了才会走到这里）。
function renderTreeBody(services, filter) {
  treeIndex = new Map();
  treeFilterExpanded.clear();
  treeForceExpand = !!filter;

  const box = $('#tree-root');
  box.innerHTML = '';
  const nodes = [];
  treeRoots(treeGroups(services, deptCatalog)).slice().sort(treeDeptSort).forEach((g) => {
    const node = treeDeptNode(g, filter);
    if (node) nodes.push(node);
  });
  const unassigned = services
    .filter((s) => !String(s.departmentId || '').trim() && !String(s.departmentName || '').trim())
    .filter((s) => treeSvcHit(s, filter));
  if (unassigned.length) nodes.push(treeUnassignedNode(unassigned));

  if (!nodes.length) {
    box.appendChild(el('div', 'tree__empty',
      filter ? '没有匹配的部门或服务。' : '还没有任何部门与服务：先登记服务契约，或在组织架构服务里建部门。'));
    return;
  }
  nodes.forEach((n) => box.appendChild(n));
}

async function renderServiceTree() {
  const res = await api('/v1/services');
  if (!res.ok) { toast('读取服务列表失败：' + JSON.stringify(res.data), true); return; }
  const services = res.data.services || [];
  await loadDepartments(false);
  const filter = $('#tree-filter').value.trim().toLowerCase();
  renderTreeNote(services); // 成色/规模：一行文字，每次刷新都更新（不会闪）

  // 数据（含部门目录）+ 过滤条件都没变 → 一行 DOM 都不动，展开状态自然原样保留。
  // 注意签名只用**稳定字段**：deptCatalog 里的 cached/fetchedAt 每次刷新都在变，
  // 让它们参与签名就等于每 3s 重建一次整棵树（展开状态会丢、还会闪）。
  const sig = JSON.stringify({
    filter, services,
    cat: [deptCatalog.enabled, deptCatalog.available, deptCatalog.stale, deptCatalog.departments || []],
  });
  if (sig === lastTreeSig) return;
  lastTreeSig = sig;
  renderTreeBody(services, filter);
}

// ---- API 检索 ----
async function renderSearch() {
  const method = $('#api-method').value;
  const path = $('#api-path').value.trim();
  const match = $('#api-match').value;
  // 部门过滤的候选也来自组织接口；首次进这个页签时补一次目录。
  if (!deptCatalog.loaded) { await loadDepartments(false); fillDeptSelects(lastServices); }
  const dept = $('#api-dept').value;
  const qs = new URLSearchParams();
  if (method) qs.set('method', method);
  if (path) qs.set('path', path);
  if (match) qs.set('match', match);
  const deptSel = deptByKey(dept);
  if (deptSel && deptSel.id) qs.set('department', deptSel.id);
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
  // 部门列只在真有数据时出现（绝大多数场景下没人给服务标部门，不必留一列空格子）。
  const anyDept = matches.some((m) => m.departmentId || m.departmentName);
  const rows = matches.map((m) => `
    <tr>
      <td>${methodBadge(m.endpoint.method)}</td>
      <td class="mono">${esc(m.endpoint.path)}</td>
      <td class="mono">${esc(m.namespace)}/${esc(m.service)}</td>
      <td>${esc(m.serviceVersion || '')}</td>
      ${anyDept ? `<td>${esc(deptText({ id: m.departmentId, name: m.departmentName }) || '—')}</td>` : ''}
      <td>${esc(m.instanceCount)}</td>
      <td><span class="badge badge--muted">${esc(m.matchType)}</span></td>
      <td>${esc(m.endpoint.summary || '')}</td>
    </tr>`).join('');
  const wrap = el('div', 'item');
  wrap.innerHTML = `<table>
    <thead><tr><th>方法</th><th>登记的路径</th><th>服务</th><th>版本</th>${anyDept ? '<th>部门</th>' : ''}<th>实例</th><th>命中方式</th><th>说明</th></tr></thead>
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
  // 部门下拉：候选来自组织接口（上次拉到的目录即可，打开表单不额外出网）。
  if (!deptCatalog.loaded) { await loadDepartments(false); }
  fillDeptSelects(lastServices);
  const f = {
    ns: $('#svc-form-ns'), name: $('#svc-form-name'), version: $('#svc-form-version'),
    owner: $('#svc-form-owner'), health: $('#svc-form-health'), desc: $('#svc-form-desc'),
    tags: $('#svc-form-tags'), docs: $('#svc-form-docs'), repo: $('#svc-form-repo'),
    specurl: $('#svc-form-specurl'), spec: $('#svc-form-spec'), dept: $('#svc-form-dept'),
  };
  renderDeptNote();
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
    f.dept.value = svcDeptKey(svc);
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
    f.dept.value = '';
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
  // 归属部门：只发下拉选中的那个（选中项就是权威值 —— 组织接口给的 ID/名称）。
  // 不选 = 不带该字段，PUT 是整份覆盖，等于把部门清掉（与 gitRepoUrl 的语义一致）。
  const deptSel = deptByKey($('#svc-form-dept').value);
  if (deptSel && deptSel.id) body.departmentId = deptSel.id;
  if (deptSel && deptSel.name) body.departmentName = deptSel.name;
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
    // 部门对齐的结果（"已按组织接口对齐" / "组织接口不可达，按声明值保存"）必须可见，
    // 否则用户填了部门却不知道到底有没有对上。
    if (res.data.departmentNote) toast(res.data.departmentNote);
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
$('#tree-refresh').addEventListener('click', renderServiceTree);
$('#tree-filter').addEventListener('input', renderServiceTree);
// 「全部展开 / 全部收起」：一次调整整棵树（过滤后只剩一部分节点时，也只动看得见的那些）。
// 展开是记进 treeExpanded 的，所以自动刷新重建 DOM 后依然是展开的。
$('#tree-expand-all').addEventListener('click', () => {
  treeIndex.forEach((entry) => { treeExpanded.add(entry.key); applyTreeOpen(entry); });
});
$('#tree-collapse-all').addEventListener('click', () => {
  treeExpanded.clear();
  treeFilterExpanded.clear();
  treeIndex.forEach((entry) => applyTreeOpen(entry));
});
$('#svc-filter').addEventListener('input', renderServices);
$('#svc-tag-filter').addEventListener('change', renderServices);
$('#svc-dept-filter').addEventListener('change', renderServices);
$('#api-search').addEventListener('click', renderSearch);
$('#api-path').addEventListener('keydown', (e) => { if (e.key === 'Enter') renderSearch(); });
$('#api-dept').addEventListener('change', renderSearch);
$('#inst-refresh').addEventListener('click', renderInstances);
$('#inst-service').addEventListener('change', renderInstances);
$('#svc-new').addEventListener('click', () => openServiceForm(null));
$('#svc-form-cancel').addEventListener('click', () => show($('#svc-form-card'), false));
$('#svc-form-submit').addEventListener('click', submitServiceForm);
$('#svc-form-ep-add').addEventListener('click', () => $('#svc-form-eps tbody').appendChild(epRow(null)));
$('#svc-form-mode').addEventListener('change', (e) => setApiMode(e.target.value));
$('#svc-form-spec-template').addEventListener('click', () => prefillSpecTemplate(true));
$('#svc-form-name').addEventListener('input', () => prefillSpecTemplate(false));
// 「重新同步部门」：强制跳过服务端 TTL 缓存重取组织接口的目录（组织服务刚建了新部门时用）。
$('#svc-form-dept-refresh').addEventListener('click', async (e) => {
  const btn = e.target;
  await withBusy(btn, '同步中…', async () => {
    const cat = await loadDepartments(true);
    fillDeptSelects(lastServices);
    renderDeptNote();
    if (cat.enabled && cat.available) toast(`已从组织接口同步 ${(cat.departments || []).length} 个部门`);
    else toast('部门目录暂时取不到：' + (cat.error || '组织接口不可达'), true);
  });
});
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

