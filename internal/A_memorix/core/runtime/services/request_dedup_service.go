package services

// 同请求合并服务，对齐 A_memorix core/runtime/services/request_dedup_service.py

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// RequestDedupService 合并并发执行的相同检索请求
type RequestDedupService struct {
	mu    sync.Mutex
	tasks map[string]*requestDedupTask
}

type requestDedupTask struct {
	done    chan struct{}
	payload map[string]any
	err     error
}

// NewRequestDedupService 构造同请求合并服务
func NewRequestDedupService() *RequestDedupService {
	return &RequestDedupService{tasks: map[string]*requestDedupTask{}}
}

// ExecuteRequestWithDedup 对齐 execute_request_with_dedup
func (s *RequestDedupService) ExecuteRequestWithDedup(
	ctx context.Context,
	requestKey string,
	executor func() (map[string]any, error),
) (bool, map[string]any, error) {
	token := strings.TrimSpace(requestKey)
	if token == "" {
		if executor == nil {
			return false, nil, fmt.Errorf("executor 未初始化")
		}
		payload, err := executor()
		return false, payload, err
	}
	s.mu.Lock()
	if s.tasks == nil {
		s.tasks = map[string]*requestDedupTask{}
	}
	if existing, ok := s.tasks[token]; ok {
		s.mu.Unlock()
		select {
		case <-existing.done:
			return true, existing.payload, existing.err
		case <-ctx.Done():
			return true, nil, ctx.Err()
		}
	}
	task := &requestDedupTask{done: make(chan struct{})}
	s.tasks[token] = task
	s.mu.Unlock()

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				task.payload = nil
				task.err = fmt.Errorf("请求执行 panic: %v", recovered)
			}
			close(task.done)
			s.mu.Lock()
			if s.tasks[token] == task {
				delete(s.tasks, token)
			}
			s.mu.Unlock()
		}()
		if executor == nil {
			task.err = fmt.Errorf("executor 未初始化")
			return
		}
		task.payload, task.err = executor()
	}()
	<-task.done
	return false, task.payload, task.err
}
