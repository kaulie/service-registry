#!/usr/bin/env node
// 面板端到端冒烟（需要**真实运行中**的注册中心，默认不带任何写操作）：
//   1. 从服务上取回 go:embed 进去的 /panel/app.js（不是本地文件，验证发出去的确实是这份）；
//   2. 在受控 DOM 里跑它，fetch 直接打真实 API；
//   3. 断言交互：服务卡片各自可展开/收起，**自动刷新（默认 3s）不得把已展开的卡片收起来**。
//
// 用法：
//   node web/panel-e2e.mjs http://127.0.0.1:4240          # 只读（推荐）
//   E2E_WRITE=1 node web/panel-e2e.mjs http://127.0.0.1:4399   # 额外验证“数据变化后仍保持展开”，会登记一个 payments 服务
//
// 为什么默认只读：这个脚本会被指向正在服务的实例，而本服务是全局注册中心（元信息就是数据）。
import vm from 'node:vm';

const BASE = process.argv[2] || process.env.PANEL_BASE || 'http://127.0.0.1:4240';
const WRITE = process.env.E2E_WRITE === '1';
const probe = await fetch(BASE + '/health').catch(() => null);
if (!probe || !probe.ok) {
  console.log(`跳过：${BASE} 上没有可用的注册中心（先起服务，或传对地址）`);
  process.exit(0);
}
const appJS = await (await fetch(BASE + '/panel/app.js')).text();
console.log(`面板资源：${BASE}/panel/app.js（${appJS.length} 字节）`);

const handlers = new Map();
const els = new Map();
let apiCalls = 0;

function makeEl(key) {
  const el = {
    _key: key, _errors: [],
    value: '', textContent: '', className: '', title: '',
    hidden: false, disabled: false, open: false,
    dataset: {}, style: {}, children: [],
    classList: { add() {}, remove() {}, contains: () => false, toggle() {} },
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
  let html = '';
  Object.defineProperty(el, 'innerHTML', { get: () => html, set: (v) => { html = v; el.children.length = 0; } });
  return el;
}
const q = (sel) => { if (!els.has(sel)) els.set(sel, makeEl(sel)); return els.get(sel); };

const failures = [];
const check = (cond, what) => {
  console.log('   ' + (cond ? '✓' : '✗') + ' ' + what);
  if (!cond) failures.push(what);
};

const sandbox = {
  console,
  document: { querySelector: q, querySelectorAll: () => [], createElement: (t) => makeEl('<' + t + '>'), addEventListener() {} },
  localStorage: { getItem: () => '', setItem() {}, removeItem() {} },
  location: { origin: BASE },
  setInterval: () => 1,
  clearInterval() {},
  setTimeout: (fn, d) => setTimeout(fn, d),
  clearTimeout: (id) => clearTimeout(id),
  EventSource: function () { this.addEventListener = () => {}; this.close = () => {}; },
  confirm: () => false,
  alert() {},
  addEventListener() {},
  fetch: (p, init) => { apiCalls++; return fetch(new URL(p, BASE), init); },
  JSON, Date, Math, Number, String, Object, Array, Promise, Error,
  encodeURIComponent, isNaN, parseInt, parseFloat, URL,
};
sandbox.window = sandbox;
sandbox.globalThis = sandbox;
vm.createContext(sandbox);
vm.runInContext(appJS, sandbox, { filename: 'app.js' });

const click = async (sel) => {
  const fns = handlers.get(sel + '|click') || [];
  for (const fn of fns) { const r = fn({ target: q(sel) }); if (r && r.then) await r; }
  return fns.length;
};
const findAll = (node, key, out = []) => {
  if (node._key === key) out.push(node);
  (node.children || []).forEach((c) => findAll(c, key, out));
  return out;
};
// 服务卡片是 class="item" 的 div（它的头部/标签是一段 innerHTML 字符串）。
const findCards = (node, out = []) => {
  if (node.className === 'item') out.push(node);
  (node.children || []).forEach((c) => findCards(c, out));
  return out;
};
// 模拟用户点「展开 / 收起」：open 由浏览器切换，然后派发 toggle 事件。
const toggle = async (details, open) => {
  details.open = open;
  for (const fn of handlers.get('<details>|toggle') || []) {
    const r = fn({ target: details }); if (r && r.then) await r;
  }
};

const services = (await (await fetch(BASE + '/v1/services')).json()).services;
console.log('\n真实数据：' + services.map((s) => s.namespace + '/' + s.name).join(', '));

console.log('\n场景：在真实数据上点开「展开」，然后自动刷新（默认 3s 一次）');
await click('#svc-refresh');
const cards = findAll(q('#svc-list'), '<details>');
check(cards.length === services.length, `每个服务一张卡片（${cards.length}）`);
if (!cards.length) {
  console.log('\n列表是空的，没法验证展开行为（先登记一个服务再跑）');
  process.exit(failures.length ? 1 : 0);
}

const first = cards[0];
const summaryText = () => String((first.children[0] && first.children[0].textContent) || first.innerHTML);
await toggle(first, true);
check(first.open === true, '用户点开后是展开的');
check(summaryText().startsWith('收起：') || summaryText().includes('收起'), '文案切换成「收起：…」：' + JSON.stringify(summaryText()));
check(!!first.dataset.loaded, '展开时才去取调用示例（只取一次）');

await click('#svc-refresh'); // 等价于 3 秒后的自动刷新
const after = findAll(q('#svc-list'), '<details>');
check(after.length === services.length, '刷新后卡片没有重复堆积（' + after.length + '）');
check(after[0] === first, '复用同一个节点（没重建、不闪、不丢滚动位置）');
check(after[0].open === true, '自动刷新后依然是展开的 ← 本次修复点');

if (WRITE) {
  // 每次跑用一个新名字：写操作要能被重复执行（否则第二次跑"数据没变"就测不到重建路径了）。
  const name = 'e2e-payments-' + Date.now().toString(36);
  const repo = 'https://github.com/kaulie/' + name + '.git';
  console.log('\n场景：真实数据变化（新登记 ' + name + '，带代码仓库）后，已展开的卡片不能被顺手收起');
  const r = await fetch(BASE + '/v1/namespaces/default/services/' + name, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      gitRepoUrl: repo,
      api: { spec: 'openapi: 3.0.3\ninfo:\n  title: ' + name + '\n  version: 1.0.0\npaths:\n  /pay:\n    post:\n      summary: 支付\n' },
    }),
  });
  check(r.ok, '登记 default/' + name + '（HTTP ' + r.status + '）');
  await click('#svc-refresh');
  const afterChange = findAll(q('#svc-list'), '<details>');
  check(afterChange.length === services.length + 1, '新服务出现在列表里（' + afterChange.length + ' 张卡片）');
  // 注意列表按 namespace/name 排序，原卡片可能就不在第一个位置了 ——
  // 这里断言"原来那个节点实例仍在列表里且仍然展开"，而新卡片是收起的。
  check(afterChange.includes(first) && first.open === true, '数据变化后已展开的卡片仍保持展开');
  check(afterChange.filter((d) => d.open).length === 1, '只有用户点开的那张是展开的，新卡片默认收起');

  // gitRepoUrl 要能在卡片上直接看到（可点的 repo: 标签）
  const card = findCards(q('#svc-list')).find((c) => c.innerHTML.includes(name));
  check(!!card && card.innerHTML.includes('repo:github.com/kaulie/' + name),
    '卡片上有可点的 repo: 标签：' + (card ? JSON.stringify((card.innerHTML.match(/repo:[^"<]*/) || [])[0]) : '(没找到卡片)'));
  check(!!card && card.innerHTML.includes(repo), '标签的链接指向登记时的 gitRepoUrl');

  // 收尾：删掉这次登记的服务，别把测试数据留在库里。
  const del = await fetch(BASE + '/v1/namespaces/default/services/' + name, { method: 'DELETE' });
  check(del.status === 204, '清理测试数据（HTTP ' + del.status + '）');
}

console.log('\n真实 API 请求数：' + apiCalls);
if (failures.length) {
  console.log('\n失败 ' + failures.length + ' 项：');
  failures.forEach((f) => console.log('  - ' + f));
  process.exit(1);
}
console.log('\n全部通过（真实 HTTP + 真实面板资源）。');
