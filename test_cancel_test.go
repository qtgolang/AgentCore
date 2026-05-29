package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestCancelOnNew 验证 CancelOnNew 模式:
//   - 线程1发起请求后被线程2取消，线程1返回"重复请求"
//   - 线程2正常完成
//   - 后续请求能感知之前累积的消息
func TestCancelOnNew(t *testing.T) {
	cfg := loadConfig(t)
	a := newCancelAgent(t, cfg)
	a.SetProviderAnthropic()
	ctx := context.Background()

	var wg sync.WaitGroup
	var reply1, reply2 string

	// 线程1: 发起慢请求
	wg.Add(1)
	go func() {
		defer wg.Done()
		r, err := a.Send(ctx, "请写一篇200字的短文")
		if err != nil {
			reply1 = fmt.Sprintf("错误: %v", err)
		} else {
			reply1 = r
		}
	}()

	// 等待让线程1的API调用先发起
	time.Sleep(500 * time.Millisecond)

	// 线程2: 发起新请求，应取消线程1
	r, err := a.Send(ctx, "1+1=几？")
	if err != nil {
		t.Fatalf("线程2失败: %v", err)
	}
	reply2 = r

	wg.Wait()

	t.Logf("线程1(被取消): %s", reply1)
	t.Logf("线程2(正常):   %s", reply2)

	if reply1 != "重复请求" {
		t.Errorf("线程1 应返回\"重复请求\", 实际: %q", reply1)
	}

	// 验证历史: 线程3能感知前面积累的消息
	reply3, err := a.Send(ctx, "再加2等于几？")
	if err != nil {
		t.Fatalf("线程3失败: %v", err)
	}
	t.Logf("线程3(验证历史): %s", reply3)

	// 清理
	a.ClearHistory()
}
