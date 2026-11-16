package agent

// ConnectionKind 对齐 AgentConfig.connection
type ConnectionKind string

const (
	ConnectionDSH        ConnectionKind = "dsh"
	ConnectionDSHDesktop ConnectionKind = "dsh_desktop"
	ConnectionCustom     ConnectionKind = "custom"
)

// DefaultAgentID 对齐 AgentService.agent_id
const DefaultAgentID = "__maibot_agent__"

// Config 对齐 src/agent/config.py AgentConfig（字段子集）
type Config struct {
	Enabled         bool
	AllowUsers      []string
	AllowGroups     []string
	PlannerEnabled  bool
	PlannerSessions []string
	Connection      ConnectionKind
	DesktopManifest string
	ACPCommand      string
	ACPArgs         []string
	ACPCwd          string
	ACPRemoteCwd    string
	MCPPort         int
	ACPEnv          map[string]string
	ACPModel        string
	ACPTimeoutSec   float64
}

// DefaultConfig 与 Python 默认一致
func DefaultConfig() Config {
	return Config{
		Connection:    ConnectionDSH,
		ACPCommand:    "dsh",
		ACPTimeoutSec: 60,
		ACPEnv:        map[string]string{},
	}
}
