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
const appJS = fs.readFileSync(path.join(here, 'app.js'), 'utf8');

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
function buildDom(scenario = {}) {
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
  };

  const sandbox = {
    console,
    document: {
      querySelector: q,
      querySelectorAll: () => [],
      createElement: (tag) => makeEl('<' + tag + '>'),
      addEventListener() {},
    },
    localStorage: { getItem: () => '', setItem() {}, removeItem() {} },
    location: { origin: 'http://127.0.0.1:4240' },
    setInterval: () => 1,
    clearInterval() {},
    setTimeout: (fn, d) => setTimeout(fn, d),
    clearTimeout: (id) => clearTimeout(id),
    EventSource: function () { this.addEventListener = () => {}; this.close = () => {}; },
    confirm: () => true,
    alert() {},
    addEventListener(ev, fn) { windowEvents.push({ ev, fn }); },
    fetch: (p, init) => {
      const method = (init && init.method) || 'GET';
      requests.push({ method, path: String(p), body: init && init.body });
      const base = String(p).split('?')[0];
      const get = getResponses[base];
      if (method === 'GET' && get) {
        return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(JSON.stringify(get.body)) });
      }
      if (scenario.serverError) {
        const body = { error: { code: 'invalid_request', message: '服务端说：host 不合法' } };
        return Promise.resolve({ ok: false, status: 400, text: () => Promise.resolve(JSON.stringify(body)) });
      }
      const body = { created: true, service: { api: { endpoints: [{ method: 'GET', path: '/health' }] } } };
      return Promise.resolve({ ok: true, status: 201, text: () => Promise.resolve(JSON.stringify(body)) });
    },
    JSON, Date, Math, Number, String, Object, Array, Promise, Error,
    encodeURIComponent, isNaN, parseInt, parseFloat,
  };
  sandbox.window = sandbox;
  sandbox.globalThis = sandbox;

  vm.createContext(sandbox);
  vm.runInContext(appJS, sandbox, { filename: 'app.js' });
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

// 填一份"完整"的表单，再按场景做破坏。
function fillServiceForm(dom, over = {}) {
  dom.q('#svc-form-ns').value = 'default';
  dom.q('#svc-form-name').value = 'my-service';
  dom.q('#svc-form-mode').value = 'spec';
  dom.q('#svc-form-spec').value = 'openapi: 3.0.3\npaths:\n  /health:\n    get: {}\n';
  Object.entries(over).forEach(([k, v]) => { dom.q(k).value = v; });
}

// ---- 场景 ----
async function scenarioFormOpensWithTemplate() {
  console.log('\n场景：点「＋ 登记服务契约」');
  const dom = buildDom();
  const n = await click(dom, '#svc-new');
  await new Promise((r) => setTimeout(r, 0));
  check(n === 1, '“登记服务契约”按钮注册了 click 处理器');
  check(dom.q('#svc-form-card').hidden === false, '表单已展开');
  check(dom.q('#svc-form-spec').value.includes('openapi: 3.0.3'), 'API 原文已预填最小模板（不粘贴 spec 也能提交成功）');
}

async function scenarioSubmitWithoutSpec() {
  console.log('\n场景：填了名字但没填 OpenAPI 原文（最容易遇到的坑）');
  const dom = buildDom();
  fillServiceForm(dom, { '#svc-form-spec': '   ' });
  const n = await click(dom, '#svc-form-submit');
  const hint = dom.q('#svc-form-hint');
  check(n === 1, '提交按钮注册了 click 处理器（否则就是“点了没反应”）');
  check(!!hint.textContent, 'hint 有可见文案：' + JSON.stringify(hint.textContent));
  check(hint.className.includes('hint--error'), 'hint 用醒目错误样式（不再是不起眼的小灰字）');
  check(!!dom.q('#toast').textContent, '同时弹 toast（表单不在视口也能看到）：' + JSON.stringify(dom.q('#toast').textContent));
  check(dom.q('#svc-form-spec')._errors.includes('field-error'), '出错字段被标红（spec 文本框）');
  check(dom.q('#svc-form-card').hidden === false, '失败时保留表单内容，不关闭');
  check(dom.requests.filter((r) => r.method === 'PUT').length === 0, '本地就拦住了，不发请求');
}

async function scenarioInvalidName() {
  console.log('\n场景：服务名非法（大写/下划线）');
  const dom = buildDom();
  fillServiceForm(dom, { '#svc-form-name': 'Bad_Name' });
  await click(dom, '#svc-form-submit');
  const hint = dom.q('#svc-form-hint');
  check(!!hint.textContent, '给出了具体原因：' + JSON.stringify(hint.textContent));
  check(dom.q('#svc-form-name')._errors.includes('field-error'), '标红了服务名输入框');
}

async function scenarioSuccess() {
  console.log('\n场景：填齐并提交成功');
  const dom = buildDom();
  fillServiceForm(dom);
  await click(dom, '#svc-form-submit');
  const puts = dom.requests.filter((r) => r.method === 'PUT');
  check(puts.length === 1 && puts[0].path.includes('/v1/namespaces/default/services/my-service'),
    '发出正确请求：' + (puts[0] ? puts[0].path : '(无)'));
  check(String(dom.q('#toast').textContent).includes('已登记'), 'toast 明确告知成功：' + JSON.stringify(dom.q('#toast').textContent));
  check(dom.q('#svc-form-card').hidden === true, '成功后关闭表单');
  check(dom.q('#svc-form-hint').textContent === '', '成功时清空错误提示');
}

async function scenarioServerError() {
  console.log('\n场景：服务端返回 400');
  const dom = buildDom({ serverError: true });
  fillServiceForm(dom);
  await click(dom, '#svc-form-submit');
  const hint = dom.q('#svc-form-hint').textContent;
  check(String(hint).includes('服务端说：host 不合法'), 'hint 原样展示服务端原因：' + JSON.stringify(hint));
  check(String(dom.q('#toast').textContent).includes('服务端说：host 不合法'), 'toast 也展示服务端原因');
  check(dom.q('#svc-form-card').hidden === false, '失败时不关闭表单，内容不丢');
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

console.log('面板冒烟测试（web/panel-smoke.mjs）');
for (const s of [
  scenarioFormOpensWithTemplate,
  scenarioSubmitWithoutSpec,
  scenarioInvalidName,
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

