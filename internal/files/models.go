package filesvc

// 一张表对应一个结构体，字段顺序、列名、可空性与 MaiBot src/files/models.py 逐项一致。
//
// 时间列统一用 float64 的 Unix 秒（Python 侧 time.time() 的返回值），不用 time.Time，
// 避免同一列在两侧出现不同的类型映射。

// Asset 对应 file_assets，file_ref 为对外引用，sha256 是内容校验值
// Source 保存平台适配器给出的定位信息，SourceKey 是其规范化哈希（可空、唯一）
type Asset struct {
	FileRef         string  `gorm:"column:file_ref;primaryKey"`
	Name            string  `gorm:"column:name;not null"`
	MimeType        string  `gorm:"column:mime_type;not null;default:'application/octet-stream'"`
	Size            int64   `gorm:"column:size;not null;default:0"`
	SHA256          string  `gorm:"column:sha256;not null;default:''"`
	Origin          string  `gorm:"column:origin;not null;default:'remote'"`
	State           string  `gorm:"column:state;not null;default:'remote'"`
	Source          string  `gorm:"column:source;not null;default:'{}'"`
	SourceKey       *string `gorm:"column:source_key;uniqueIndex"`
	Error           string  `gorm:"column:error;not null;default:''"`
	CreatedAt       float64 `gorm:"column:created_at;not null"`
	AccessedAt      float64 `gorm:"column:accessed_at;not null"`
	ReservedBytes   int64   `gorm:"column:reserved_bytes;not null;default:0"`
	LeasedUntil     float64 `gorm:"column:leased_until;not null;default:0"`
	OpenReaders     int     `gorm:"column:open_readers;not null;default:0"`
	TransferID      string  `gorm:"column:transfer_id;not null;default:''"`
	TransferStarted float64 `gorm:"column:transfer_started;not null;default:0"`
}

func (Asset) TableName() string { return "file_assets" }

// Binding 对应 file_bindings，会话与 file_ref 的唯一绑定
type Binding struct {
	ID        int64   `gorm:"column:id;primaryKey"`
	FileRef   string  `gorm:"column:file_ref;not null;index"`
	SessionID string  `gorm:"column:session_id;not null;index"`
	MessageID string  `gorm:"column:message_id;not null;default:''"`
	CreatedAt float64 `gorm:"column:created_at;not null"`
}

func (Binding) TableName() string { return "file_bindings" }

// Parse 对应 file_parses，记录文本解析结果
type Parse struct {
	FileRef       string  `gorm:"column:file_ref;primaryKey"`
	ParserVersion string  `gorm:"column:parser_version;not null;default:'text-v1'"`
	Encoding      string  `gorm:"column:encoding;not null;default:''"`
	State         string  `gorm:"column:state;not null;default:'pending'"`
	Error         string  `gorm:"column:error;not null;default:''"`
	UpdatedAt     float64 `gorm:"column:updated_at;not null"`
}

func (Parse) TableName() string { return "file_parses" }
