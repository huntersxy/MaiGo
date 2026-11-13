package desktoppet

import (
	"context"
	"sort"
	"sync"

	"maibot-go/internal/plugin_runtime"
	"maibot-go/internal/plugin_runtime/protocol"

	"go.uber.org/zap"
)

// maxButtonErrorText 按钮状态读取失败时回填的文案长度，对齐 _sync_buttons 的 str(result)[:200]
const maxButtonErrorText = 200

// ButtonProvider 桌宠按钮来源，对齐 plugin_runtime.integration 的桌宠按钮接口
type ButtonProvider interface {
	// DesktopPetButtonContributions 返回声明了桌宠按钮的插件贡献
	DesktopPetButtonContributions() map[string]pluginruntime.DesktopPetButtonContribution
	// DesktopPetButtons 读取按钮快照；buttonID 非空时先触发点击
	DesktopPetButtons(ctx context.Context, pluginID, buttonID string) (protocol.DesktopPetButtonSnapshot, error)
}

// petButton 窗口按钮项的完整形状：声明字段、运行态与插件信息
type petButton struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Icon       string `json:"icon"`
	Order      int    `json:"order"`
	Visible    bool   `json:"visible"`
	Disabled   bool   `json:"disabled"`
	Text       string `json:"text"`
	PluginID   string `json:"plugin_id"`
	PluginName string `json:"plugin_name"`
}

// SetButtonProvider 注入插件按钮来源
func (s *Service) SetButtonProvider(provider ButtonProvider) {
	s.mu.Lock()
	s.buttons = provider
	s.mu.Unlock()
}

// NotifyButtonsChanged 供插件加载/卸载后触发按钮同步，对齐 subscribe_status_changes
func (s *Service) NotifyButtonsChanged() {
	s.buttonsChanged()
}

// buttonsChanged 递增 revision 并在窗口就绪时启动一轮同步
func (s *Service) buttonsChanged() {
	s.mu.Lock()
	s.buttonRevision++
	ready, closing, syncing := s.ready, s.closing, s.buttonSyncing
	if !ready || closing || syncing {
		s.mu.Unlock()
		return
	}
	s.buttonSyncing = true
	ctx := s.ctx
	s.mu.Unlock()
	go s.syncButtons(ctx)
}

// syncButtons 对齐 _sync_buttons：请求期间 revision 变化则再同步一轮
func (s *Service) syncButtons(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.buttonSyncing = false
		s.mu.Unlock()
	}()
	for {
		s.mu.RLock()
		revision, ready, closing := s.buttonRevision, s.ready, s.closing
		provider := s.buttons
		s.mu.RUnlock()
		if !ready || closing || provider == nil {
			return
		}

		contributions := provider.DesktopPetButtonContributions()
		buttons := s.collectButtons(ctx, provider, contributions)
		current := provider.DesktopPetButtonContributions()
		filtered := buttons[:0]
		for _, button := range buttons {
			if equalContribution(current[button.PluginID], contributions[button.PluginID]) {
				filtered = append(filtered, button)
			}
		}
		buttons = filtered
		sort.SliceStable(buttons, func(left, right int) bool {
			if buttons[left].Order != buttons[right].Order {
				return buttons[left].Order < buttons[right].Order
			}
			if buttons[left].PluginID != buttons[right].PluginID {
				return buttons[left].PluginID < buttons[right].PluginID
			}
			return buttons[left].ID < buttons[right].ID
		})

		if _, err := s.call(ctx, map[string]any{"type": "buttons", "buttons": buttons}); err != nil {
			s.setError(err)
			zap.L().Warn("桌宠按钮同步失败", zap.Error(err))
			return
		}
		s.mu.RLock()
		unchanged := revision == s.buttonRevision
		s.mu.RUnlock()
		if unchanged {
			return
		}
	}
}

// collectButtons 并发读取各插件按钮状态，失败的插件整组回填禁用态
func (s *Service) collectButtons(ctx context.Context, provider ButtonProvider, contributions map[string]pluginruntime.DesktopPetButtonContribution) []petButton {
	pluginIDs := make([]string, 0, len(contributions))
	for pluginID := range contributions {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)

	perPlugin := make([][]petButton, len(pluginIDs))
	var wg sync.WaitGroup
	for index, pluginID := range pluginIDs {
		wg.Add(1)
		go func(index int, pluginID string) {
			defer wg.Done()
			contribution := contributions[pluginID]
			snapshot, err := provider.DesktopPetButtons(ctx, pluginID, "")
			buttons := make([]petButton, 0, len(contribution.Buttons))
			for _, declared := range contribution.Buttons {
				state := protocol.DesktopPetButtonState{}
				if err != nil {
					state = protocol.DesktopPetButtonState{
						Visible:  true,
						Disabled: true,
						Text:     truncateRunes(err.Error(), maxButtonErrorText),
					}
				} else {
					state = snapshot.States[declared.ID]
				}
				buttons = append(buttons, petButton{
					ID:         declared.ID,
					Label:      declared.Label,
					Icon:       declared.Icon,
					Order:      declared.Order,
					Visible:    state.Visible,
					Disabled:   state.Disabled,
					Text:       state.Text,
					PluginID:   pluginID,
					PluginName: contribution.Name,
				})
			}
			perPlugin[index] = buttons
		}(index, pluginID)
	}
	wg.Wait()

	out := make([]petButton, 0, len(pluginIDs))
	for _, buttons := range perPlugin {
		out = append(out, buttons...)
	}
	return out
}

// clickButton 对齐 _click_button：执行按钮回调后回执，再触发一次按钮同步
func (s *Service) clickButton(ctx context.Context, pluginID, buttonID string) {
	s.mu.RLock()
	provider := s.buttons
	s.mu.RUnlock()

	errorText := ""
	if provider == nil {
		errorText = "桌宠按钮运行时未接入"
	} else if _, err := provider.DesktopPetButtons(ctx, pluginID, buttonID); err != nil {
		errorText = err.Error()
		zap.L().Error("桌宠按钮执行失败",
			zap.String("plugin_id", pluginID),
			zap.String("button_id", buttonID),
			zap.Error(err))
	}

	s.mu.RLock()
	ready, closing := s.ready, s.closing
	s.mu.RUnlock()
	if !ready || closing {
		return
	}

	_, err := s.call(ctx, map[string]any{
		"type":      "button_result",
		"plugin_id": pluginID,
		"button_id": buttonID,
		"error":     errorText,
	})
	if err != nil {
		s.setError(err)
		zap.L().Warn("桌宠按钮回执发送失败", zap.Error(err))
		return
	}
	s.buttonsChanged()
}

// equalContribution 判断插件按钮声明是否未变，卸载或重载后的贡献不回到窗口
func equalContribution(left, right pluginruntime.DesktopPetButtonContribution) bool {
	if left.Name != right.Name || len(left.Buttons) != len(right.Buttons) {
		return false
	}
	for index := range left.Buttons {
		leftButton, rightButton := left.Buttons[index], right.Buttons[index]
		if leftButton.ID != rightButton.ID || leftButton.Label != rightButton.Label ||
			leftButton.Icon != rightButton.Icon || leftButton.Order != rightButton.Order {
			return false
		}
	}
	return true
}

// truncateRunes 按字符截断，避免把多字节字符截半
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
