// Package metrics 是一个极小的 Prometheus 文本暴露实现（无第三方依赖）。
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type family struct {
	typ  string
	help string
}

type sample struct {
	name   string
	labels string // 已序列化并排序，形如 {a="b",c="d"} 或空串
	value  float64
}

// Registry 收集计数器/仪表并输出 Prometheus 文本格式。
type Registry struct {
	mu       sync.Mutex
	families map[string]family
	values   map[string]float64 // key = name + "\x00" + labels
	labels   map[string]string  // 同 key 的标签串（用于输出）
}

// New 创建一个空 Registry。
func New() *Registry {
	return &Registry{
		families: map[string]family{},
		values:   map[string]float64{},
		labels:   map[string]string{},
	}
}

// Counter 声明一个 counter（重复声明会被忽略，help 以首次为准）。
func (r *Registry) Counter(name, help string) { r.declare(name, "counter", help) }

// Gauge 声明一个 gauge。
func (r *Registry) Gauge(name, help string) { r.declare(name, "gauge", help) }

func (r *Registry) declare(name, typ, help string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.families[name]; !ok {
		r.families[name] = family{typ: typ, help: help}
	}
}

// Add 累加一个 counter/gauge 样本。
func (r *Registry) Add(name string, labels map[string]string, delta float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := serialize(labels)
	k := name + "\x00" + l
	r.values[k] += delta
	r.labels[k] = l
}

// Set 设置一个 gauge 样本。
func (r *Registry) Set(name string, labels map[string]string, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := serialize(labels)
	k := name + "\x00" + l
	r.values[k] = v
	r.labels[k] = l
}

// Inc 是 Add(1) 的便捷写法。
func (r *Registry) Inc(name string, labels map[string]string) { r.Add(name, labels, 1) }

// WriteText 输出 Prometheus 文本格式（刻意不叫 WriteTo，避免与 io.WriterTo 混淆）。
func (r *Registry) WriteText(w io.Writer) error {
	r.mu.Lock()
	samples := make([]sample, 0, len(r.values))
	for k, v := range r.values {
		name, l, _ := strings.Cut(k, "\x00")
		samples = append(samples, sample{name: name, labels: l, value: v})
	}
	fams := make(map[string]family, len(r.families))
	for k, v := range r.families {
		fams[k] = v
	}
	r.mu.Unlock()

	sort.Slice(samples, func(i, j int) bool {
		if samples[i].name != samples[j].name {
			return samples[i].name < samples[j].name
		}
		return samples[i].labels < samples[j].labels
	})

	seen := map[string]bool{}
	for _, s := range samples {
		if !seen[s.name] {
			seen[s.name] = true
			f, ok := fams[s.name]
			if !ok {
				f = family{typ: "gauge"}
			}
			if f.help != "" {
				if _, err := fmt.Fprintf(w, "# HELP %s %s\n", s.name, f.help); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "# TYPE %s %s\n", s.name, f.typ); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s%s %s\n", s.name, s.labels, formatFloat(s.value)); err != nil {
			return err
		}
	}
	return nil
}

// Handler 返回 /metrics 的处理器；extra 可为 nil，用于追加运行期读数。
func (r *Registry) Handler(extra func(w io.Writer)) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if extra != nil {
			extra(w)
		}
		_ = r.WriteText(w)
	}
}

// serialize 把标签映射序列化为 Prometheus 标签串（键排序，保证稳定输出）。
func serialize(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escape(labels[k]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return strings.ReplaceAll(v, "\n", `\n`)
}

func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
