package runtime

import (
	"MaiGo/internal/maisaka/turn_trigger"

	"go.uber.org/zap"
)

// requestMaisakaPlannerInterrupt 在快速反应模式下用新消息打断正在运行的 Planner
func (a *Agent) requestMaisakaPlannerInterrupt(session *Session) {
	if a == nil || session == nil || !a.plannerMessageSteeringEnabled() {
		return
	}
	runtime := session.MaisakaRuntime()
	if runtime == nil || runtime.FocusModeActive() || runtime.AgentState() != turn_trigger.StateRunning {
		return
	}
	switch runtime.PlannerInterrupt().Request() {
	case "idle":
		return
	case "duplicate":
		zap.L().Info("收到新消息，但当前请求已发起过一次规划器打断，本次不重复打断")
	case "request":
		zap.L().Info("收到新消息，发起规划器打断")
	}
}
