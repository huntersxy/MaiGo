package webuiapi

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

// bodySpecsJSON 由 agents 侧生成脚本从 MaiBot 1.4.4 真实 FastAPI 路由导出
//
//go:embed data/body_specs/body_specs.json
var bodySpecsJSON []byte

type bodySpecsFile struct {
	Source struct {
		MaiBotRoot    string `json:"maibot_root"`
		MaiBotVersion string `json:"maibot_version"`
		FastAPI       string `json:"fastapi"`
		Pydantic      string `json:"pydantic"`
	} `json:"source"`
	Routes []bodyRouteSpec `json:"routes"`
}

// bodyRouteSpec 描述一个请求体参数校验规格；schema 是 Pydantic model_json_schema 的子集
// x-order 由生成脚本写入，用来保持 Pydantic 的字段声明顺序

type bodyRouteSpec struct {
	Method   string         `json:"method"`
	Path     string         `json:"path"`
	Required bool           `json:"required"`
	Schema   map[string]any `json:"schema"`
}

type bodySpecRegistry struct {
	routes   map[string]bodyRouteSpec
	patterns []bodyRoutePattern
}

// bodyRoutePattern 保存含路径参数的 Python 路由，按实际请求路径匹配
type bodyRoutePattern struct {
	method   string
	segments []string
	spec     bodyRouteSpec
}

var (
	bodySpecOnce sync.Once
	bodySpecReg  bodySpecRegistry
)

func loadBodySpecRegistry() bodySpecRegistry {
	bodySpecOnce.Do(func() {
		var payload bodySpecsFile
		if err := json.Unmarshal(bodySpecsJSON, &payload); err != nil {
			panic("解析请求体校验规格失败: " + err.Error())
		}
		registry := bodySpecRegistry{routes: make(map[string]bodyRouteSpec, len(payload.Routes))}
		for _, route := range payload.Routes {
			normalized := normalizeBodyRoutePath(route.Path)
			key := strings.ToUpper(route.Method) + " " + normalized
			registry.routes[key] = route
			if strings.Contains(normalized, "{}") {
				registry.patterns = append(registry.patterns, bodyRoutePattern{
					method:   strings.ToUpper(route.Method),
					segments: strings.Split(strings.TrimPrefix(normalized, "/"), "/"),
					spec:     route,
				})
			}
		}
		bodySpecReg = registry
	})
	return bodySpecReg
}

// lookupBodyRouteForRequest 按 HTTP 方法与实际 URL 路径查找 Python 侧对应请求体规格
func lookupBodyRouteForRequest(method, requestPath string) (bodyRouteSpec, bool) {
	registry := loadBodySpecRegistry()
	method = strings.ToUpper(method)
	normalized := normalizeBodyRoutePath(requestPath)
	if spec, ok := registry.routes[method+" "+normalized]; ok {
		return spec, true
	}
	segments := strings.Split(strings.TrimPrefix(normalized, "/"), "/")
	for _, pattern := range registry.patterns {
		if pattern.method != method || len(pattern.segments) != len(segments) {
			continue
		}
		matched := true
		for index, expected := range pattern.segments {
			if expected == "{}" {
				if segments[index] == "" {
					matched = false
					break
				}
				continue
			}
			if expected != segments[index] {
				matched = false
				break
			}
		}
		if matched {
			return pattern.spec, true
		}
	}
	return bodyRouteSpec{}, false
}

// normalizeBodyRoutePath 统一尾斜杠与路径参数名，便于 Python/Go 路由对照
func normalizeBodyRoutePath(path string) string {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			parts[i] = "{}"
		}
	}
	normalized := strings.Join(parts, "/")
	if normalized == "" {
		return "/"
	}
	return normalized
}
