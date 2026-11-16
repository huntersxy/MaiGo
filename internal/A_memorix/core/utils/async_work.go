package utils

// 有界等待后台任务取消收尾，对齐 async_work.py 的 cancel_and_wait_for_tasks

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// TaskStopTimeoutSeconds 关停等待上限，对齐 TASK_STOP_TIMEOUT_SECONDS
const TaskStopTimeoutSeconds = 30.0

// StoppableTask 一个可取消并等待退出的后台任务
type StoppableTask struct {
	Name   string
	Cancel func()
	Done   <-chan struct{}
}

// CancelAndWaitForTasks 有界等待取消收尾；超时报告未退出任务并保留调用方资源
func CancelAndWaitForTasks(ctx context.Context, operation string, tasks []StoppableTask, logError func(string)) error {
	return cancelAndWaitForTasksWithTimeout(
		ctx, operation, tasks, time.Duration(TaskStopTimeoutSeconds*float64(time.Second)), logError,
	)
}

func cancelAndWaitForTasksWithTimeout(
	ctx context.Context,
	operation string,
	tasks []StoppableTask,
	timeout time.Duration,
	logError func(string),
) error {
	if len(tasks) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pending := make([]StoppableTask, 0, len(tasks))
	for _, task := range tasks {
		if task.Cancel != nil {
			task.Cancel()
		}
		if task.Done != nil {
			pending = append(pending, task)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	wait := timeout
	if wait <= 0 {
		wait = time.Duration(TaskStopTimeoutSeconds * float64(time.Second))
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	cases := make([]reflect.SelectCase, 0, len(pending)+2)
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(timer.C)})
	for _, task := range pending {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(task.Done)})
	}
	remaining := len(pending)
	for remaining > 0 {
		chosen, _, _ := reflect.Select(cases)
		if chosen == 0 {
			return ctx.Err()
		}
		if chosen == 1 {
			names := make([]string, 0, remaining)
			for index, task := range pending {
				if cases[index+2].Chan.IsValid() {
					names = append(names, task.Name)
				}
			}
			sort.Strings(names)
			message := fmt.Sprintf(
				"%s 超过 %gs，任务仍未退出: %s；底层存储与锁保持占用，请待写入结束后重试关闭",
				operation, wait.Seconds(), strings.Join(names, ", "),
			)
			if logError != nil {
				logError(message)
			}
			return errors.New(message)
		}
		cases[chosen].Chan = reflect.Value{}
		remaining--
	}
	return nil
}
