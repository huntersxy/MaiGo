package host

import (
	"context"

	"MaiGo/internal/plugin_runtime/protocol"
	"MaiGo/internal/plugin_runtime/transport"
)

// defaultRPCSendQueueSize 默认发送队列容量，对齐 rpc_server.send_queue_size
const defaultRPCSendQueueSize = 128

// rpcSendJob 一次待发送的已编码 RPC 帧
type rpcSendJob struct {
	conn *transport.FramedConn
	data []byte
	done chan error
}

func (j *rpcSendJob) complete(err error) {
	if j == nil || j.done == nil {
		return
	}
	select {
	case j.done <- err:
	default:
	}
}

func normalizeRPCSendQueueSize(size int) int {
	if size <= 0 {
		return defaultRPCSendQueueSize
	}
	return size
}

// startSendWorker 启动队列消费者，ListenRequest 成功后才创建队列
func (s *RPCServer) startSendWorker() {
	if s == nil {
		return
	}
	s.sendMu.Lock()
	queue := make(chan *rpcSendJob, s.sendQueueSize)
	s.sendQueue = queue
	s.sendClosed = false
	s.sendMu.Unlock()

	s.wg.Add(1)
	go s.sendLoop(queue)
}

func (s *RPCServer) sendLoop(queue <-chan *rpcSendJob) {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case job := <-queue:
			if job == nil {
				continue
			}
			err := job.conn.SendFrame(job.data)
			if err != nil {
				err = &protocol.RPCError{Code: protocol.ErrorCodePluginCrashed, Message: err.Error()}
			}
			job.complete(err)
		}
	}
}

// enqueueSend 将请求帧放入有界发送队列并等待真正写入连接
//
// 对齐 rpc_server._enqueue_send：队列满抛 E_BACK_PRESSURE，服务关闭抛
// E_SHUTTING_DOWN，连接写入失败按 E_PLUGIN_CRASHED 返回。
func (s *RPCServer) enqueueSend(ctx context.Context, conn *transport.FramedConn, env protocol.Envelope) error {
	if s == nil {
		return &protocol.RPCError{Code: protocol.ErrorCodeUnknown, Message: "RPC Server 未初始化"}
	}
	if conn == nil {
		return &protocol.RPCError{Code: protocol.ErrorCodePluginCrashed, Message: "Runner 未连接"}
	}
	data, err := s.codec.EncodeEnvelope(env)
	if err != nil {
		return err
	}

	s.sendMu.Lock()
	if s.sendClosed {
		s.sendMu.Unlock()
		return &protocol.RPCError{Code: protocol.ErrorCodeShuttingDown, Message: "服务器正在关闭"}
	}
	queue := s.sendQueue
	if queue == nil {
		s.sendMu.Unlock()
		// 与 Python 启动前 _send_queue 为空时的直发分支保持一致
		if err := conn.SendFrame(data); err != nil {
			return &protocol.RPCError{Code: protocol.ErrorCodePluginCrashed, Message: err.Error()}
		}
		return nil
	}
	job := &rpcSendJob{conn: conn, data: data, done: make(chan error, 1)}
	select {
	case queue <- job:
		s.sendMu.Unlock()
	default:
		s.sendMu.Unlock()
		return &protocol.RPCError{Code: protocol.ErrorCodeBackPressure, Message: "发送队列已满"}
	}

	select {
	case err := <-job.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stopCh:
		return &protocol.RPCError{Code: protocol.ErrorCodeShuttingDown, Message: "服务器正在关闭"}
	}
}

// failQueuedSends 失败所有仍在排队、尚未被 worker 取走的发送任务
func (s *RPCServer) failQueuedSends(code protocol.ErrorCode, message string) int {
	if s == nil {
		return 0
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.sendClosed = true
	if s.sendQueue == nil {
		return 0
	}
	count := 0
	for {
		select {
		case job := <-s.sendQueue:
			if job == nil {
				continue
			}
			job.complete(&protocol.RPCError{Code: code, Message: message})
			count++
		default:
			return count
		}
	}
}
