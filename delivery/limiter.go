package delivery

import (
	"context"
	"errors"
	"sync"
)

// ErrTooManyInFlight 表示并发生成数与排队数均已达到上限。
var ErrTooManyInFlight = errors.New("delivery: generation capacity exhausted")

// Limiter 限制生成阶段的并发与排队数量，提供背压：
// 并发名额满后请求排队，队列也满则立即拒绝（ErrTooManyInFlight），
// 等待可被 ctx 取消；只使用可缓冲信号量与计数器，不会死锁。
type Limiter struct {
	sem      chan struct{}
	maxQueue int

	mu      sync.Mutex
	waiting int
}

// NewLimiter 构造限流器：maxConcurrent 为最大并发生成数（<=0 视为 1），
// maxQueue 为最大排队等待数（<0 视为 0），超出即拒绝。
func NewLimiter(maxConcurrent, maxQueue int) *Limiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if maxQueue < 0 {
		maxQueue = 0
	}
	return &Limiter{sem: make(chan struct{}, maxConcurrent), maxQueue: maxQueue}
}

// Acquire 获取一个生成名额；容量耗尽且队列已满时返回 ErrTooManyInFlight，
// 排队期间 ctx 取消时返回 ctx.Err()。
func (l *Limiter) Acquire(ctx context.Context) error {
	// 快路径：有空闲名额直接获取。
	select {
	case l.sem <- struct{}{}:
		return nil
	default:
	}
	l.mu.Lock()
	if l.waiting >= l.maxQueue {
		l.mu.Unlock()
		return ErrTooManyInFlight
	}
	l.waiting++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.waiting--
		l.mu.Unlock()
	}()
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release 释放名额。必须与成功的 Acquire 配对调用。
func (l *Limiter) Release() { <-l.sem }

// InFlight 返回当前持有的名额数（用于观测与测试）。
func (l *Limiter) InFlight() int { return len(l.sem) }

// Queued 返回当前排队等待者数量（用于观测与测试）。
func (l *Limiter) Queued() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waiting
}
