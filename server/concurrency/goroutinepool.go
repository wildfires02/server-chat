// Package concurrency 提供了轻量级 Goroutine 线程池以及基于 Channel 实现的简易互斥锁（支持 TryLock 尝试加锁功能）。
package concurrency

// Task 表示要在指定协程池中运行的工作任务函数。
type Task func()

// GoRoutinePool 表示包含关联锁定与控制机制的 Goroutine 协程池。
type GoRoutinePool struct {
	// 工作任务队列 Channel
	work chan Task
	// 信号量，用于控制已分配/运行中 Goroutine 的并发数量
	sem chan struct{}
	// 退出停止信号 Channel
	stop chan struct{}
}

// NewGoRoutinePool 创建并分配一个最多包含 `numWorkers` 个 Goroutine 的新协程池。
func NewGoRoutinePool(numWorkers int) *GoRoutinePool {
	return &GoRoutinePool{
		work: make(chan Task),
		sem:  make(chan struct{}, numWorkers),
		stop: make(chan struct{}, numWorkers),
	}
}

// Schedule 将待执行的闭包任务加入队列，提交到 GoRoutinePool 的协程中运行。
func (p *GoRoutinePool) Schedule(task Task) {
	select {
	case p.work <- task:
	case p.sem <- struct{}{}:
		go p.worker(task)
	}
}

// Stop 向所有正在运行的 Goroutine 发送停止退出信号。
func (p *GoRoutinePool) Stop() {
	numWorkers := cap(p.sem)
	for range numWorkers {
		p.stop <- struct{}{}
	}
}

// worker 协程池中的工作协程循环处理逻辑。
func (p *GoRoutinePool) worker(task Task) {
	defer func() { <-p.sem }()
	for {
		task()
		select {
		case task = <-p.work:
		case <-p.stop:
			return
		}
	}
}
