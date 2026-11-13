package webuiapi

import (
	_ "embed"
	"net/http"
	"sync"

	"github.com/bytedance/sonic"
)

//go:embed assets/config_schema.json
var configSchemaAsset []byte

type configSchemaPayload struct {
	Bot      map[string]any            `json:"bot"`
	Model    map[string]any            `json:"model"`
	Sections map[string]map[string]any `json:"sections"`
}

var (
	configSchemaOnce  sync.Once
	configSchemaData  configSchemaPayload
	configSchemaError error
)

func loadConfigSchema() (configSchemaPayload, error) {
	configSchemaOnce.Do(func() {
		configSchemaError = sonic.Unmarshal(configSchemaAsset, &configSchemaData)
	})
	return configSchemaData, configSchemaError
}

func writeConfigSchema(w http.ResponseWriter, schema any, message string) {
	if schema == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "schema": schema})
}

// handleConfigSchemaBot 对应 GET /api/webui/config/schema/bot
// 架构数据由 ../MaiBot 的 ConfigSchemaGenerator 导出为 assets/config_schema.json，
// 避免在 Go 侧重复维护 Pydantic 字段元数据
func (d Deps) handleConfigSchemaBot(w http.ResponseWriter, r *http.Request) {
	schema, err := loadConfigSchema()
	if err != nil {
		writeConfigSchema(w, nil, "获取配置架构失败: "+err.Error())
		return
	}
	writeConfigSchema(w, schema.Bot, "")
}

// handleConfigCompatSchema 兼容 GET /api/config/schema
func (d Deps) handleConfigCompatSchema(w http.ResponseWriter, r *http.Request) {
	d.handleConfigSchemaBot(w, r)
}

// handleConfigSchemaModel 对应 GET /api/webui/config/schema/model
func (d Deps) handleConfigSchemaModel(w http.ResponseWriter, r *http.Request) {
	schema, err := loadConfigSchema()
	if err != nil {
		writeConfigSchema(w, nil, "获取模型配置架构失败: "+err.Error())
		return
	}
	writeConfigSchema(w, schema.Model, "")
}

// handleConfigSchemaSection 对应 GET /api/webui/config/schema/section/{section_name}
func (d Deps) handleConfigSchemaSection(w http.ResponseWriter, r *http.Request) {
	sectionName := chiURLParam(r, "section_name")
	schema, err := loadConfigSchema()
	if err != nil {
		writeConfigSchema(w, nil, "获取配置节架构失败: "+err.Error())
		return
	}
	section, ok := schema.Sections[sectionName]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置节 '" + sectionName + "' 不存在"})
		return
	}
	writeConfigSchema(w, section, "")
}
