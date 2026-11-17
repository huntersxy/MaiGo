package desktoppet

// 应用条件的确定性检测，对齐 desktop_pet/app_conditions.py。

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// runningApplications 读取本机进程名与窗口标题
func runningApplications() ([]map[string]string, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("应用打开检测目前仅支持 Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; $ErrorActionPreference='Stop'; "+
			"@(Get-Process | Where-Object { $_.Id -ne $PID } | Select-Object ProcessName,MainWindowTitle) | ConvertTo-Json -Compress")
	hideConsoleWindow(command)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(string(output), "\ufeff")
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, err
	}
	list, ok := raw.([]any)
	if !ok {
		if single, singleOK := raw.(map[string]any); singleOK {
			list = []any{single}
		} else {
			return nil, fmt.Errorf("应用列表格式无效")
		}
	}
	applications := make([]map[string]string, 0, len(list))
	for _, item := range list {
		mapping, ok := item.(map[string]any)
		if !ok {
			continue
		}
		applications = append(applications, map[string]string{
			"process": asString(mapping["ProcessName"]),
			"title":   asString(mapping["MainWindowTitle"]),
		})
	}
	return applications, nil
}

// matchingApplication 返回匹配应用证据，未命中返回空串
func matchingApplication(query string, applications []map[string]string) string {
	keyword := normalizeApplicationText(query)
	if keyword == "" {
		return ""
	}
	for _, application := range applications {
		process := normalizeApplicationText(application["process"])
		title := normalizeApplicationText(application["title"])
		if strings.Contains(process, keyword) || strings.Contains(title, keyword) {
			evidence := fmt.Sprintf("检测到进程 %s；窗口标题：%s", application["process"], application["title"])
			if len([]rune(evidence)) > 1000 {
				return string([]rune(evidence)[:1000])
			}
			return evidence
		}
	}
	return ""
}

func normalizeApplicationText(value string) string {
	normalized := strings.TrimSpace(norm.NFKC.String(value))
	normalized = strings.ToLower(normalized)
	return strings.TrimSuffix(normalized, ".exe")
}
