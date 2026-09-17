#!/usr/bin/env node
// 面板端到端冒烟（需要**真实运行中**的注册中心，默认不带任何写操作）：
//   1. 从服务上取回 go:embed 进去的 /panel/shared.js + /panel/app.js（不是本地文件，
//      验证发出去的确实是这份），以及**契约编辑页**（/panel/contract.html + contract.js）资源；
//   2. 在受控 DOM 里跑它们，fetch 直接打真实 API；
//   3. 断言交互：服务卡片各自可展开/收起、**自动刷新（默认 3s）不得把已展开的卡片收起来**，
//      且卡片上的「编辑契约」确实是独立页面（contract.html?ns=..&name=..）的链接。
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
// 面板与契约编辑页共用 shared.js（令牌/api/toast/部门/表单反馈），
// 所以两个脚本要按顺序跑在同一个上下文里 —— 与浏览器的加载顺序一致。
const PAGE_SCRIPTS = ['shared.js', 'app.js'];
const scripts = [];
for (const name of PAGE_SCRIPTS) {
  const text = await (await fetch(`${BASE}/panel/${name}`)).text();
  scripts.push({ name, text });
  console.log(`面板资源：${BASE}/panel/${name}（${text.length} 字节）`);
}
// 契约编辑页是独立页面：它的资源必须真的能取回来（否则「编辑契约」就是个死链）。
// 断言放在下面 check() 定义之后跑。
const contractAssets = {};
for (const path of ['/panel/contract.html', '/panel/contract.js', '/panel/styles.css']) {
  const res = await fetch(BASE + path);
  const text = res.ok ? await res.text() : '';
  contractAssets[path] = { ok: res.ok, status: res.status, text };
  console.log(`面板资源：${BASE}${path}（HTTP ${res.status}，${text.length} 字节）`);
}

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
      // 服务树的行是 createElement 造出来的（所有 <div> 共用一个 key），
      // 要"只点某一行"就得能从元素本身拿到它的处理器。
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

// 契约编辑页（独立页面）的静态资源断言
console.log('\n场景：契约编辑页（独立页面）确实随服务一起发出去了');
for (const [path, a] of Object.entries(contractAssets)) {
  check(a.ok && a.text.length > 0, `${path} 可访问（HTTP ${a.status}，${a.text.length} 字节）`);
}
check(contractAssets['/panel/contract.html'].text.includes('contract.js'), '契约编辑页引用了 contract.js');
check(contractAssets['/panel/contract.html'].text.includes('shared.js'), '契约编辑页引用了 shared.js');
check(contractAssets['/panel/contract.html'].text.includes('id="svc-form-submit"'),
  '契约编辑页里有提交按钮（表单的落点）');
check(contractAssets['/panel/contract.js'].text.includes('initContractPage'), 'contract.js 会按 URL 初始化（登记 / 编辑）');

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
// 主题开关会写 <html data-theme>（真实 DOM 里就是 documentElement）
sandbox.document.documentElement = { setAttribute() {}, getAttribute: () => 'dark' };
sandbox.document.title = '';
vm.createContext(sandbox);
for (const s of scripts) {
  vm.runInContext(s.text, sandbox, { filename: s.name });
}

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

// 服务树的节点：class 里带 tree__node 的 div（树是 createElement 造的，不走 q(sel)）。
const isTreeNode = (n) => String(n.className || '').split(' ').includes('tree__node');
const treeNodes = (node, out = []) => {
  if (isTreeNode(node)) out.push(node);
  (node.children || []).forEach((c) => treeNodes(c, out));
  return out;
};
const treeTop = (root) => (root.children || []).filter(isTreeNode);
const treeRow = (n) => n.children[0];
const treeKids = (n) => n.children[1];
const treeLabel = (n) => treeRow(n).children[1].textContent;
const treeIsOpen = (n) => treeKids(n).hidden === false;
const fireOn = async (el, ev) => {
  const fns = (el._handlers && el._handlers[ev]) || [];
  for (const fn of fns) { const r = fn({ target: el }); if (r && r.then) await r; }
  return fns.length;
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

// 「编辑契约」必须是独立页面（contract.html?ns=..&name=..）的链接，在新页签打开。
{
  const cardEl = findCards(q('#svc-list'))[0];
  const anchors = findAll(cardEl, '<a>');
  const edit = anchors.find((a) => String(a.textContent).includes('编辑契约'));
  check(!!edit, '服务卡片上有「编辑契约」链接');
  check(!!edit && /^contract\.html\?ns=.+&name=.+$/.test(String(edit.href)),
    '它指向独立页面：「' + (edit && edit.href) + '」');
  check(!!edit && edit.target === '_blank', '在新页签打开（面板的展开状态与滚动位置不受影响）');
}

await toggle(first, true);
check(first.open === true, '用户点开后是展开的');
check(summaryText().startsWith('收起：') || summaryText().includes('收起'), '文案切换成「收起：…」：' + JSON.stringify(summaryText()));
check(!!first.dataset.loaded, '展开时才去取调用示例（只取一次）');

await click('#svc-refresh'); // 等价于 3 秒后的自动刷新
const after = findAll(q('#svc-list'), '<details>');
check(after.length === services.length, '刷新后卡片没有重复堆积（' + after.length + '）');
check(after[0] === first, '复用同一个节点（没重建、不闪、不丢滚动位置）');
check(after[0].open === true, '自动刷新后依然是展开的 ← 本次修复点');

// ---- 服务树：真实数据 + 真实面板资源 ----
console.log('\n场景：服务树 —— 默认收起、点开只动一枝、自动刷新不丢展开状态');
if (!(await click('#tree-refresh'))) {
  console.log('   跳过：这份面板还没有「服务树」页签（旧版本）');
} else {
  const root = q('#tree-root');
  const top = treeTop(root);
  check(top.length > 0, `树上至少有一枝（部门 / 未归属部门）（${top.length}）`);
  check(top.every((n) => !treeIsOpen(n)), '默认全部收起');
  console.log('   工具条：' + JSON.stringify(q('#tree-note').textContent));
  console.log('   顶级枝：' + top.map(treeLabel).join(' | '));

  // 每个登记过的服务都必须在树上找得到（服务的标签就是 namespace/name）
  const labels = treeNodes(root).map(treeLabel);
  const missing = services.filter((s) => !labels.includes(s.namespace + '/' + s.name));
  check(missing.length === 0, '每个服务都出现在树上'
    + (missing.length ? '（缺 ' + missing.map((s) => s.namespace + '/' + s.name).join(', ') + '）' : ''));
  if (services.some((s) => !s.departmentId && !s.departmentName)) {
    check(treeLabel(top[top.length - 1]) === '未归属部门', '没填部门的服务收在最后一枝「未归属部门」里');
  }

  const first = top[0];
  check(await fireOn(treeRow(first), 'click') === 1, '点这一行能开合');
  check(treeIsOpen(first), '被点的那枝展开了');
  check(top.slice(1).every((n) => !treeIsOpen(n)), '其它枝不受影响（仍是收起的）');
  await fireOn(treeRow(first), 'click');
  check(!treeIsOpen(first), '再点一次收起');

  await fireOn(treeRow(first), 'click');
  await click('#tree-refresh'); // 等价于 3 秒后的自动刷新
  check(treeTop(root)[0] === first && treeIsOpen(first), '自动刷新后还是同一批节点、且保持展开');

  await click('#tree-expand-all');
  check(treeNodes(root).every(treeIsOpen), '「全部展开」展开整棵树');
  await click('#tree-collapse-all');
  check(treeNodes(root).every((n) => !treeIsOpen(n)), '「全部收起」收起整棵树');
}

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
