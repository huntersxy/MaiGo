package capabilities

import (
	"context"
	"fmt"

	"maibot-go/internal/files"
)

// makeFilesCapabilityHandler 把 files.* 能力转交给 files 模块
//
// 权限判定与业务实现都在 files.FileCapability（对应 MaiBot src/files/capability.py），
// 这里只负责把结果包成插件运行时的 {"success","result"} 载荷。
func makeFilesCapabilityHandler(svc *filesvc.Service) CapabilityHandler {
	return func(ctx context.Context, pluginID, capability string, args map[string]any) (any, error) {
		if svc == nil {
			return nil, fmt.Errorf("文件服务未就绪")
		}
		result, err := svc.FileCapability(ctx, pluginID, capability, args)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "result": result}, nil
	}
}
