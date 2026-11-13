package capabilities

import (
	"context"
	"fmt"
	"strings"
)

func makeDatabaseQueryHandler(port PluginDatabasePort) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		modelName := strings.TrimSpace(coalesceString(args, "model_name"))
		if modelName == "" {
			return capabilityFailure("缺少必要参数 model_name"), nil
		}
		queryType := strings.TrimSpace(coalesceString(args, "query_type"))
		if queryType == "" {
			queryType = "get"
		}
		if port == nil {
			return pluginPortNotWired("database.query"), nil
		}
		switch queryType {
		case "get":
			return port.Get(ctx, pluginID, modelName, PluginDatabaseQuery{
				Filters:      payloadMap(args, "filters"),
				Limit:        int64(coalesceFloat(args, "limit", 0)),
				OrderBy:      databaseOrderByArg(args),
				SingleResult: truthyPayloadValue(args["single_result"]),
			})
		case "create":
			data := payloadMap(args, "data")
			if len(data) == 0 {
				return capabilityFailure("create 需要 data"), nil
			}
			return port.Save(ctx, pluginID, modelName, data, "", "")
		case "update":
			data := payloadMap(args, "data")
			if len(data) == 0 {
				return capabilityFailure("update 需要 data"), nil
			}
			return port.Update(ctx, pluginID, modelName, data, payloadMap(args, "filters"))
		case "delete":
			return port.Delete(ctx, pluginID, modelName, payloadMap(args, "filters"))
		case "count":
			return port.Count(ctx, pluginID, modelName, payloadMap(args, "filters"))
		default:
			return capabilityFailure("不支持的 query_type: %s", queryType), nil
		}
	}
}

func makeDatabaseSaveHandler(port PluginDatabasePort) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		modelName := strings.TrimSpace(coalesceString(args, "model_name"))
		data := payloadMap(args, "data")
		if modelName == "" || len(data) == 0 {
			return capabilityFailure("缺少必要参数 model_name 或 data"), nil
		}
		if port == nil {
			return pluginPortNotWired("database.save"), nil
		}
		return port.Save(ctx, pluginID, modelName, data, coalesceString(args, "key_field"), coalesceString(args, "key_value"))
	}
}

func makeDatabaseGetHandler(port PluginDatabasePort) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		modelName := strings.TrimSpace(coalesceString(args, "model_name"))
		if modelName == "" {
			return capabilityFailure("缺少必要参数 model_name"), nil
		}
		if port == nil {
			return pluginPortNotWired("database.get"), nil
		}
		return port.Get(ctx, pluginID, modelName, PluginDatabaseQuery{
			Filters:      payloadMap(args, "filters"),
			Limit:        int64(coalesceFloat(args, "limit", 0)),
			OrderBy:      databaseOrderByArg(args),
			SingleResult: truthyPayloadValue(args["single_result"]),
		})
	}
}

func makeDatabaseDeleteHandler(port PluginDatabasePort) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		modelName := strings.TrimSpace(coalesceString(args, "model_name"))
		filters := payloadMap(args, "filters")
		if modelName == "" {
			return capabilityFailure("缺少必要参数 model_name"), nil
		}
		if len(filters) == 0 {
			return capabilityFailure("缺少必要参数 filters（不允许无条件删除）"), nil
		}
		if port == nil {
			return pluginPortNotWired("database.delete"), nil
		}
		return port.Delete(ctx, pluginID, modelName, filters)
	}
}

func databaseOrderByArg(args map[string]any) string {
	raw, exists := args["order_by"]
	if !exists || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []string:
		return strings.Join(value, ",")
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				items = append(items, text)
			}
		}
		return strings.Join(items, ",")
	default:
		return ""
	}
}

func makeDatabaseCountHandler(port PluginDatabasePort) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		modelName := strings.TrimSpace(coalesceString(args, "model_name"))
		if modelName == "" {
			return capabilityFailure("缺少必要参数 model_name"), nil
		}
		if port == nil {
			return pluginPortNotWired("database.count"), nil
		}
		return port.Count(ctx, pluginID, modelName, payloadMap(args, "filters"))
	}
}
