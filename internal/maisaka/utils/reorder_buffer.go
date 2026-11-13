package utils

import (
	"math"

	"go.uber.org/zap"
)

// pendingCommitSize 是单个会话排队等待重排的提交项上限
const pendingCommitSize = 256

// ReorderBuffer 按会话内到达序号重排提交项，保证落库、撤回、入缓冲和思考调度按到达顺序执行
// 等待窗口有上限：超限时丢弃最接近水位的等待项并把水位推进越过它，
// 被越过的序号后续到达时自然跳过，不记录、不留下永久缺口、内存有界；
// 若到达项本身最接近水位，则越过中间缺口直接提交到达项，等待项保持不变
type ReorderBuffer[T any] struct {
	logField func(int64) zap.Field
	next     uint64
	pending  map[uint64]T
}

func NewReorderBuffer[T any](logField func(int64) zap.Field, next uint64) *ReorderBuffer[T] {
	if next == 0 {
		next = 1
	}
	return &ReorderBuffer[T]{logField: logField, pending: make(map[uint64]T), next: next}
}

// push 处理一个到达项；commit 负责真正提交，调用方保证 commit 串行执行
// PendingCount 返回尚未提交的等待项数量
func (b *ReorderBuffer[T]) PendingCount() int {
	if b == nil {
		return 0
	}
	return len(b.pending)
}

// NextSeq 返回当前等待的到达序号水位
func (b *ReorderBuffer[T]) NextSeq() uint64 {
	if b == nil {
		return 0
	}
	return b.next
}

func (b *ReorderBuffer[T]) Push(seq uint64, targetID int64, item T, commit func(T)) {
	for {
		switch {
		case seq < b.next:
			// 水位已越过该项，继续推进后面已就绪的等待项
			b.Drain(commit)
			return
		case seq == b.next:
			commit(item)
			b.next++
			b.Drain(commit)
			return
		default: // seq > b.next：等待窗口有空位时直接排队
			if len(b.pending) < pendingCommitSize {
				b.pending[seq] = item
				b.Drain(commit)
				return
			}
			minSeq := uint64(math.MaxUint64)
			for pendingSeq := range b.pending {
				if pendingSeq < minSeq {
					minSeq = pendingSeq
				}
			}
			if seq < minSeq {
				// 到达项比所有等待项更接近水位：越过中间缺口直接提交到达项，等待项保持
				zap.L().Warn("提交重排等待队列超限，跳过缺口提交最接近水位的到达项", b.logField(targetID), zap.Uint64("skipped_from", b.next), zap.Uint64("seq", seq), zap.Int("pending", len(b.pending)))
				b.next = seq
			} else {
				delete(b.pending, minSeq)
				b.next = minSeq + 1
				zap.L().Error("提交重排等待队列超限，丢弃最旧等待项并推进水位", b.logField(targetID), zap.Uint64("dropped_seq", minSeq), zap.Uint64("watermark", b.next), zap.Int("pending", len(b.pending)))
			}
			// 水位推进后回到循环顶部，当前项直接提交、跳过或重新排队
		}
	}
}

// drain 从当前水位起连续提交已就绪的等待项
func (b *ReorderBuffer[T]) Drain(commit func(T)) {
	for {
		queued, ok := b.pending[b.next]
		if !ok {
			return
		}
		delete(b.pending, b.next)
		commit(queued)
		b.next++
	}
}

func (b *ReorderBuffer[T]) WarnLeftover() {
	if len(b.pending) > 0 {
		zap.L().Warn("停机排空结束，乱序等待项未提交", zap.Int("pending", len(b.pending)))
	}
}
