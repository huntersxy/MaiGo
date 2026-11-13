package learners

import (
	"context"
)

// Service 表达方式 CRUD 与审核开关，对齐 webui/routers/expression.py 核心写读路径
type ExpressionService struct {
	reg *Registry
}

func NewExpressionService(reg *Registry) *ExpressionService {
	if reg == nil {
		reg = DefaultRegistry()
	}
	return &ExpressionService{reg: reg}
}

// RecordCandidate 写入或合并学习候选（situation+style 完全一致时累加 count）
func (s *ExpressionService) RecordCandidate(ctx context.Context, sessionID, situation, style string, checked bool, modifiedBy *ModifiedBy) (*ExpressionRecord, error) {
	rec := ExpressionRecord{
		SessionID:  sessionID,
		Situation:  situation,
		Style:      style,
		Checked:    checked,
		ModifiedBy: modifiedBy,
	}
	saved, err := s.reg.Store().UpsertExpression(ctx, rec)
	if err != nil {
		return nil, err
	}
	s.scheduleExpressionVectorIndex(*saved)
	return saved, nil
}

func (s *ExpressionService) List(ctx context.Context, filter ExpressionFilter) ([]ExpressionRecord, int, error) {
	return s.reg.Store().ListExpressions(ctx, filter)
}

func (s *ExpressionService) Get(ctx context.Context, id uint) (*ExpressionRecord, error) {
	return s.reg.Store().GetExpression(ctx, id)
}

// SetChecked 人工或 AI 审核通过/驳回，对应 WebUI approve 与 checked 字段
func (s *ExpressionService) SetChecked(ctx context.Context, id uint, checked bool, modifiedBy ModifiedBy) error {
	mod := modifiedBy
	if err := s.reg.Store().SetExpressionChecked(ctx, id, checked, &mod); err != nil {
		return err
	}
	if checked {
		if row, err := s.reg.Store().GetExpression(ctx, id); err == nil && row != nil {
			s.scheduleExpressionVectorIndex(*row)
		}
	} else {
		RemoveExpressionVector(s.reg, id)
	}
	return nil
}

func (s *ExpressionService) Delete(ctx context.Context, id uint) error {
	if err := s.reg.Store().DeleteExpression(ctx, id); err != nil {
		return err
	}
	RemoveExpressionVector(s.reg, id)
	return nil
}

// scheduleExpressionVectorIndex 异步写入内存向量索引，嵌入失败不影响主路径
func (s *ExpressionService) scheduleExpressionVectorIndex(rec ExpressionRecord) {
	if s == nil || s.reg == nil || rec.ID == 0 {
		return
	}
	go func(snapshot ExpressionRecord) {
		IndexExpressionRecord(context.Background(), s.reg, snapshot)
	}(rec)
}

// SetChatLearningEnabled 启用/禁用某聊天流的表达学习
func (s *ExpressionService) SetChatLearningEnabled(sessionID string, enabled bool) {
	s.reg.ChatConfig().SetExpressionLearningEnabled(sessionID, enabled)
}

// SetChatUseExpression 启用/禁用某聊天流使用表达方式
func (s *ExpressionService) SetChatUseExpression(sessionID string, enabled bool) {
	s.reg.ChatConfig().SetExpressionEnabled(sessionID, enabled)
}
