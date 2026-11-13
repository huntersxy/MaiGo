package learners

import (
	"context"
	"strings"
	"sync"

	"go.uber.org/zap"
)

const defaultCacheLimit = 50

// JargonMiner 对齐 MaiBot JargonMiner 的 LLM 提取写库与缓存路径；可选 LLM 含义推断见 MeaningInferencer
// Python 生产路径没有逐条入站的机械扫描入口，这里同样不提供
type JargonMiner struct {
	SessionID   string
	SessionName string
	svc         *JargonService
	inferencer  *MeaningInferencer

	CacheLimit int

	mu        sync.Mutex
	cache     []string
	cacheSeen map[string]struct{}
}

// NewMiner 创建会话级黑话矿工
func NewMiner(sessionID, sessionName string, svc *JargonService) *JargonMiner {
	if svc == nil {
		svc = NewJargonService(DefaultRegistry())
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionName == "" {
		sessionName = sessionID
	}
	return &JargonMiner{
		SessionID:   sessionID,
		SessionName: sessionName,
		svc:         svc,
		CacheLimit:  defaultCacheLimit,
		cacheSeen:   map[string]struct{}{},
	}
}

// GetCachedJargons 返回进程内缓存词条，对齐 get_cached_jargons
func (m *JargonMiner) GetCachedJargons() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.cache))
	copy(out, m.cache)
	return out
}

// SessionInfo 返回当前矿工会话 ID 与显示名
func (m *JargonMiner) SessionInfo() (string, string) {
	if m == nil {
		return "", ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.SessionID, m.SessionName
}

// SetSession 临时切换矿工会话（对齐 _learn_from_batch 对 miner.session_id 的替换）
func (m *JargonMiner) SetSession(sessionID, sessionName string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SessionID = strings.TrimSpace(sessionID)
	if name := strings.TrimSpace(sessionName); name != "" {
		m.SessionName = name
	} else {
		m.SessionName = m.SessionID
	}
}

// ProcessExtractedEntries 处理 LLM/Hook 提取的黑话条目，对齐 process_extracted_entries
func (m *JargonMiner) ProcessExtractedEntries(ctx context.Context, entries []Entry, personNameFilters ...func(string) bool) (saved, updated int, err error) {
	if m == nil {
		return 0, 0, nil
	}
	if len(personNameFilters) > 0 && personNameFilters[0] != nil {
		filter := personNameFilters[0]
		filtered := entries[:0]
		for _, entry := range entries {
			content := strings.TrimSpace(entry.Content)
			if filter(content) {
				zap.L().Info("条目包含人物名称，已过滤", zap.String("content", content))
				continue
			}
			filtered = append(filtered, entry)
		}
		entries = filtered
	}
	entries = MergeEntries(entries)
	entries, aborted := emitJargonEntryBeforePersist(ctx, m.SessionID, m.SessionName, entries)
	if aborted {
		zap.L().Info("黑话提取结果被 Hook 中止，不写入数据库",
			zap.String("session_id", m.SessionID))
		return 0, 0, nil
	}
	if len(entries) == 0 {
		zap.L().Info("Hook 过滤后没有可写入的黑话条目",
			zap.String("session_id", m.SessionID))
		return 0, 0, nil
	}
	_, enable := DefaultRegistry().ChatConfig().GetJargonConfig(m.SessionID)
	if !enable {
		return 0, 0, nil
	}
	accepted := make([]string, 0, len(entries))
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		rec, created, upsertErr := m.svc.UpsertJargonEntry(ctx, m.SessionID, content, entry.Evidence)
		if upsertErr != nil {
			err = upsertErr
			zap.L().Warn("黑话条目写库失败",
				zap.String("session_id", m.SessionID),
				zap.String("content", content),
				zap.Error(upsertErr))
			continue
		}
		if rec == nil {
			continue
		}
		m.addToCache(content)
		if rec.CreatedBy == JargonCreatedByManual {
			continue
		}
		accepted = append(accepted, content)
		if created {
			saved++
		} else {
			updated++
		}
		if ShouldInferRecord(rec) {
			inf := m.ResolveInferencer(DefaultRegistry().JargonRouter())
			InferMeaningAsync(ctx, inf, m.svc, m.SessionID, m.SessionName, *rec)
		}
	}
	if len(accepted) > 0 {
		zap.L().Info("黑话条目处理完成",
			zap.String("session_id", m.SessionID),
			zap.String("session_name", m.SessionName),
			zap.Strings("terms", accepted),
			zap.Int("saved", saved),
			zap.Int("updated", updated))
	}
	return saved, updated, err
}

// SetMeaningInferencer 设置可选 LLM 含义推断器
func (m *JargonMiner) SetMeaningInferencer(inf *MeaningInferencer) {
	if m == nil {
		return
	}
	m.inferencer = inf
}

func (m *JargonMiner) inCache(content string) bool {
	key := NormalizeMatchText(content)
	if key == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.cacheSeen[key]
	return ok
}

func (m *JargonMiner) addToCache(content string) {
	content = strings.TrimSpace(content)
	if content == "" || IsSingleCharJargon(content) {
		return
	}
	key := NormalizeMatchText(content)
	if key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := m.CacheLimit
	if limit <= 0 {
		limit = defaultCacheLimit
	}
	if _, ok := m.cacheSeen[key]; ok {
		// 已存在时移动到末尾表示最近使用，对齐 OrderedDict.move_to_end
		filtered := m.cache[:0]
		for _, existing := range m.cache {
			if NormalizeMatchText(existing) != key {
				filtered = append(filtered, existing)
			}
		}
		m.cache = append(filtered, content)
		return
	}
	m.cache = append(m.cache, content)
	m.cacheSeen[key] = struct{}{}
	if len(m.cache) > limit {
		removed := m.cache[0]
		m.cache = m.cache[1:]
		delete(m.cacheSeen, NormalizeMatchText(removed))
	}
}
