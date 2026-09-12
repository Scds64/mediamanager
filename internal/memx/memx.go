// Package memx 提供内存归还与统计工具。
//
// 背景：Go 的 scavenger 按需归还内存，在 GOMEMLIMIT 宽松时会长期持有空闲 span，
// 导致 RSS 停在历史峰值水位。任务结束后主动调用 Release 可立即归还给 OS。
package memx

import (
	"log"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

const (
	// 空闲量小于此值时，不值得付出一次 GC 的代价
	minWasteBytes = 8 << 20 // 8 MiB
	// 两次归还的最小间隔，防止高频任务反复触发
	minInterval = 2 * time.Minute
)

var (
	mu     sync.Mutex
	lastAt time.Time
)

// Stats 返回堆统计（MiB）。
//
//	allocMiB = HeapAlloc，存活对象占用的堆
//	sysMiB   = HeapSys，向 OS 申请的堆内存（含已空闲未归还的部分）
func Stats() (allocMiB, sysMiB float64) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20), float64(m.HeapSys) / (1 << 20)
}

// Release 强制 GC 并归还空闲内存给 OS。
//
// force=false 时仅在「空闲量 >= 8MiB」且「距上次归还 > 2 分钟」时才执行，
// 适用于事件驱动的、可能较频繁的任务结束点。
// force=true 时无条件执行，适用于确定要长时间空闲的结束点。
//
// 返回是否执行了归还。
func Release(force bool) bool {
	mu.Lock()
	defer mu.Unlock()

	alloc, sys := Stats()
	if !force && (sys-alloc < float64(minWasteBytes)/(1<<20) || time.Since(lastAt) < minInterval) {
		return false
	}

	// 内部已包含一次阻塞式 GC，无需再手动调用 runtime.GC()
	debug.FreeOSMemory()
	lastAt = time.Now()

	_, after := Stats()
	log.Printf("[内存] 已归还空闲内存：HeapSys %.1fMiB → %.1fMiB（存活 %.1fMiB）", sys, after, alloc)
	return true
}
