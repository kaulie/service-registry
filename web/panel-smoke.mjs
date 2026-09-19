#!/usr/bin/env node
// 面板冒烟测试：在受控 DOM 里加载 web/app.js，模拟"填写表单 → 点提交"，
// 断言**任何失败都不能静默**（必须有可见提示/字段标红/toast），成功则关闭表单。
//
// 为什么需要它：面板是纯前端，Go 的 httptest 覆盖不到它的交互逻辑。
// 曾经的线上症状就是第 2 个场景：本地校验失败只写了一个 12px 灰字，
// 用户看到的是"点了提交没反应，也不提示哪里错了"。
//
// 运行：node web/panel-smoke.mjs（或 make panel-smoke；无 node 环境会跳过）

import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const readScript = (name) => fs.readFileSync(path.join(here, name), 'utf8');
// 面板（index.html）与契约编辑页（contract.html）各自加载 shared.js + 自己的脚本。
const PANEL_SCRIPTS = ['shared.js', 'app.js'];
const CONTRACT_SCRIPTS = ['shared.js', 'contract.js'];

const failures = [];
function check(cond, what) {
  if (cond) {
    console.log('   ✓ ' + what);
  } else {
    console.log('   ✗ ' + what);
    failures.push(what);
  }
}

// ---- 受控 DOM ----
function buildDom(scenario = {}, scripts = PANEL_SCRIPTS) {
  const handlers = new Map();
  const els = new Map();
  const requests = [];
  const windowEvents = [];

  function makeEl(key) {
    const el = {
      _key: key,
      _errors: [],
      value: '', textContent: '', className: '', title: '',
      hidden: false, disabled: false, scrollTop: 0, scrollHeight: 0, open: false,
      dataset: {}, style: {}, children: [],
      classList: {
        add(c) { if (c === 'field-error') el._errors.push(c); },
        remove() {}, contains: () => false, toggle() {},
      },
      addEventListener(ev, fn) {
        const k = key + '|' + ev;
        if (!handlers.has(k)) handlers.set(k, []);
        handlers.get(k).push(fn);
        // 也按元素记一份：服务树的行是 createElement 造出来的（所有 <div> 共用一个 key），
        // 测试要能"只点某一行"，就得能从元素本身拿到它的处理器。
        if (!el._handlers) el._handlers = {};
        if (!el._handlers[ev]) el._handlers[ev] = [];
        el._handlers[ev].push(fn);
      },
      appendChild(c) { el.children.push(c); return c; },
      remove() {},
      querySelector: (s) => makeEl(key + ' ' + s),
      querySelectorAll: () => [],
      focus() {}, scrollIntoView() {}, setAttribute() {}, getAttribute() { return null; },
    };
    // 真实 DOM 里 innerHTML = '' 会移除所有子节点（列表重建就是靠它），这里保持一致，
    // 否则"重建列表"的断言会看到一堆本该被移除的旧节点。
    let html = '';
    Object.defineProperty(el, 'innerHTML', {
      get: () => html,
      set: (v) => { html = v; el.children.length = 0; },
    });
    return el;
  }
  const q = (sel) => {
    if (!els.has(sel)) els.set(sel, makeEl(sel));
    return els.get(sel);
  };

  const getResponses = {
    '/v1/namespaces': { body: { namespaces: [{ name: 'default', serviceCount: 0, instanceCount: 0 }], total: 1 } },
    '/health': { body: { status: 'ok', version: 'test' } },
    '/v1/meta': { body: { version: 'test', uptimeSeconds: 1, revision: 0, writeAuth: 'open', counts: { namespaces: 1, services: 0, instances: 0, endpoints: 0, changes: 0 } } },
    '/v1/services': { body: { services: scenario.services || [], total: (scenario.services || []).length } },
    '/v1/departments': { body: scenario.deptCatalog || ORG_CATALOG },
  };

  const sandbox = {
    console,
    document: {
      querySelector: q,
      querySelectorAll: () => [],
      createElement: (tag) => makeEl('<' + tag + '>'),
      addEventListener() {},
      // 主题开关会读写 <html data-theme>（真实 DOM 里就是 documentElement）
      documentElement: { setAttribute() {}, getAttribute: () => 'dark' },
    },
    localStorage: { getItem: () => '', setItem() {}, removeItem() {} },
    location: {
      origin: 'http://127.0.0.1:4240',
      pathname: '/panel/contract.html',
      search: scenario.search || '',
      hash: scenario.hash || '',
    },
    URLSearchParams,
    setInterval: () => 1,
    clearInterval() {},
    setTimeout: (fn, d) => setTimeout(fn, d),
    clearTimeout: (id) => clearTimeout(id),
    EventSource: function () { this.addEventListener = () => {}; this.close = () => {}; },
    confirm: () => true,
    alert() {},
    addEventListener(ev, fn) { windowEvents.push({ ev, fn }); },
    // 内联 spec 原文（编辑时取回），由下面的 fetch mock 按路径动态返回
    fetch: (p, init) => {
      const method = (init && init.method) || 'GET';
      requests.push({ method, path: String(p), body: init && init.body });
      const base = String(p).split('?')[0];
      const okText = (text) => Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(text) });
      const okJSON = (obj) => okText(JSON.stringify(obj));
      const get = getResponses[base];
      if (method === 'GET' && get) {
        return okText(get.raw !== undefined ? get.raw : JSON.stringify(get.body));
      }
      // 契约编辑页：读单个契约（编辑模式回填）
      const hit = base.match(/^\/v1\/namespaces\/([^/]+)\/services\/([^/]+)(\/spec)?$/);
      if (method === 'GET' && hit) {
        if (hit[3]) return okText(scenario.spec !== undefined ? scenario.spec : 'openapi: 3.0.3\n');
        const svc = scenario.service
          || (scenario.services || []).find((s) => s.name === decodeURIComponent(hit[2]))
          || EVENT_CENTER;
        return okJSON({ service: svc });
      }
      if (scenario.serverError) {
        const body = { error: { code: 'invalid_request', message: '服务端说：host 不合法' } };
        return Promise.resolve({ ok: false, status: 400, text: () => Promise.resolve(JSON.stringify(body)) });
      }
      const body = { created: true, service: { api: { hasSpec: true, endpoints: [{ method: 'GET', path: '/health' }] } } };
      return Promise.resolve({ ok: true, status: 201, text: () => Promise.resolve(JSON.stringify(body)) });
    },
    JSON, Date, Math, Number, String, Object, Array, Promise, Error,
    encodeURIComponent, isNaN, parseInt, parseFloat,
  };
  sandbox.window = sandbox;
  sandbox.globalThis = sandbox;

  vm.createContext(sandbox);
  for (const name of scripts) {
    vm.runInContext(readScript(name), sandbox, { filename: name });
  }
  return {
    sandbox, q, handlers, requests, windowEvents,
    // 模拟"别的平台登记/下线了服务"：改完之后面板读到的是新数据。
    setServices(list) { getResponses['/v1/services'].body = { services: list, total: list.length }; },
  };
}

// 收集节点树里所有 <details>（服务卡片的「展开」）。
function collectDetails(node, out = []) {
  if (node._key === '<details>') out.push(node);
  (node.children || []).forEach((c) => collectDetails(c, out));
  return out;
}

// 服务卡片是 class="item" 的 div：头部/标签挂在它的 innerHTML 里。
function collectCards(node, out = []) {
  if (node.className === 'item') out.push(node);
  (node.children || []).forEach((c) => collectCards(c, out));
  return out;
}

// 按 createElement 的标签收集节点（卡片上的按钮/链接是造出来的，不在 innerHTML 里）。
function collectByKey(node, key, out = []) {
  if (node._key === key) out.push(node);
  (node.children || []).forEach((c) => collectByKey(c, key, out));
  return out;
}

// 面板上的入口必须是"独立页面"的链接 —— 本轮需求的固化点。
async function scenarioPanelLinksToContractPage() {
  console.log('\n场景：面板里的「编辑契约」指向独立页面');
  const dom = buildDom({ services: [EVENT_CENTER] });
  await click(dom, '#svc-refresh');
  const card = collectCards(dom.q('#svc-list'))[0];
  const links = collectByKey(card, '<a>');
  const edit = links.find((a) => String(a.textContent).includes('编辑契约'));
  check(!!edit, '服务卡片上有「编辑契约」入口');
  check(!!edit && edit.href === 'contract.html?ns=default&name=event-center',
    '它是链接，指向独立页面（带 ns/name，可收藏可分享）：' + (edit && edit.href));
  check(!!edit && edit.target === '_blank', '在新页签打开（面板的展开状态与滚动位置不受影响）');
  check(dom.handlers.get('#svc-form-submit|click') === undefined,
    '面板里没有内联的契约表单提交（已挪到 contract.html）');
}

// ---- 服务树的断言辅助：树节点是 createElement 造出来的，不走 q(sel) ----
// 直接派发某个元素上的事件（模拟"只点这一行"）。
async function fireOn(el, ev, extra = {}) {
  const fns = (el._handlers && el._handlers[ev]) || [];
  for (const fn of fns) {
    const r = fn(Object.assign({ target: el }, extra));
    if (r && typeof r.then === 'function') await r;
  }
  return fns.length;
}

function isTreeNode(n) {
  return String(n.className || '').split(' ').includes('tree__node');
}

// 树上的所有节点（含服务叶子；先序）。
function collectTreeNodes(node, out = []) {
  if (isTreeNode(node)) out.push(node);
  (node.children || []).forEach((c) => collectTreeNodes(c, out));
  return out;
}

// 顶级节点 = 最外层的几棵子树（部门 / 未归属部门）。
const treeTopNodes = (root) => (root.children || []).filter(isTreeNode);
const treeRow = (n) => n.children[0];    // 第一行（可点，写着一行文字）
const treeKids = (n) => n.children[1];   // 第二行是子容器（收起时 hidden）
const treeLabel = (n) => treeRow(n).children[1].textContent;
const treeCaret = (n) => treeRow(n).children[0].textContent;
const treeBadges = (n) => treeRow(n).children[2].innerHTML;
const treeIsOpen = (n) => treeKids(n).hidden === false;
const treeFind = (node, label) => collectTreeNodes(node).find((n) => treeLabel(n) === label);

// 模拟用户点「展开」：<details> 的 open 由浏览器切换，然后派发 toggle 事件。
async function userToggle(dom, details, open) {
  details.open = open;
  for (const fn of dom.handlers.get('<details>|toggle') || []) {
    const r = fn({ target: details });
    if (r && typeof r.then === 'function') await r;
  }
}

// 触发某个按钮上的 click 处理器（模拟用户点击），返回注册的处理器个数。
async function click(dom, sel) {
  const fns = dom.handlers.get(sel + '|click') || [];
  for (const fn of fns) {
    const r = fn({ target: dom.q(sel) });
    if (r && typeof r.then === 'function') await r;
  }
  return fns.length;
}

// 等一宏任务：把 initContractPage() 的微任务链（命名空间→部门→契约）跑完。
const settle = () => new Promise((r) => setTimeout(r, 0));

// 打开契约编辑页（contract.html 的真实初始状态：正文与"已保存"卡片都带 hidden）。
async function buildContractDom(scenario = {}) {
  const dom = buildDom(scenario, CONTRACT_SCRIPTS);
  dom.q('#contract-body').hidden = true;
  dom.q('#contract-done').hidden = true;
  await settle();
  return dom;
}

// 填一份"完整"的表单，再按场景做破坏。
function fillServiceForm(dom, over = {}) {
  dom.q('#svc-form-ns').value = 'default';
  dom.q('#svc-form-name').value = 'my-service';
  dom.q('#svc-form-mode').value = 'spec';
  dom.q('#svc-form-spec').value = 'openapi: 3.0.3\npaths:\n  /health:\n    get: {}\n';
  dom.q('#svc-form-repo').value = 'https://github.com/kaulie/my-service.git';
  Object.entries(over).forEach(([k, v]) => { dom.q(k).value = v; });
}

// ---- 场景 ----
// 契约编辑页是**独立页面**（本轮需求）：打开它 = 打开表单，不再有"点按钮弹内联表单"这一步。
async function scenarioContractPageNew() {
  console.log('\n场景：契约编辑页（独立页面 /panel/contract.html）打开即可用');
  const dom = await buildContractDom();
  check(dom.q('#contract-body').hidden === false, '页面正文展开（不是列表里的内联表单）');
  check(String(dom.q('#svc-form-title').textContent).includes('登记服务契约'),
    '标题是登记：' + JSON.stringify(dom.q('#svc-form-title').textContent));
  check(dom.q('#svc-form-mode').value === 'spec', '默认走「粘贴 OpenAPI」');
  check(dom.q('#svc-form-spec').value.includes('openapi: 3.0.3'), 'API 原文已预填最小模板（不粘贴 spec 也能提交成功）');
  check(dom.q('#svc-form-repo').value === '', '新登记时代码仓库输入框是空的（这个字段可选）');
  check(dom.q('#svc-form-name').disabled !== true, '新登记时服务名可填');
}

async function scenarioContractEditFromURL() {
  console.log('\n场景：编辑模式（URL 带 ns/name）把契约回填进表单');
  const svc = {
    ...EVENT_CENTER, version: '9.9.9', owner: 'kaulie', healthPath: '/health',
    description: '事件中心', tags: ['events', 'pubsub'],
    gitRepoUrl: 'https://github.com/kaulie/event-center.git',
    departmentId: 'D0001', departmentName: 'SRE部门',
    api: {
      ...EVENT_CENTER.api,
      docsUrl: 'https://docs.example.com/event-center', specUrl: 'https://x/openapi.yaml',
    },
  };
  const dom = await buildContractDom({
    search: '?ns=default&name=event-center', service: svc, spec: 'openapi: 3.0.3\ninfo:\n  title: event-center\n',
  });
  check(String(dom.q('#svc-form-title').textContent).includes('编辑服务契约 default/event-center'),
    '标题点名在编辑哪个契约：' + JSON.stringify(dom.q('#svc-form-title').textContent));
  check(dom.q('#svc-form-name').value === 'event-center' && dom.q('#svc-form-name').disabled === true,
    '服务名回填并锁定（它是身份，不能改）');
  check(dom.q('#svc-form-version').value === '9.9.9', '版本回填：' + JSON.stringify(dom.q('#svc-form-version').value));
  check(dom.q('#svc-form-owner').value === 'kaulie', 'owner 回填');
  check(dom.q('#svc-form-tags').value === 'events,pubsub', '标签回填');
  check(dom.q('#svc-form-repo').value === 'https://github.com/kaulie/event-center.git', '代码仓库回填');
  check(dom.q('#svc-form-dept').value === 'id:D0001', '归属部门回填（key 用组织接口的 ID）');
  check(dom.q('#svc-form-docs').value === 'https://docs.example.com/event-center', '文档链接回填');
  check(dom.q('#svc-form-spec').value.includes('title: event-center'), '内联 OpenAPI 原文取回来了');
  check(dom.q('#contract-body').hidden === false, '编辑模式同样直接可用');

  // 只改一个字段提交：PUT 打在 URL 里的那个契约上
  dom.q('#svc-form-version').value = '10.0.0';
  await click(dom, '#svc-form-submit');
  const puts = dom.requests.filter((r) => r.method === 'PUT');
  check(puts.length === 1 && puts[0].path === '/v1/namespaces/default/services/event-center',
    'PUT 打在 URL 里的那个契约上：' + (puts[0] ? puts[0].path : '(无)'));
  const body = puts[0] ? JSON.parse(puts[0].body) : {};
  check(body.version === '10.0.0', '改动的版本进了请求体');
  check(body.departmentId === 'D0001' && body.departmentName === 'SRE部门',
    '部门照旧带上（不会因为"没重新选"被清掉）');
  check(body.api.spec.includes('title: event-center'), '内联 spec 原文原样带回（编辑不会把 spec 弄丢）');
}


async function scenarioInvalidGitRepoURL() {
  console.log('\n场景：代码仓库地址写成了简写/本地路径');
  const dom = await buildContractDom();
  fillServiceForm(dom, { '#svc-form-repo': 'github.com/kaulie/my-service' });
  await click(dom, '#svc-form-submit');
  const hint = dom.q('#svc-form-hint');
  check(String(hint.textContent).includes('gitRepoUrl'), '提示点名字段：' + JSON.stringify(hint.textContent));
  check(dom.q('#svc-form-repo')._errors.includes('field-error'), '标红了代码仓库输入框');
  check(dom.requests.filter((r) => r.method === 'PUT').length === 0, '本地就拦住了，不发请求');

  // scp 风格（git remote -v 直接抄）要放行
  const ok = await buildContractDom();
  fillServiceForm(ok, { '#svc-form-repo': 'git@github.com:kaulie/my-service.git' });
  await click(ok, '#svc-form-submit');
  const puts = ok.requests.filter((r) => r.method === 'PUT');
  check(puts.length === 1, 'scp 风格地址可以正常提交');
  check(JSON.parse(puts[0].body).gitRepoUrl === 'git@github.com:kaulie/my-service.git',
    '请求体里带上了 gitRepoUrl：' + (puts[0] ? puts[0].body : '(无)'));
}

async function scenarioSubmitWithoutSpec() {
  console.log('\n场景：填了名字但没填 OpenAPI 原文（最容易遇到的坑）');
  const dom = await buildContractDom();
  fillServiceForm(dom, { '#svc-form-spec': '   ' });
  const n = await click(dom, '#svc-form-submit');
  const hint = dom.q('#svc-form-hint');
  check(n === 1, '提交按钮注册了 click 处理器（否则就是"点了没反应"）');
  check(!!hint.textContent, 'hint 有可见文案：' + JSON.stringify(hint.textContent));
  check(hint.className.includes('hint--error'), 'hint 用醒目错误样式（不再是不起眼的小灰字）');
  check(!!dom.q('#toast').textContent, '同时弹 toast（表单不在视口也能看到）：' + JSON.stringify(dom.q('#toast').textContent));
  check(dom.q('#svc-form-spec')._errors.includes('field-error'), '出错字段被标红（spec 文本框）');
  check(dom.q('#contract-done').hidden === true, '失败时不显示"已保存"卡片，表单内容不丢');
  check(dom.requests.filter((r) => r.method === 'PUT').length === 0, '本地就拦住了，不发请求');
}

async function scenarioInvalidName() {
  console.log('\n场景：服务名非法（大写/下划线）');
  const dom = await buildContractDom();
  fillServiceForm(dom, { '#svc-form-name': 'Bad_Name' });
  await click(dom, '#svc-form-submit');
  const hint = dom.q('#svc-form-hint');
  check(!!hint.textContent, '给出了具体原因：' + JSON.stringify(hint.textContent));
  check(dom.q('#svc-form-name')._errors.includes('field-error'), '标红了服务名输入框');
}

async function scenarioServiceTypeCard() {
  console.log('\n场景：服务卡片显示注册对象类型（service/app），app 额外显示 APP_ID 与操作系统');
  const svc = { ...EVENT_CENTER, type: 'service' };
  const app = { ...EVENT_CENTER, name: 'mobile-app', type: 'app', appId: 'com.example.mobile', os: 'iOS' };
  const dom = buildDom({ services: [svc, app] });
  await click(dom, '#svc-refresh');
  const cards = collectCards(dom.q('#svc-list'));
  const svcCard = cards.find((c) => c.innerHTML.includes('event-center'));
  const appCard = cards.find((c) => c.innerHTML.includes('mobile-app'));
  check(!!svcCard && svcCard.innerHTML.includes('>service</span>'), 'service 卡片显示类型 service');
  check(!!appCard && appCard.innerHTML.includes('>app</span>'), 'app 卡片显示类型 app');
  check(!!appCard && appCard.innerHTML.includes('APP_ID:com.example.mobile'), 'app 卡片显示 APP_ID');
  check(!!appCard && appCard.innerHTML.includes('OS:iOS'), 'app 卡片显示操作系统');
  check(!!svcCard && !svcCard.innerHTML.includes('APP_ID'), 'service 卡片不显示 APP_ID/OS');
}

async function scenarioContractTypeForm() {
  console.log('\n场景：契约编辑页可编辑注册对象类型（service/app），app 时必填 APP_ID 与操作系统');
  const dom = await buildContractDom();
  check(dom.q('#svc-form-type').value === 'service', '新登记默认类型是 service');
  check(dom.q('#svc-form-appid-wrap').hidden === true && dom.q('#svc-form-os-wrap').hidden === true,
    'service 时 APP_ID 与操作系统字段隐藏');

  dom.q('#svc-form-type').value = 'app';
  await fire(dom, '#svc-form-type', 'change');
  check(dom.q('#svc-form-appid-wrap').hidden === false && dom.q('#svc-form-os-wrap').hidden === false,
    '切到 app 后显示 APP_ID 与操作系统字段');

  // app 少了 APP_ID：本地就要拦住，并把错误指到字段上
  fillServiceForm(dom);
  dom.q('#svc-form-appid').value = '';
  await click(dom, '#svc-form-submit');
  check(String(dom.q('#svc-form-hint').textContent).includes('APP_ID'), 'app 缺少 APP_ID 时提示点名字段');
  check(dom.q('#svc-form-appid')._errors.includes('field-error'), '标红 APP_ID 输入框');
  check(dom.requests.filter((r) => r.method === 'PUT').length === 0, '本地就拦住了，不发请求');

  // 补齐后提交：请求体带上 type/appId/os
  const ok = await buildContractDom();
  fillServiceForm(ok);
  ok.q('#svc-form-type').value = 'app';
  await fire(ok, '#svc-form-type', 'change');
  ok.q('#svc-form-appid').value = 'com.example.mobile';
  ok.q('#svc-form-os').value = 'iOS';
  await click(ok, '#svc-form-submit');
  const put = ok.requests.find((r) => r.method === 'PUT');
  const body = put ? JSON.parse(put.body) : {};
  check(body.type === 'app' && body.appId === 'com.example.mobile' && body.os === 'iOS',
    'app 提交时请求体带上 type/appId/os：' + (put ? put.body : '(无请求)'));
}

async function scenarioContractTypeEditApp() {
  console.log('\n场景：编辑 app 契约时回填类型、APP_ID 与操作系统');
  const app = {
    ...EVENT_CENTER, name: 'mobile-app', type: 'app', appId: 'com.example.mobile', os: 'iOS',
    api: { ...EVENT_CENTER.api },
  };
  const dom = await buildContractDom({
    search: '?ns=default&name=mobile-app', service: app,
    spec: 'openapi: 3.0.3\ninfo:\n  title: mobile-app\n',
  });
  check(dom.q('#svc-form-type').value === 'app', '类型回填 app');
  check(dom.q('#svc-form-appid').value === 'com.example.mobile', 'APP_ID 回填');
  check(dom.q('#svc-form-os').value === 'iOS', '操作系统回填 iOS');
  check(dom.q('#svc-form-appid-wrap').hidden === false && dom.q('#svc-form-os-wrap').hidden === false,
    'app 时 APP_ID 与操作系统字段保持可见');
}

async function scenarioSuccess() {
  console.log('\n场景：填齐并提交成功');
  const dom = await buildContractDom();
  fillServiceForm(dom);
  await click(dom, '#svc-form-submit');
  const puts = dom.requests.filter((r) => r.method === 'PUT');
  check(puts.length === 1 && puts[0].path.includes('/v1/namespaces/default/services/my-service'),
    '发出正确请求：' + (puts[0] ? puts[0].path : '(无)'));
  check(String(dom.q('#toast').textContent).includes('已登记'), 'toast 明确告知成功：' + JSON.stringify(dom.q('#toast').textContent));
  check(String(dom.q('#svc-form-hint').textContent).includes('已登记')
    && dom.q('#svc-form-hint').className.includes('hint--ok'),
    '页面内也留下成功提示（不是一闪而过的 toast）');
  check(dom.q('#contract-done').hidden === false, '出现"已保存"卡片');
  check(String(dom.q('#contract-done-text').textContent).includes('default/my-service'),
    '卡片写明存了哪个契约：' + JSON.stringify(dom.q('#contract-done-text').textContent));
  check(String(dom.q('#contract-done-spec').href).includes('/v1/namespaces/default/services/my-service/spec'),
    '"查看内联 spec"指向刚存的契约：' + JSON.stringify(dom.q('#contract-done-spec').href));
}

async function scenarioServerError() {
  console.log('\n场景：服务端返回 400');
  const dom = await buildContractDom({ serverError: true });
  fillServiceForm(dom);
  await click(dom, '#svc-form-submit');
  const hint = dom.q('#svc-form-hint').textContent;
  check(String(hint).includes('服务端说：host 不合法'), 'hint 原样展示服务端原因：' + JSON.stringify(hint));
  check(String(dom.q('#toast').textContent).includes('服务端说：host 不合法'), 'toast 也展示服务端原因');
  check(dom.q('#contract-done').hidden === true, '失败时不显示"已保存"卡片，内容不丢');
}

async function scenarioInstanceForm() {
  console.log('\n场景：新增实例表单（缺 host/port）');
  const dom = buildDom();
  dom.q('#inst-service').value = 'default/my-service';
  await click(dom, '#inst-new');
  await click(dom, '#inst-form-submit');
  const hint = dom.q('#inst-form-hint');
  check(!!hint.textContent, '缺字段有可见提示：' + JSON.stringify(hint.textContent));
  check(dom.q('#inst-form-host')._errors.includes('field-error'), '标红了 host 输入框');

  // host 带协议这种最常见的错，本地就要拦住并说清楚
  dom.q('#inst-form-host')._errors.length = 0;
  dom.q('#inst-form-host').value = 'http://10.0.0.7';
  dom.q('#inst-form-port').value = '9099';
  await click(dom, '#inst-form-submit');
  check(String(dom.q('#inst-form-hint').textContent).includes('不要带协议'), 'host 带协议时给出明确原因');
  check(dom.requests.filter((r) => r.method === 'POST').length === 0, '本地拦截，不发请求');
}

async function scenarioGlobalErrorHandlers() {
  console.log('\n场景：脚本异常兜底（杜绝“没反应”）');
  const dom = buildDom();
  const evs = dom.windowEvents.map((e) => e.ev);
  check(evs.includes('error'), '注册了 window error 处理器');
  check(evs.includes('unhandledrejection'), '注册了 unhandledrejection 处理器');
}

const EVENT_CENTER = {
  namespace: 'default',
  name: 'event-center',
  version: '0.1.0',
  revision: 1,
  owner: 'kaulie',
  instanceCount: 1,
  updatedAt: '2026-09-17T01:00:00Z',
  tags: ['events'],
  api: {
    hasSpec: true, specHash: 'a'.repeat(64), specBytes: 247, protocols: ['http'],
    endpoints: [{ method: 'GET', path: '/health', summary: '健康检查' }],
  },
};

// 组织架构服务（organization）的部门目录：面板的部门下拉就是它。
const ORG_CATALOG = {
  source: 'organization',
  url: 'http://127.0.0.1:4244',
  path: '/api/v1/departments',
  enabled: true, available: true, cached: false, stale: false,
  fetchedAt: '2026-09-17T05:05:00Z',
  departments: [
    { id: 'D0001', name: 'SRE部门', type: '研发' },
    { id: 'D0002', name: '工程效能部门', type: '研发' },
  ],
  types: ['研发', '测试', '产品', '管理'],
};

// 触发某个元素上的事件处理器（change/input 之类没有 click 的快捷方式）。
async function fire(dom, sel, ev) {
  const fns = dom.handlers.get(sel + '|' + ev) || [];
  for (const fn of fns) {
    const r = fn({ target: dom.q(sel) });
    if (r && typeof r.then === 'function') await r;
  }
  return fns.length;
}

async function scenarioDepartmentFromOrg() {
  console.log('\n场景：归属部门的候选来自组织接口（登记时对齐）');
  const dom = await buildContractDom();
  check(dom.q('#svc-form-dept').innerHTML.includes('SRE部门（D0001）'),
    '表单部门下拉是组织接口给的目录：' + JSON.stringify(dom.q('#svc-form-dept').innerHTML));
  check(String(dom.q('#svc-form-dept-note').textContent).includes('已从组织接口同步 2 个部门'),
    '表单里写明"数据来源/成色"：' + JSON.stringify(dom.q('#svc-form-dept-note').textContent));

  fillServiceForm(dom, { '#svc-form-dept': 'id:D0001' });
  await click(dom, '#svc-form-submit');
  const put = dom.requests.find((r) => r.method === 'PUT');
  const body = put ? JSON.parse(put.body) : {};
  check(body.departmentId === 'D0001', '请求体带上了部门 ID：' + (put ? put.body : '(无请求)'));
  check(body.departmentName === 'SRE部门', '同时带上权威部门名（消费方不用再查组织接口）');
}

async function scenarioDepartmentOffline() {
  console.log('\n场景：组织接口不可达时的部门处理（不能阻塞登记）');
  const down = {
    ...ORG_CATALOG, available: false, cached: true, stale: true,
    error: '请求组织接口失败：connection refused',
  };
  const svc = { ...EVENT_CENTER, departmentId: 'D0009', departmentName: '已下线部门' };
  const panel = buildDom({ deptCatalog: down, services: [svc] });
  await click(panel, '#svc-refresh');
  const card = collectCards(panel.q('#svc-list'))[0];
  check(!!card && card.innerHTML.includes('部门:已下线部门'),
    '卡片照常显示部门（stale 的声明值也留着）');
  check(!!card && card.innerHTML.includes('title="归属部门：已下线部门（D0009）'),
    '部门标签的 title 里带 ID（悬停能看到权威标识）');

  // 契约编辑页：目录不可达也要能编辑（下拉里保留契约上实际用到的部门）
  const dom = await buildContractDom({ deptCatalog: down, services: [svc], search: '?ns=default&name=event-center' });
  check(String(dom.q('#svc-form-dept-note').textContent).includes('组织接口暂时不可达'),
    '编辑页明确说明目录为什么是旧的：' + JSON.stringify(dom.q('#svc-form-dept-note').textContent));
  check(dom.q('#svc-form-dept').innerHTML.includes('已下线部门（D0009）'),
    '服务上实际用到的部门仍在候选里（编辑时不会被静默清掉）');
}

async function scenarioDepartmentFilter() {
  console.log('\n场景：按部门过滤服务目录');
  const sre = { ...EVENT_CENTER, departmentId: 'D0001', departmentName: 'SRE部门' };
  const eff = { ...EVENT_CENTER, name: 'billing', departmentId: 'D0002', departmentName: '工程效能部门' };
  const plain = { ...EVENT_CENTER, name: 'plain-service' };
  const dom = buildDom({ services: [sre, eff, plain] });
  await click(dom, '#svc-refresh');
  check(collectCards(dom.q('#svc-list')).length === 3, '未过滤时 3 张卡片');

  dom.q('#svc-dept-filter').value = 'id:D0001';
  await fire(dom, '#svc-dept-filter', 'change');
  let cards = collectCards(dom.q('#svc-list'));
  check(cards.length === 1 && cards[0].innerHTML.includes('event-center'),
    '按 SRE部门 过滤只剩 1 张（实际 ' + cards.length + ' 张）');

  // 关键字也能按部门名搜到（服务目录的过滤框）
  dom.q('#svc-dept-filter').value = '';
  dom.q('#svc-filter').value = '工程效能';
  await fire(dom, '#svc-filter', 'input');
  cards = collectCards(dom.q('#svc-list'));
  check(cards.length === 1 && cards[0].innerHTML.includes('billing'),
    '按部门名做关键字过滤能命中（实际 ' + cards.length + ' 张）');
}

async function scenarioRepoTagRendering() {
  console.log('\n场景：服务卡片上的代码仓库标签（gitRepoUrl）');
  const https = { ...EVENT_CENTER, gitRepoUrl: 'https://github.com/kaulie/event-center.git' };
  const scp = { ...EVENT_CENTER, name: 'billing', gitRepoUrl: 'git@github.com:kaulie/billing.git' };
  const dom = buildDom({ services: [https, scp] });
  await click(dom, '#svc-refresh');
  const cards = collectCards(dom.q('#svc-list'));
  check(cards.length === 2, '两张卡片都渲染出来了（' + cards.length + '）');

  const httpsCard = cards.find((c) => c.innerHTML.includes('event-center'));
  check(!!httpsCard && httpsCard.innerHTML.includes('repo:github.com/kaulie/event-center'),
    'https 仓库显示成短标签 repo:github.com/kaulie/event-center');
  check(!!httpsCard && httpsCard.innerHTML.includes('<a class="tag tag--link" href="https://github.com/kaulie/event-center.git"'),
    'https 仓库是可点的链接（href 是登记时的地址）');

  const scpCard = cards.find((c) => c.innerHTML.includes('billing'));
  check(!!scpCard && scpCard.innerHTML.includes('repo:github.com/kaulie/billing'),
    'scp 风格也照样显示（repo:github.com/kaulie/billing）');
  check(!!scpCard && !scpCard.innerHTML.includes('href="git@'),
    'scp 风格不做成 href（否则就是个被当相对路径的坏链接）');
  check(!!scpCard && scpCard.innerHTML.includes('title="代码仓库：git@github.com:kaulie/billing.git'),
    '原文放在 title 里（悬停能看到、能复制）');
}

async function scenarioExpandedCardStaysOpen() {
  console.log('\n场景：点开「展开」后自动刷新（默认 3s）不得把卡片收起来');
  const dom = buildDom({ services: [EVENT_CENTER] });
  await click(dom, '#svc-refresh'); // 第一次渲染（等价于点开「服务目录」）
  const first = collectDetails(dom.q('#svc-list'))[0];
  check(!!first, '服务卡片里有「展开」节点');
  check(first.open === false, '默认是收起的');
  check(String(first.children[0].textContent).startsWith('展开：'),
    '收起态文案是「展开：…」：' + JSON.stringify(first.children[0].textContent));

  await userToggle(dom, first, true); // 用户点开
  check(String(first.children[0].textContent).startsWith('收起：'),
    '展开后文案变「收起：…」：' + JSON.stringify(first.children[0].textContent));
  check(!!first.dataset.loaded, '展开时才去取调用示例（不重复取）');

  // 自动刷新：数据没变 → 一行 DOM 都不动
  await click(dom, '#svc-refresh');
  const afterRefresh = collectDetails(dom.q('#svc-list'));
  check(afterRefresh.length === 1, '没变的数据不重复插卡片（当前 ' + afterRefresh.length + ' 个）');
  check(afterRefresh[0] === first, '还是同一个节点（没重建、没闪）');
  check(afterRefresh[0].open === true, '自动刷新后依然是展开的 —— 就是本次修复的点');

  // 数据变了（别的平台新登记了一个服务）：已展开的卡片不能被顺手收起来
  dom.setServices([EVENT_CENTER, { ...EVENT_CENTER, name: 'other-service', revision: 1 }]);
  await click(dom, '#svc-refresh');
  const afterChange = collectDetails(dom.q('#svc-list'));
  check(afterChange.length === 2, '新增的服务出现在列表里（' + afterChange.length + ' 张卡片）');
  check(afterChange[0].open === true, '原有卡片即使被重建也保持展开');
  check(afterChange[1].open === false, '新卡片默认收起，互不影响');

  // 用户自己收起来：就不该再自动张开
  await userToggle(dom, afterChange[0], false);
  await click(dom, '#svc-refresh');
  const afterCollapse = collectDetails(dom.q('#svc-list'));
  check(afterCollapse[0].open === false, '用户手动收起的卡片保持收起（不擅自弹开）');
}

// ---- 服务树 ----
async function scenarioServiceTreeGroups() {
  console.log('\n场景：服务树按部门分组，默认全部收起');
  const sre = { ...EVENT_CENTER, departmentId: 'D0001', departmentName: 'SRE部门' };
  const eff = { ...EVENT_CENTER, name: 'billing', departmentId: 'D0002', departmentName: '工程效能部门' };
  const plain = { ...EVENT_CENTER, name: 'plain-service' };
  const dom = buildDom({ services: [sre, eff, plain] });
  await click(dom, '#tree-refresh');
  const root = dom.q('#tree-root');

  let top = treeTopNodes(root);
  check(top.length === 3, '顶级节点 = 组织目录的 2 个部门 + 未归属部门（实际 ' + top.length + '）');
  check(top.every((n) => !treeIsOpen(n)) && top.every((n) => treeCaret(n) === '▸'),
    '默认全部收起（▸ + 子容器 hidden）—— 就是这次要的默认态');
  const labels = top.map(treeLabel);
  check(labels.includes('SRE部门（D0001）') && labels.includes('工程效能部门（D0002）')
    && labels.includes('未归属部门') && labels[labels.length - 1] === '未归属部门',
    '部门来自组织目录、标签是「名称（ID）」，未归属部门永远排最后：' + labels.join(' | '));
  check(treeBadges(top[0]).includes('1 个服务'), '部门上带服务数：' + treeBadges(top[0]));
  check(!treeBadges(top[0]).includes('仅契约声明'), '组织目录里的部门不标「仅契约声明」');
  check(String(dom.q('#tree-note').textContent).includes('部门目录来自组织接口')
    && String(dom.q('#tree-note').textContent).includes('另有 1 个未填归属部门'),
    '工具条说明数据成色与规模：' + JSON.stringify(dom.q('#tree-note').textContent));

  const sreNode = treeFind(root, 'SRE部门（D0001）');
  const svcNode = treeFind(treeKids(sreNode), 'default/event-center');
  check(!!sreNode && !!svcNode, '事件中心挂在 SRE部门 下');
  check(!treeIsOpen(svcNode), '服务节点默认也是收起的');

  const effNode = treeFind(root, '工程效能部门（D0002）');
  check(await fireOn(treeRow(sreNode), 'click') === 1, '行上注册了 click（点一下能开合）');
  check(treeIsOpen(sreNode) && treeCaret(sreNode) === '▾', '点一下展开（▸ → ▾）');
  check(!treeIsOpen(effNode), '只展开被点的那一个，同级部门不受影响');

  await fireOn(treeRow(svcNode), 'click');
  check(treeIsOpen(svcNode), '服务节点也能展开');
  check(treeKids(svcNode).children[0].innerHTML.includes('健康检查'),
    '展开服务看到它的对外 API 端点表（不用额外请求）');

  await fireOn(treeRow(sreNode), 'click');
  check(!treeIsOpen(sreNode), '再点一次收起');

  // 自动刷新：数据没变 → 一行 DOM 都不动（展开状态自然保留）
  await fireOn(treeRow(sreNode), 'click');
  await click(dom, '#tree-refresh');
  top = treeTopNodes(root);
  check(top.length === 3, '自动刷新没有把节点重复堆积（' + top.length + ' 个顶级节点）');
  check(treeFind(root, 'SRE部门（D0001）') === sreNode, '数据没变时复用同一批节点（不重建、不闪）');
  check(treeIsOpen(sreNode), '自动刷新后依然展开');

  // 数据变了（别的平台新登记了一个服务）：重建也必须保住用户展开的那枝
  dom.setServices([sre, eff, plain, { ...EVENT_CENTER, name: 'new-svc', departmentId: 'D0001', departmentName: 'SRE部门' }]);
  await click(dom, '#tree-refresh');
  const sreAfter = treeFind(root, 'SRE部门（D0001）');
  check(sreAfter !== sreNode, '数据变化后确实重建了节点');
  check(treeIsOpen(sreAfter), '重建后用户点开的部门仍然展开 —— 与「服务目录」卡片同一条语义');
  check(treeBadges(sreAfter).includes('2 个服务'), '部门上的服务数跟着变：' + treeBadges(sreAfter));
  check(!treeIsOpen(treeFind(root, '工程效能部门（D0002）')), '没被点开的部门依然是收起的');
}

async function scenarioServiceTreeHierarchy() {
  console.log('\n场景：部门自己也是棵树（组织接口给了 parentId），0 服务的部门也列出来');
  const catalog = {
    ...ORG_CATALOG,
    departments: [
      { id: 'D0001', name: '集团', type: '管理' },
      { id: 'D0002', name: 'SRE部门', type: '研发', parentId: 'D0001' },
      { id: 'D0003', name: 'AI架构部门', type: '研发' }, // 组织里有、但还没有服务
    ],
  };
  const svc = { ...EVENT_CENTER, departmentId: 'D0002', departmentName: 'SRE部门' };
  const orphan = { ...EVENT_CENTER, name: 'legacy', departmentId: 'D0099', departmentName: '已下线部门' };
  const dom = buildDom({ deptCatalog: catalog, services: [svc, orphan] });
  await click(dom, '#tree-refresh');
  const root = dom.q('#tree-root');

  const topLabels = treeTopNodes(root).map(treeLabel).sort();
  check(topLabels.join(' | ') === ['AI架构部门（D0003）', '集团（D0001）', '已下线部门（D0099）'].sort().join(' | '),
    '子部门不占顶层位置（顶层 = ' + topLabels.join(' | ') + '）');
  const group = treeFind(root, '集团（D0001）');
  const sre = treeFind(treeKids(group), 'SRE部门（D0002）');
  check(!!sre, 'parentId 生效：SRE部门 挂在 集团 下面');
  check(!!treeFind(treeKids(sre), 'default/event-center'), '服务挂在子部门下');
  const ai = treeFind(root, 'AI架构部门（D0003）');
  check(treeBadges(ai).includes('0 个服务'), '还没有服务的部门也列出来（0 个服务）：' + treeBadges(ai));
  check(treeKids(ai).children[0].textContent === '该部门下暂无登记的服务', '展开只有一句说明，不是空白');
  const legacy = treeFind(root, '已下线部门（D0099）');
  check(!!legacy && treeBadges(legacy).includes('仅契约声明'),
    '目录里查不到的部门（契约上的声明值）也成一枝、并标明来源：' + (legacy ? treeBadges(legacy) : '(丢了)'));

  // 父节点互相成环：断链当根，不能让整枝从界面上消失
  const cyclic = {
    ...ORG_CATALOG,
    departments: [
      { id: 'D0001', name: '甲部门', parentId: 'D0002' },
      { id: 'D0002', name: '乙部门', parentId: 'D0001' },
    ],
  };
  const dom2 = buildDom({ deptCatalog: cyclic, services: [{ ...EVENT_CENTER, departmentId: 'D0001', departmentName: '甲部门' }] });
  await click(dom2, '#tree-refresh');
  const root2 = dom2.q('#tree-root');
  check(!!treeFind(root2, '甲部门（D0001）') && !!treeFind(root2, '乙部门（D0002）'),
    '成环的部门仍都画得出来（各自断链当根）');
  check(!!treeFind(root2, 'default/event-center'), '成环也不吞掉挂在它下面的服务');

  // 组织接口不可达 + 目录也为空：只能列出契约上出现过的部门，并说明是降级数据
  const down = { ...ORG_CATALOG, available: false, cached: true, stale: true, departments: [], error: '请求组织接口失败：connection refused' };
  const dom3 = buildDom({ deptCatalog: down, services: [orphan] });
  await click(dom3, '#tree-refresh');
  const top3 = treeTopNodes(dom3.q('#tree-root'));
  check(top3.length === 1 && treeLabel(top3[0]) === '已下线部门（D0099）',
    '降级时只列契约上出现过的部门（实际 ' + top3.map(treeLabel).join(' | ') + '）');
  check(dom3.q('#tree-note').className.includes('note--warn')
    && String(dom3.q('#tree-note').textContent).includes('组织接口暂时不可达'),
    '降级时用醒目样式说明成色：' + JSON.stringify(dom3.q('#tree-note').textContent));
}

async function scenarioServiceTreeFilter() {
  console.log('\n场景：服务树过滤 / 全部展开收起');
  const sre = { ...EVENT_CENTER, departmentId: 'D0001', departmentName: 'SRE部门' };
  const eff = { ...EVENT_CENTER, name: 'billing', departmentId: 'D0002', departmentName: '工程效能部门' };
  const dom = buildDom({ services: [sre, eff] });
  await click(dom, '#tree-refresh');
  const root = dom.q('#tree-root');
  check(treeTopNodes(root).length === 2, '两个部门');

  dom.q('#tree-filter').value = 'billing';
  await fire(dom, '#tree-filter', 'input');
  let top = treeTopNodes(root);
  check(top.length === 1 && treeLabel(top[0]) === '工程效能部门（D0002）',
    '过滤后只留命中的那一枝（实际 ' + top.map(treeLabel).join(' | ') + '）');
  check(treeIsOpen(top[0]) && treeIsOpen(treeFind(treeKids(top[0]), 'default/billing')),
    '过滤时命中项自动展开（否则命中还埋在折叠里）');
  check(treeBadges(top[0]).includes('命中 1/1'), '部门上写明命中几个：' + treeBadges(top[0]));

  dom.q('#tree-filter').value = '';
  await fire(dom, '#tree-filter', 'input');
  top = treeTopNodes(root);
  check(top.length === 2, '清空过滤后回到全量');
  check(top.every((n) => !treeIsOpen(n)), '清空过滤后回到默认收起（临时展开不残留）');

  await click(dom, '#tree-expand-all');
  check(collectTreeNodes(root).every(treeIsOpen), '「全部展开」把整棵树都展开');
  await click(dom, '#tree-collapse-all');
  check(collectTreeNodes(root).every((n) => !treeIsOpen(n)), '「全部收起」把整棵树都收起');

  // 全部展开 → 数据变化重建 → 原来展开的还展开（新出现的那枝按默认收起，不擅自弹开）
  await click(dom, '#tree-expand-all');
  dom.setServices([sre, eff, { ...EVENT_CENTER, name: 'third' }]);
  await click(dom, '#tree-refresh');
  check(treeTopNodes(root).length === 3, '新服务带来新的「未归属部门」枝');
  const d1 = treeFind(root, 'SRE部门（D0001）');
  const d2 = treeFind(root, '工程效能部门（D0002）');
  const un = treeFind(root, '未归属部门');
  check(treeIsOpen(d1) && treeIsOpen(d2) && treeIsOpen(treeFind(treeKids(d1), 'default/event-center')),
    '重建后原来展开的节点仍然展开（含服务叶子）');
  check(!treeIsOpen(un), '新出现的枝默认收起（与新服务卡片同一条语义）');
}

console.log('面板冒烟测试（web/panel-smoke.mjs）');
for (const s of [
  scenarioContractPageNew,
  scenarioContractEditFromURL,
  scenarioPanelLinksToContractPage,
  scenarioServiceTypeCard,
  scenarioContractTypeForm,
  scenarioContractTypeEditApp,
  scenarioSubmitWithoutSpec,
  scenarioInvalidName,
  scenarioInvalidGitRepoURL,
  scenarioRepoTagRendering,
  scenarioDepartmentFromOrg,
  scenarioDepartmentOffline,
  scenarioDepartmentFilter,
  scenarioServiceTreeGroups,
  scenarioServiceTreeHierarchy,
  scenarioServiceTreeFilter,
  scenarioSuccess,
  scenarioServerError,
  scenarioInstanceForm,
  scenarioGlobalErrorHandlers,
  scenarioExpandedCardStaysOpen,
]) {
  await s();
}

if (failures.length) {
  console.log('\n失败 ' + failures.length + ' 项：');
  failures.forEach((f) => console.log('  - ' + f));
  process.exit(1);
}
console.log('\n全部通过。');

