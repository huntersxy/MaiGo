package runtime

import (
	"context"
	"strings"
	"fmt"

	"maibot-go/internal/config"
	"maibot-go/internal/config/persona"

	"go.uber.org/zap"
)

// runState 是一轮思考固定的配置和人格快照
type runState struct {
	config  *config.RuntimeConfig
	persona *persona.Persona
}

type runStateKey struct{}

func withRunState(ctx context.Context, state *runState) context.Context {
	return context.WithValue(ctx, runStateKey{}, state)
}

func runStateFrom(ctx context.Context) *runState {
	state, _ := ctx.Value(runStateKey{}).(*runState)
	return state
}

func newRunState() (*runState, error) {
	cfg := config.Snapshot()
	p, err := persona.NewPersona(&cfg.Persona)
	if err != nil {
		return nil, fmt.Errorf("加载人格失败: %w", err)
	}
	return &runState{config: cfg, persona: p}, nil
}

// personaFor 优先使用本轮快照，其次使用当前配置
func (a *Agent) personaFor(ctx context.Context) *persona.Persona {
	if state := runStateFrom(ctx); state != nil && state.persona != nil {
		return state.persona
	}
	return a.currentPersona()
}

// fallbackBotName 是取不到 QQ 昵称时的自称
const fallbackBotName = "麦麦"

// botName 返回机器人账号昵称，用于自身消息与 @ 渲染
func (a *Agent) botName() string {
	if a == nil {
		return fallbackBotName
	}
	if instance := a.instanceConfig(); instance != nil {
		if name := strings.TrimSpace(instance.Bot.Bot.Nickname); name != "" {
			return name
		}
	}
	return fallbackBotName
}

func (a *Agent) currentPersona() *persona.Persona {
	p, err := persona.NewPersona(&config.Snapshot().Persona)
	if err != nil {
		zap.L().Warn("加载当前人格失败", zap.Error(err))
		return nil
	}
	return p
}

// configFor 优先使用本轮快照，其次使用当前配置
func configFor(ctx context.Context) *config.RuntimeConfig {
	if state := runStateFrom(ctx); state != nil && state.config != nil {
		return state.config
	}
	return config.Snapshot()
}
