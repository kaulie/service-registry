'use strict';

// ---- 契约编辑页（独立页面）----
//
//   /panel/contract.html?ns=<ns>&name=<服务名>   → 编辑已有契约
//   /panel/contract.html                         → 登记新契约
//
// 为什么是独立页面而不是列表里的内联表单：内联表单挤在服务卡片上方，列表一长就"点了不知道
// 弹出在哪"，而且 OpenAPI 原文要一大片地方。独立页面自带 URL（可收藏/可分享/可后退），
// 在面板里点「编辑契约」会在新页签打开，面板本身的滚动位置与展开状态都不受影响。
//
// 与面板共用 shared.js：令牌（localStorage，同源共享）、部门目录、表单反馈一套做法。
// 保存成功后回面板即可 —— 面板每 3s 自动刷新，改动会自动出现。

const params = new URLSearchParams(location.search);
const TARGET = {
  ns: (params.get('ns') || '').trim(),
  name: (params.get('name') || '').trim(),
};
const isEdit = Boolean(TARGET.name);

const field = (id) => $('#svc-form-' + id);
// 契约上实际用到的部门（来自 /v1/services）：与组织目录一起构成部门下拉的候选，
// 否则编辑一个"组织接口里已下线、但契约上还写着"的部门时，下拉里没有它 → 一提交就被清掉。
let knownServices = [];

// fillNamespaceSelect 拉命名空间填下拉（编辑时优先选中服务所属的那个）。
async function fillNamespaceSelect(prefer) {
  const sel = field('ns');
  const res = await api('/v1/namespaces');
  const list = (res.ok && res.data.namespaces) || [];
  sel.innerHTML = list.map((n) => `<option value="${esc(n.name)}">${esc(n.name)}</option>`).join('');
  if (prefer && list.some((n) => n.name === prefer)) sel.value = prefer;
  if (!list.length) {
    formFail($('#svc-form-hint'),
      '一个命名空间都没有：先去面板「命名空间」页签建一个，再回来登记契约。', sel);
  }
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
  const spec = field('spec');
  const tpl = specTemplate(field('name').value.trim());
  if (force || !spec.value.trim() || spec.value === lastSpecTemplate) {
    spec.value = tpl;
  }
  lastSpecTemplate = tpl;
}

function setPageTitle(text) {
  $('#svc-form-title').textContent = text;
  document.title = text + ' · Service Registry';
}

// 新登记：清空表单并预填最小模板（不粘贴 spec 也能直接提交成功）。
function resetForNew() {
  setPageTitle('登记服务契约');
  field('name').disabled = false;
  ['name', 'version', 'owner', 'health', 'desc', 'tags', 'docs', 'repo', 'specurl', 'spec']
    .forEach((k) => { field(k).value = ''; });
  field('dept').value = '';
  setEpRows(null);
  setApiMode('spec');
  prefillSpecTemplate(true);
  show($('#contract-done'), false);
}

// 编辑：把服务契约回填进表单（服务名是身份，不可改）。
async function loadForEdit() {
  setPageTitle(`编辑服务契约 ${TARGET.ns}/${TARGET.name}`);
  field('name').disabled = true;
  field('ns').value = TARGET.ns;
  field('name').value = TARGET.name;
  const res = await api(`/v1/namespaces/${encodeURIComponent(TARGET.ns)}/services/${encodeURIComponent(TARGET.name)}`);
  if (!res.ok) {
    // 可能是被删了 / 名字打错：保持页面可用 —— 解锁服务名，用户可以就地改成"登记新契约"。
    formFail($('#svc-form-hint'),
      `读不到契约 ${TARGET.ns}/${TARGET.name}：${errText(res.data)}（可能刚被删除。要新建就把服务名改成别的，再提交）`,
      field('name'));
    field('name').disabled = false;
    setPageTitle(`登记服务契约（没找到 ${TARGET.ns}/${TARGET.name}）`);
    return null;
  }
  const svc = res.data.service || {};
  field('version').value = svc.version || '';
  field('owner').value = svc.owner || '';
  field('health').value = svc.healthPath || '';
  field('desc').value = svc.description || '';
  field('repo').value = svc.gitRepoUrl || '';
  field('dept').value = svcDeptKey(svc);
  field('tags').value = (svc.tags || []).join(',');
  field('docs').value = (svc.api && svc.api.docsUrl) || '';
  field('specurl').value = (svc.api && svc.api.specUrl) || '';
  field('spec').value = '';
  if (svc.api && svc.api.hasSpec) {
    const raw = await fetchText(`/v1/namespaces/${encodeURIComponent(TARGET.ns)}`
      + `/services/${encodeURIComponent(TARGET.name)}/spec`);
    field('spec').value = raw;
    setApiMode('spec');
  } else {
    setEpRows(svc.api ? svc.api.endpoints : []);
    setApiMode('manual');
  }
  const badge = $('#contract-exists');
  if (badge) {
    badge.hidden = false;
    badge.textContent = `revision ${svc.revision || 0} · 更新 ${fmtTime(svc.updatedAt)}`;
  }
  return svc;
}

async function initContractPage() {
  mountThemeToggle('#theme-toggle');
  mountTokenInput('#token');
  const modeBadge = $('#page-mode');
  if (modeBadge) modeBadge.textContent = isEdit ? '编辑契约' : '登记契约';
  const urlNode = $('#contract-url');
  if (urlNode) urlNode.textContent = location.pathname + location.search;
  await fillNamespaceSelect(isEdit ? TARGET.ns : undefined);
  // 部门候选：组织目录 ∪ 契约上实际用到的部门（编辑时不能把已有部门弄丢）。
  if (!deptCatalog.loaded) await loadDepartments(false);
  const listRes = await api('/v1/services');
  knownServices = (listRes.ok && listRes.data.services) || [];
  fillDeptSelects(knownServices);
  setHint($('#svc-form-hint'), '');
  if (isEdit) await loadForEdit();
  else resetForNew();
  renderDeptNote();
  show($('#contract-loading'), false);
  show($('#contract-body'), true);
}

// 提交：与面板时代完全同一套校验与反馈（失败一律 formFail：提示 + toast + 标红字段）。
async function submitContract() {
  const hint = $('#svc-form-hint');
  clearFieldErrors($('#contract-body'));

  const nsEl = field('ns');
  const nameEl = field('name');
  const specEl = field('spec');
  const ns = nsEl.value;
  const name = nameEl.value.trim();

  if (!ns) {
    formFail(hint, '请选择命名空间（下拉为空说明还没有命名空间，先去面板「命名空间」页签新建一个）', nsEl);
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
  const docs = field('docs').value.trim();
  const specurl = field('specurl').value.trim();
  if (docs) apiPart.docsUrl = docs;
  if (specurl) apiPart.specUrl = specurl;

  // 代码仓库地址：本地先拦一道，错误直接指到字段（服务端也会校验，规则一致）。
  const repoEl = field('repo');
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

  const healthEl = field('health');
  const health = healthEl.value.trim();
  if (health && !health.startsWith('/')) {
    formFail(hint, 'healthPath 必须以 / 开头（它是元信息，供消费方/看门狗自行探活）：' + health, healthEl);
    return;
  }

  const body = {
    version: field('version').value.trim(),
    owner: field('owner').value.trim(),
    description: field('desc').value.trim(),
    healthPath: health,
    api: apiPart,
  };
  if (repo) body.gitRepoUrl = repo;
  // 归属部门：只发下拉选中的那个（选中项就是权威值 —— 组织接口给的 ID/名称）。
  // 不选 = 不带该字段，PUT 是整份覆盖，等于把部门清掉（与 gitRepoUrl 的语义一致）。
  const deptSel = deptByKey(field('dept').value);
  if (deptSel && deptSel.id) body.departmentId = deptSel.id;
  if (deptSel && deptSel.name) body.departmentName = deptSel.name;
  const tags = parseList(field('tags').value);
  if (tags.length) body.tags = tags;

  await withBusy($('#svc-form-submit'), '提交中…', async () => {
    const res = await api(
      `/v1/namespaces/${encodeURIComponent(ns)}/services/${encodeURIComponent(name)}`,
      { method: 'PUT', body });
    if (!res.ok) {
      formFail(hint, '登记失败：' + errText(res.data));
      return;
    }
    const svcOut = res.data.service || {};
    const endpoints = ((svcOut.api || {}).endpoints || []).length;
    formOK(hint, `已${res.data.created ? '登记' : '更新'} ${ns}/${name}（${endpoints} 个端点）`);
    toast(`已${res.data.created ? '登记' : '更新'} ${ns}/${name}（${endpoints} 个端点）`);
    // 部门对齐的结果（"已按组织接口对齐" / "组织接口不可达，按声明值保存"）必须可见，
    // 否则用户填了部门却不知道到底有没有对上。
    if (res.data.departmentNote) toast(res.data.departmentNote);
    afterSaved(ns, name, res.data.created, Boolean((svcOut.api || {}).hasSpec));
  });
}

// afterSaved 在页面里给出"接下来能干什么"：回面板 / 看内联 spec / 接着登记下一个。
// 面板每 3s 自动刷新，所以回面板时列表已经是新的了。
function afterSaved(ns, name, created, hasSpec) {
  const done = $('#contract-done');
  if (done) {
    done.hidden = false;
    $('#contract-done-text').textContent = `已${created ? '登记' : '更新'} ${ns}/${name}。`
      + '面板每 3 秒自动刷新，切回去就能看到最新契约。';
    const specLink = $('#contract-done-spec');
    if (specLink) {
      // 没登记内联 spec（只有手工声明的端点）时不给这个链接 —— 否则点开是 404
      show(specLink, hasSpec);
      specLink.href = `/v1/namespaces/${encodeURIComponent(ns)}/services/${encodeURIComponent(name)}/spec`;
    }
    // 新建成功后这个页面就变成"编辑 X/Y"了：刷新不会再误建一个新的。
    try {
      history.replaceState(null, '', `contract.html?ns=${encodeURIComponent(ns)}&name=${encodeURIComponent(name)}`);
    } catch (_) { /* 无 history 的环境（测试/沙箱）：只影响地址栏，不影响功能 */ }
    setPageTitle(`编辑服务契约 ${ns}/${name}`);
    field('name').disabled = true;
    if (done.scrollIntoView) done.scrollIntoView({ block: 'nearest' });
  }
}

$('#svc-form-submit').addEventListener('click', submitContract);
$('#svc-form-cancel').addEventListener('click', () => { location.href = './#services'; });
$('#svc-form-ep-add').addEventListener('click', () => $('#svc-form-eps tbody').appendChild(epRow(null)));
$('#svc-form-mode').addEventListener('change', (e) => setApiMode(e.target.value));
$('#svc-form-spec-template').addEventListener('click', () => prefillSpecTemplate(true));
$('#svc-form-name').addEventListener('input', () => prefillSpecTemplate(false));
// 「重新同步部门」：强制跳过服务端 TTL 缓存重取组织接口的目录（组织服务刚建了新部门时用）。
$('#svc-form-dept-refresh').addEventListener('click', async (e) => {
  const btn = e.target;
  await withBusy(btn, '同步中…', async () => {
    const cat = await loadDepartments(true);
    fillDeptSelects(knownServices);
    renderDeptNote();
    if (cat.enabled && cat.available) toast(`已从组织接口同步 ${(cat.departments || []).length} 个部门`);
    else toast('部门目录暂时取不到：' + (cat.error || '组织接口不可达'), true);
  });
});

initContractPage();

