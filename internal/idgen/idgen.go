// Package idgen 生成带前缀的、按时间有序的短 ID（ULID 风格，无第三方依赖）。
package idgen

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// 前缀（便于在日志/面板里一眼分清对象类型）。
const (
	PrefixInstance  = "inst_"
	PrefixNamespace = "ns_"
	PrefixToken     = "rt_"
)

var (
	mu       sync.Mutex
	lastMs   int64
	lastRand [10]byte
)

// New 返回形如 "inst_01J8Z9K3M4N5P6Q7R8S9T0V1W2" 的 ID。
// 48 位毫秒时间戳 + 80 位随机数，编码为 26 个 Crockford Base32 字符，
// 因此**字典序即时间序**，且同一毫秒内不会重复。
func New(prefix string) string { return prefix + ulid() }

func ulid() string {
	ms := time.Now().UnixMilli()

	mu.Lock()
	if ms == lastMs {
		// 同一毫秒内递增随机段，保证单调且不重复。
		for i := len(lastRand) - 1; i >= 0; i-- {
			lastRand[i]++
			if lastRand[i] != 0 {
				break
			}
		}
	} else {
		lastMs = ms
		if _, err := rand.Read(lastRand[:]); err != nil {
			// crypto/rand 失败属于系统级异常；退化为时间派生值而不是 panic。
			binary.BigEndian.PutUint64(lastRand[:8], uint64(ms))
		}
	}
	entropy := lastRand
	mu.Unlock()

	var data [16]byte
	data[0] = byte(ms >> 40)
	data[1] = byte(ms >> 32)
	data[2] = byte(ms >> 24)
	data[3] = byte(ms >> 16)
	data[4] = byte(ms >> 8)
	data[5] = byte(ms)
	copy(data[6:], entropy[:10])
	return encode(data)
}

// encode 把 16 字节按 5 位一组编码为 26 个 Base32 字符
// （128 位 = 25 组 + 1 组 3 位，最高组左侧补零，与 ULID 一致）。
func encode(data [16]byte) string {
	out := make([]byte, 0, 26)
	var acc uint32
	var bits uint
	for _, b := range data {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, crockford[(acc>>bits)&0x1f])
		}
	}
	if bits > 0 {
		out = append(out, crockford[(acc<<(5-bits))&0x1f])
	}
	return string(out)
}

// Token 生成一个随机注册令牌（明文只在创建/轮换时返回一次）。
func Token() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	const hexd = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexd[c>>4], hexd[c&0x0f])
	}
	return PrefixToken + string(out)
}
