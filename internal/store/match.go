package store

import (
	"regexp"
	"sort"
	"strings"
)

// 端点路径匹配顺序（文档与实现必须一致）：
//
//	exact    查询串与登记的路径字面完全一致
//	template 登记的是模板（如 /v1/streams/{stream}/events），查询的是具体路径
//	glob     查询里含 * 或 **：* 匹配单段（不含 /），** 跨段匹配
//
// 通配匹配的实现技巧：把登记路径里的 {param} 换成字面 '*' 作为"探针串"，
// 再把查询串编译成正则去匹配探针串 —— 段通配 [^/]+ 恰好也能匹配那个 '*' 字符，
// 于是 `/v1/streams/*/events` 能命中 `/v1/streams/{stream}/events`，
// 而 `/v1/**` 能命中该服务下任意路径。
type endpointMatcher struct {
	mode  string // 显式指定："" (自动) | exact | template | glob
	query string
	glob  *regexp.Regexp
}

func newEndpointMatcher(mode, query string) *endpointMatcher {
	m := &endpointMatcher{mode: mode, query: strings.TrimSpace(query)}
	if strings.ContainsAny(m.query, "*?{") {
		m.glob = compileGlob(m.query)
	}
	return m
}

// match 返回是否命中及命中类型。
func (m *endpointMatcher) match(registered string) (bool, string) {
	if m.query == "" {
		return true, "any"
	}
	switch m.mode {
	case "exact":
		return m.query == registered, "exact"
	case "template":
		if re := compileTemplate(registered); re != nil && re.MatchString(m.query) {
			return true, "template"
		}
		return false, ""
	case "glob":
		if m.glob != nil && m.glob.MatchString(probe(registered)) {
			return true, "glob"
		}
		return false, ""
	}
	// 自动：精确 → （查询含通配则按通配）→ 模板。
	if m.query == registered {
		return true, "exact"
	}
	if m.glob != nil {
		// 查询本身带 * / ** / ? / {x}，说明调用方给的是"模式"而不是具体路径，
		// 此时不该按"具体路径 vs 登记模板"来解释，直接走通配。
		if m.glob.MatchString(probe(registered)) {
			return true, "glob"
		}
		return false, ""
	}
	if re := compileTemplate(registered); re != nil && re.MatchString(m.query) {
		return true, "template"
	}
	return false, ""
}

// compileTemplate 把登记路径编译为"具体路径"正则（{param} → 单段通配）。
// 登记路径里没有参数时返回 nil（此时只可能是精确匹配）。
func compileTemplate(registered string) *regexp.Regexp {
	if !strings.Contains(registered, "{") {
		return nil
	}
	segs := strings.Split(registered, "/")
	for i, seg := range segs {
		if isParamSegment(seg) {
			segs[i] = "[^/]+"
			continue
		}
		segs[i] = regexp.QuoteMeta(seg)
	}
	re, err := regexp.Compile("^" + strings.Join(segs, "/") + "$")
	if err != nil {
		return nil
	}
	return re
}

func isParamSegment(seg string) bool {
	return len(seg) > 2 && strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

// compileGlob 把查询串编译为正则：* → 单段，** → 跨段。
func compileGlob(q string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(q); i++ {
		switch q[i] {
		case '*':
			if i+1 < len(q) && q[i+1] == '*' {
				b.WriteString(".*")
				i++
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '{':
			// 查询本身写成模板（如 /v1/x/{id}/y）时，按单段通配处理。
			end := strings.IndexByte(q[i:], '}')
			if end < 0 {
				b.WriteString(regexp.QuoteMeta(string(q[i])))
				continue
			}
			b.WriteString("[^/]+")
			i += end
		default:
			b.WriteString(regexp.QuoteMeta(string(q[i])))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

// probe 把登记路径里的 {param} 换成本地字面 '*'（见 endpointMatcher 注释）。
func probe(registered string) string {
	if !strings.Contains(registered, "{") {
		return registered
	}
	segs := strings.Split(registered, "/")
	for i, seg := range segs {
		if isParamSegment(seg) {
			segs[i] = "*"
		}
	}
	return strings.Join(segs, "/")
}

func sortEndpointMatches(ms []EndpointMatch, rank map[string]int) {
	sort.SliceStable(ms, func(i, j int) bool {
		ri, rj := rank[ms[i].MatchType], rank[ms[j].MatchType]
		if ri != rj {
			return ri < rj
		}
		if len(ms[i].Endpoint.Path) != len(ms[j].Endpoint.Path) {
			return len(ms[i].Endpoint.Path) > len(ms[j].Endpoint.Path)
		}
		if ms[i].Namespace != ms[j].Namespace {
			return ms[i].Namespace < ms[j].Namespace
		}
		if ms[i].Service != ms[j].Service {
			return ms[i].Service < ms[j].Service
		}
		return ms[i].Endpoint.Method < ms[j].Endpoint.Method
	})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func matchQuery(q string, fields ...string) bool {
	q = strings.ToLower(q)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}
