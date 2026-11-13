package scripts

// 脚本共用内核装配与 Python 兼容辅助
// 迁移脚本按 Python SDKMemoryKernel(plugin_root, config) 的最小配置装配原生内核

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite"

	"maibot-go/internal/A_memorix"
	"maibot-go/internal/A_memorix/core/runtime/native"
	"maibot-go/internal/A_memorix/core/utils"
)

// previewLimit 对齐 Python 迁移脚本 dry-run 只预览前 5 行
const previewLimit = 5

// newScriptKernel 对齐 SDKMemoryKernel(plugin_root=PLUGIN_ROOT, config={"storage": {"data_dir": ...}})
func newScriptKernel(dataDir string, logger utils.Logger) (*native.Runtime, error) {
	if logger == nil {
		logger = utils.NopLogger{}
	}
	return native.Open(native.Options{
		PluginRoot: memorix.PackageRoot(),
		Config:     map[string]any{"storage": map[string]any{"data_dir": dataDir}},
		Logger:     logger,
	})
}

// shutdownScriptKernel 对齐 finally 中的 await kernel.shutdown()，关闭错误只记日志
func shutdownScriptKernel(ctx context.Context, runtime *native.Runtime, logger utils.Logger) {
	if runtime == nil {
		return
	}
	if err := runtime.Shutdown(ctx); err != nil && logger != nil {
		logger.Warning(fmt.Sprintf("内核关闭失败: %v", err))
	}
}

// openMaiBotSQLite 打开 MaiBot SQLite，对齐 sqlite3.connect 的单连接读取
func openMaiBotSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// pythonTimestamp 对齐 _to_timestamp：数字直接转 float，字符串按 fromisoformat 解析，失败返回 nil
func pythonTimestamp(value any) *float64 {
	switch typed := value.(type) {
	case nil:
		return nil
	case int64:
		out := float64(typed)
		return &out
	case float64:
		out := typed
		return &out
	case []byte:
		return pythonTimestamp(string(typed))
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
		out := parsePythonISOTimestamp(text)
		return out
	default:
		text := strings.TrimSpace(pyString(typed))
		if text == "" {
			return nil
		}
		return parsePythonISOTimestamp(text)
	}
}

// pythonISOLayouts 覆盖 datetime.fromisoformat 的常见输入形态
var pythonISOLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02",
}

// parsePythonISOTimestamp 解析时间戳；无时区输入按本地时区解释，对齐 Python 行为
func parsePythonISOTimestamp(text string) *float64 {
	for _, layout := range pythonISOLayouts {
		var parsed time.Time
		var err error
		if strings.Contains(layout, "Z07") || strings.Contains(layout, "-07:00") {
			parsed, err = time.Parse(layout, text)
		} else {
			parsed, err = time.ParseInLocation(layout, text, time.Local)
		}
		if err == nil {
			out := float64(parsed.UnixNano()) / 1e9
			return &out
		}
	}
	return nil
}

// storedIDsNonEmpty 判断 ingest 结果的 stored_ids 是否非空
func storedIDsNonEmpty(result map[string]any) bool {
	value, ok := result["stored_ids"]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case []string:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	default:
		return isTruthy(value)
	}
}

// parseJSONStringList 对齐 json.loads 后逐项 str() 的用法，空值返回空切片
func parseJSONStringList(raw sql.NullString) ([]string, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var values []any
	if err := UnmarshalJSON([]byte(raw.String), &values); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, pyString(value))
	}
	return out, nil
}

// printJSONLine 对齐 print(json.dumps(value, ensure_ascii=False))
// 差异说明：Python 保留字典插入顺序，这里按键名排序
func printJSONLine(ctx context.Context, value any) error {
	text, err := utils.PyJSONMarshal(value)
	if err != nil {
		return err
	}
	scriptPrintln(ctx, text)
	return nil
}

// humanLimit 对齐 int(args.limit or 0)，负数在 SQL 中按无限制处理
func humanLimit(value int) int {
	if value > 0 {
		return value
	}
	return 0
}

// itoa 便于拼接 external_id
func itoa(value int64) string { return strconv.FormatInt(value, 10) }
