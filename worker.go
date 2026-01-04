package main

import (
	"context"
	"sync"
)

// WorkerPool limits concurrent goroutine execution
type WorkerPool struct {
	sem chan struct{}
	wg  sync.WaitGroup
	ctx context.Context
}

func NewWorkerPool(ctx context.Context, maxWorkers int) *WorkerPool {
	return &WorkerPool{
		sem: make(chan struct{}, maxWorkers),
		ctx: ctx,
	}
}

func (wp *WorkerPool) Submit(task func()) bool {
	select {
	case wp.sem <- struct{}{}:
		wp.wg.Add(1)
		go func() {
			defer func() {
				wp.wg.Done()
				<-wp.sem
				if r := recover(); r != nil {
					logf("PANIC in worker: %v", r)
				}
			}()
			task()
		}()
		return true
	case <-wp.ctx.Done():
		return false
	}
}

func (wp *WorkerPool) Wait() {
	wp.wg.Wait()
}
