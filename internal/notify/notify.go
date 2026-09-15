// Package notify 提供"写入后唤醒读者"的广播原语，用于 long-poll 与 SSE。
package notify

import "sync"

// Hub 是一个广播器：任何一次提交都能唤醒**全部**等待者。
// 采用经典的"关闭当前 channel 再换一个"实现——关闭即唤醒所有等待者，
// 且不会像带缓冲 channel 那样丢事件（等待者醒来后自己去库里按游标查）。
type Hub struct {
	mu sync.Mutex
	ch chan struct{}
}

// New 创建一个 Hub。
func New() *Hub {
	return &Hub{ch: make(chan struct{})}
}

// Broadcast 唤醒当前所有等待者。必须在数据**已提交之后**调用。
func (h *Hub) Broadcast() {
	h.mu.Lock()
	old := h.ch
	h.ch = make(chan struct{})
	h.mu.Unlock()
	close(old)
}

// Wait 返回一个在下次 Broadcast 时关闭的 channel。
func (h *Hub) Wait() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ch
}
