// expvar 状态统计逻辑：汇报在线统计数据，例如
// Session 与 Topic 计数、内存使用率等。
// 状态更新在单独的 goroutine 中运行，以避免
// 阻塞主逻辑流程。

package main

import (
	"encoding/json"
	"expvar"
	"net/http"
	"runtime"
	"sort"
	"time"

	"chat/server/logs"
	"chat/server/store"
)

// 直方图 expvar.Var 的简单实现。
// `Bounds` 指定直方图桶如下（长度 = len(bounds)）：
//
//	(-inf, Bounds[i]) for i = 0
//	[Bounds[i-1], Bounds[i]) for 0 < i < length
//	[Bounds[i-1], +inf) for i = length
type histogram struct {
	Count          int64     `json:"count"`
	Sum            float64   `json:"sum"`
	CountPerBucket []int64   `json:"count_per_bucket"`
	Bounds         []float64 `json:"bounds"`
}

func (h *histogram) addSample(v float64) {
	h.Count++
	h.Sum += v
	idx := sort.SearchFloat64s(h.Bounds, v)
	h.CountPerBucket[idx]++
}

func (h *histogram) String() string {
	if r, err := json.Marshal(h); err == nil {
		return string(r)
	}
	return ""
}

type varUpdate struct {
	// 待更新的变量名称
	varname string
	// 要发布的数值（int, float 等）
	value any
	// 将计数视为增量而非最终绝对值。
	inc bool
}

// 通过 expvar 初始化状态统计汇报。
func statsInit(mux *http.ServeMux, path string) {
	if path == "" || path == "-" {
		return
	}

	mux.Handle(path, expvar.Handler())
	globals.statsUpdate = make(chan *varUpdate, 1024)

	start := time.Now()
	expvar.Publish("Uptime", expvar.Func(func() any {
		return time.Since(start).Seconds()
	}))
	expvar.Publish("NumGoroutines", expvar.Func(func() any {
		return runtime.NumGoroutine()
	}))

	go statsUpdater()

	logs.Info.Printf("stats: variables exposed at '%s'", path)
}

func statsRegisterDbStats() {
	if f := store.Store.DbStats(); f != nil {
		expvar.Publish("DbStats", expvar.Func(f))
	}
}

// 注册整型变量。不检查初始化状态。
func statsRegisterInt(name string) {
	expvar.Publish(name, new(expvar.Int))
}

// 注册直方图变量。`bounds` 指定直方图桶/区间
//（参见 `histogram` 结构体定义旁的注释）
func statsRegisterHistogram(name string, bounds []float64) {
	numBuckets := len(bounds) + 1
	expvar.Publish(name, &histogram{
		CountPerBucket: make([]int64, numBuckets),
		Bounds:         bounds,
	})
}

// 异步发布整型变量
func statsSet(name string, val int64) {
	if globals.statsUpdate != nil {
		select {
		case globals.statsUpdate <- &varUpdate{name, val, false}:
		default:
		}
	}
}

// 异步发布整型变量的增量（或减量）
func statsInc(name string, val int) {
	if globals.statsUpdate != nil {
		select {
		case globals.statsUpdate <- &varUpdate{name, int64(val), true}:
		default:
		}
	}
}

// 异步发布直方图变量的样本值
func statsAddHistSample(name string, val float64) {
	if globals.statsUpdate != nil {
		select {
		case globals.statsUpdate <- &varUpdate{varname: name, value: val}:
		default:
		}
	}
}

// 停止发布统计信息
func statsShutdown() {
	if globals.statsUpdate != nil {
		globals.statsShutdownOnce.Do(func() {
			close(globals.statsUpdate)
		})
	}
}

// 实际发布统计更新的协程
func statsUpdater() {
	for upd := range globals.statsUpdate {
		// 处理变量更新
		if ev := expvar.Get(upd.varname); ev != nil {
			switch v := ev.(type) {
			case *expvar.Int:
				count := upd.value.(int64)
				if upd.inc {
					v.Add(count)
				} else {
					v.Set(count)
				}
			case *histogram:
				val := upd.value.(float64)
				v.addSample(val)
			default:
				logs.Err.Panicf("stats: unsupported expvar type %T", ev)
			}
		} else {
			panic("stats: update to unknown variable " + upd.varname)
		}
	}

	logs.Info.Println("stats: shutdown")
}
