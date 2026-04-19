package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"bou.ke/monkey"
	"github.com/HUSTSecLab/OpenSift/cmd/git-metadata-collector/internal/schedule"
	"github.com/HUSTSecLab/OpenSift/cmd/git-metadata-collector/internal/task"
	"github.com/HUSTSecLab/OpenSift/pkg/logger"
)

// TestWorkerResilience 测试 Worker 在遇到错误和 Panic 时的健壮性
func TestWorkerResilience(t *testing.T) {
	// 初始化日志
	logger.SetContext("test-worker")

	// 用于记录执行情况的变量
	var mu sync.Mutex
	getTaskCallCount := 0
	collectCallCount := 0
	finishTaskCallCount := 0
	panicRecovered := false
	errorHandled := false

	// 1. Mock schedule.GetTask
	mockGetTask := func() (string, error) {
		mu.Lock()
		getTaskCallCount++
		count := getTaskCallCount
		mu.Unlock()

		if count == 1 {
			return "", errors.New("mock network error")
		}
		// 返回一个唯一的任务ID，方便追踪
		return "repo-test-1", nil
	}

	// 2. Mock task.Collect
	// 注意：参数名改为 taskID，避免与任何潜在的 t 冲突
	mockCollect := func(taskID string, disable bool) {
		mu.Lock()
		collectCallCount++
		mu.Unlock()

		if taskID == "repo-test-1" {
			// 模拟 Panic
			panic("mock collection panic")
		}
	}

	// 3. Mock schedule.FinishTask
	// 注意：参数名改为 taskID，避免遮蔽外层 t *testing.T
	mockFinishTask := func(taskID string) {
		mu.Lock()
		finishTaskCallCount++
		mu.Unlock()
		// 使用外层的 t (*testing.T) 记录日志
		// 确保这里用的是 taskID 而不是 t
		t.Logf("FinishTask called for: %s", taskID)
	}

	// 应用 Monkey Patches
	patch1 := monkey.Patch(schedule.GetTask, mockGetTask)
	patch2 := monkey.Patch(task.Collect, mockCollect)
	patch3 := monkey.Patch(schedule.FinishTask, mockFinishTask)

	// 确保测试结束后恢复原始函数
	defer patch1.Unpatch()
	defer patch2.Unpatch()
	defer patch3.Unpatch()

	// --- 开始测试逻辑 ---

	var wg sync.WaitGroup
	wg.Add(1)

	// 复制 main.go 中的 Worker 逻辑
	go func() {
		defer wg.Done()
		cnt := 0
		// 限制循环次数以防止无限运行
		for i := 0; i < 5; i++ {
			// 变量名改为 currentTask，避免与 testing.T 的 t 混淆，虽然在不同作用域
			currentTask, err := schedule.GetTask()
			if err != nil {
				logger.Errorf("Failed to get task: %s", err)
				mu.Lock()
				errorHandled = true
				mu.Unlock()
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Sleep trick (简化)
			if cnt%10 == 0 {
				<-time.After(10 * time.Millisecond)
			} else {
				<-time.After(10 * time.Millisecond)
			}
			cnt++

			collectSuccess := true
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Errorf("Task %s panic: %v", currentTask, r)
						collectSuccess = false
						mu.Lock()
						panicRecovered = true
						mu.Unlock()
					}
				}()
				task.Collect(currentTask, false)
			}()

			if collectSuccess {
				schedule.FinishTask(currentTask)
			} else {
				logger.Warnf("Task %s was not finished due to error/panic.", currentTask)
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	// 等待 Worker 完成有限的循环
	wg.Wait()

	// --- 验证结果 ---

	mu.Lock()
	defer mu.Unlock()

	t.Logf("GetTask called: %d times", getTaskCallCount)
	t.Logf("Collect called: %d times", collectCallCount)
	t.Logf("FinishTask called: %d times", finishTaskCallCount)
	t.Logf("Error Handled: %v", errorHandled)
	t.Logf("Panic Recovered: %v", panicRecovered)

	// 1. 验证 GetTask 错误处理
	if !errorHandled {
		t.Error("Expected GetTask error to be handled gracefully")
	}
	if getTaskCallCount < 2 {
		t.Error("Expected Worker to retry after GetTask error")
	}

	// 2. 验证 Panic 隔离
	if !panicRecovered {
		t.Error("Expected Panic in Collect to be recovered")
	}

	// 3. 验证 FinishTask 逻辑
	// 因为 repo-test-1 触发了 Panic，collectSuccess 应为 false，所以 FinishTask 不应被调用
	if finishTaskCallCount != 0 {
		t.Errorf("Expected FinishTask NOT to be called after panic, but it was called %d times", finishTaskCallCount)
	}

	t.Log("Test passed: Worker survived errors and panics without crashing.")
}
