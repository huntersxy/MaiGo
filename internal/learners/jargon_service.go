package learners

import (
	"context"
	"strings"
)

// Service 黑话 CRUD 与 is_jargon 开关，对齐 webui/routers/jargon.py
type JargonService struct {
	reg *Registry
}

func NewJargonService(reg *Registry) *JargonService {
	if reg == nil {
		reg = DefaultRegistry()
	}
	return &JargonService{reg: reg}
}

func (s *JargonService) Create(ctx context.Context, rec JargonRecord) (*JargonRecord, error) {
	rec.Content = strings.TrimSpace(rec.Content)
	return s.reg.Store().CreateJargon(ctx, rec)
}

func (s *JargonService) Update(ctx context.Context, rec JargonRecord) (*JargonRecord, error) {
	return s.reg.Store().UpdateJargon(ctx, rec)
}

// UpdateInference 只写回推断结果字段，避免覆盖并发学习更新
func (s *JargonService) UpdateInference(ctx context.Context, rec JargonRecord) error {
	return s.reg.Store().UpdateJargonInference(ctx, rec)
}

func (s *JargonService) List(ctx context.Context, filter JargonFilter) ([]JargonRecord, int, error) {
	return s.reg.Store().ListJargons(ctx, filter)
}

func (s *JargonService) Get(ctx context.Context, id uint) (*JargonRecord, error) {
	return s.reg.Store().GetJargon(ctx, id)
}

// SetEnabled 设置是否为黑话（白话 vs 黑话），对应 batch/set-jargon
func (s *JargonService) SetEnabled(ctx context.Context, id uint, isJargon bool) error {
	return s.reg.Store().SetJargonIsJargon(ctx, id, isJargon)
}

func (s *JargonService) Delete(ctx context.Context, id uint) error {
	return s.reg.Store().DeleteJargon(ctx, id)
}

// RecordCandidate 机械/LLM 学习管线写入候选，对齐 JargonMiner.process_extracted_entries 写库语义
func (s *JargonService) RecordCandidate(ctx context.Context, sessionID, content string) (*JargonRecord, bool, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false, nil
	}
	return s.reg.Store().UpsertJargonCandidate(ctx, strings.TrimSpace(sessionID), content)
}

// UpsertJargonEntry 写入携带证据组的黑话候选（LLM 批次路径）
func (s *JargonService) UpsertJargonEntry(ctx context.Context, sessionID, content string, evidence []JargonEvidenceGroup) (*JargonRecord, bool, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false, nil
	}
	return s.reg.Store().UpsertJargonEntry(ctx, strings.TrimSpace(sessionID), content, evidence)
}

func (s *JargonService) SetChatLearningEnabled(sessionID string, enabled bool) {
	s.reg.ChatConfig().SetJargonLearningEnabled(sessionID, enabled)
}

func (s *JargonService) SetChatUseJargon(sessionID string, enabled bool) {
	s.reg.ChatConfig().SetJargonEnabled(sessionID, enabled)
}
