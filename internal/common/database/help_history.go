package memory

// WebUIHelpSession WebUI 答疑会话，对齐 MaiBot webui_help_sessions
type WebUIHelpSession struct {
	ID        string  `gorm:"primaryKey;column:id" json:"id"`
	Title     string  `gorm:"column:title;not null" json:"title"`
	UpdatedAt float64 `gorm:"column:updated_at;not null;index" json:"updated_at"`
}

func (WebUIHelpSession) TableName() string { return "webui_help_sessions" }

// WebUIHelpTurn 一轮答疑问答，对齐 MaiBot webui_help_turns
type WebUIHelpTurn struct {
	ID        uint    `gorm:"primaryKey;column:id" json:"id"`
	SessionID string  `gorm:"column:session_id;not null;index" json:"session_id"`
	Question  string  `gorm:"column:question;not null" json:"question"`
	ReplyJSON *string `gorm:"column:reply_json" json:"reply_json,omitempty"`
	Error     string  `gorm:"column:error;not null;default:''" json:"error"`
}

func (WebUIHelpTurn) TableName() string { return "webui_help_turns" }
