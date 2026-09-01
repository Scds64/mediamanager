package transfer

// 通知模块（对应 transfer_notify.py）。
// 提供回调注入机制，解耦 transfer 与消息发送。外部通过 SetNotifyCallback 注入。

import (
	"log"
	"sync"
)

// NotifyFunc 通知回调签名（message, imageURL）。
type NotifyFunc func(message, imageURL string)

var (
	notifyMu       sync.RWMutex
	notifyCallback NotifyFunc
)

// SetNotifyCallback 设置通知回调。
func SetNotifyCallback(cb NotifyFunc) {
	notifyMu.Lock()
	notifyCallback = cb
	notifyMu.Unlock()
	if cb != nil {
		log.Printf("整理通知回调已设置")
	}
}

// Notify 发送通知。
func Notify(message, imageURL string) {
	notifyMu.RLock()
	cb := notifyCallback
	notifyMu.RUnlock()
	if cb != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("发送通知失败: %v", r)
				}
			}()
			cb(message, imageURL)
		}()
		return
	}
	log.Printf("[整理通知] %s", message)
}
