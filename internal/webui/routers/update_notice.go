package webuiapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"MaiGo/internal/common/self_update"
	appversion "MaiGo/internal/common/version"

	"go.uber.org/zap"
)

// 本文件对齐 ../MaiBot/src/common/update_notice.py：
// changelog 版本块解析、版本比较、WebUI 更新公告状态与历史记录

type changelogEntry struct {
	Version  string
	Title    string
	Markdown string
}

type updateNotice struct {
	CurrentVersion string
	FromVersion    string
	Versions       []string
	Content        string
}

const (
	terminalNoticeRepeatCount = 3
	terminalNotifiedKey       = "terminal_notified_version"
	terminalNoticeVersionKey  = "terminal_notice_version"
	terminalNoticeCountKey    = "terminal_notice_count"
)

var (
	changelogVersionHeading = regexp.MustCompile("(?m)^# \\[([^\\]]+)\\]([^\\n]*)$")
	changelogSectionHeading = regexp.MustCompile("(?m)^#{2,6}\\s+(.+?)\\s*$")
	updateNoticeDigits      = regexp.MustCompile("\\d+")
	updateNoticePreSplit    = regexp.MustCompile("[.\\-_]")
)

func updateNoticeRoot(instanceDir string) (string, error) {
	dir := strings.TrimSpace(instanceDir)
	if dir == "" {
		return "", errors.New("需要设置 MAIBOT_INSTANCE_DIR 指向 MaiBot 实例根目录")
	}
	return dir, nil
}

func changelogPath(instanceDir string) string {
	return filepath.Join(instanceDir, "changelogs", "changelog.md")
}

func updateNoticeStatePath(instanceDir string) string {
	return filepath.Join(instanceDir, "data", "update_notice_state.json")
}

func normalizeVersion(version string) string {
	normalized := strings.TrimSpace(version)
	if strings.HasPrefix(strings.ToLower(normalized), "v") {
		normalized = normalized[1:]
	}
	return normalized
}

type versionKey struct {
	core       []int
	prerelease int
	preParts   []versionPrePart
}

type versionPrePart struct {
	kind int
	num  int
	text string
}

func parseVersionKey(version string) versionKey {
	normalized := normalizeVersion(version)
	core := normalized
	separator := false
	prerelease := ""
	if index := strings.Index(normalized, "-"); index >= 0 {
		core = normalized[:index]
		separator = true
		prerelease = normalized[index+1:]
	}
	key := versionKey{}
	for _, part := range updateNoticeDigits.FindAllString(core, -1) {
		if value, err := strconv.Atoi(part); err == nil {
			key.core = append(key.core, value)
		}
	}
	if !separator {
		key.prerelease = 1
	}
	for _, part := range updateNoticePreSplit.Split(prerelease, -1) {
		if part == "" {
			continue
		}
		if value, err := strconv.Atoi(part); err == nil {
			key.preParts = append(key.preParts, versionPrePart{kind: 0, num: value})
		} else {
			key.preParts = append(key.preParts, versionPrePart{kind: 1, text: strings.ToLower(part)})
		}
	}
	return key
}

func compareVersionKeys(left, right versionKey) int {
	// 对齐 Python 元组比较：公共前缀相等时，短元组更小
	length := len(left.core)
	if len(right.core) < length {
		length = len(right.core)
	}
	for index := 0; index < length; index++ {
		leftValue, rightValue := left.core[index], right.core[index]
		if leftValue != rightValue {
			if leftValue < rightValue {
				return -1
			}
			return 1
		}
	}
	if len(left.core) != len(right.core) {
		if len(left.core) < len(right.core) {
			return -1
		}
		return 1
	}
	if left.prerelease != right.prerelease {
		if left.prerelease < right.prerelease {
			return -1
		}
		return 1
	}
	length = len(left.preParts)
	if len(right.preParts) > length {
		length = len(right.preParts)
	}
	for index := 0; index < length; index++ {
		if index >= len(left.preParts) {
			return -1
		}
		if index >= len(right.preParts) {
			return 1
		}
		leftPart, rightPart := left.preParts[index], right.preParts[index]
		if leftPart.kind != rightPart.kind {
			if leftPart.kind < rightPart.kind {
				return -1
			}
			return 1
		}
		if leftPart.kind == 0 {
			if leftPart.num != rightPart.num {
				if leftPart.num < rightPart.num {
					return -1
				}
				return 1
			}
			continue
		}
		if leftPart.text != rightPart.text {
			if leftPart.text < rightPart.text {
				return -1
			}
			return 1
		}
	}
	return 0
}

func isVersionNewer(left, right string) bool {
	return compareVersionKeys(parseVersionKey(left), parseVersionKey(right)) > 0
}

func isVersionInRange(version, fromVersion, currentVersion string) bool {
	return isVersionNewer(version, fromVersion) && !isVersionNewer(version, currentVersion)
}

func parseChangelogEntries(path string) []changelogEntry {
	raw, err := os.ReadFile(path)
	if err != nil {
		zap.L().Warn("读取更新日志失败", zap.String("path", path), zap.Error(err))
		return nil
	}
	text := string(raw)
	matches := changelogVersionHeading.FindAllStringSubmatchIndex(text, -1)
	entries := make([]changelogEntry, 0, len(matches))
	for index, match := range matches {
		start := match[0]
		end := len(text)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		block := strings.TrimSpace(text[start:end])
		if block == "" {
			continue
		}
		entries = append(entries, changelogEntry{
			Version:  strings.TrimSpace(text[match[2]:match[3]]),
			Title:    strings.TrimSpace(text[match[0]:match[1]]),
			Markdown: block,
		})
	}
	return entries
}

func buildUpdateNotice(fromVersion, currentVersion, changelogFile string) updateNotice {
	entries := parseChangelogEntries(changelogFile)
	selected := make([]changelogEntry, 0, len(entries))
	for _, entry := range entries {
		if isVersionInRange(entry.Version, fromVersion, currentVersion) {
			selected = append(selected, entry)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return isVersionNewer(selected[i].Version, selected[j].Version)
	})
	if len(selected) > 0 {
		blocks := make([]string, 0, len(selected))
		versions := make([]string, 0, len(selected))
		for _, entry := range selected {
			blocks = append(blocks, entry.Markdown)
			versions = append(versions, entry.Version)
		}
		return updateNotice{
			CurrentVersion: currentVersion,
			FromVersion:    fromVersion,
			Versions:       versions,
			Content:        "# 麦麦已从 v" + fromVersion + " 更新到 v" + currentVersion + "\n\n" + strings.Join(blocks, "\n\n"),
		}
	}
	return updateNotice{
		CurrentVersion: currentVersion,
		FromVersion:    fromVersion,
		Versions:       []string{},
		Content:        "# 麦麦已从 v" + fromVersion + " 更新到 v" + currentVersion + "\n\n未在 `changelogs/changelog.md` 中找到对应版本的更新日志条目。",
	}
}

func buildDebugUpdateNotice(currentVersion, changelogFile string) updateNotice {
	entries := make([]changelogEntry, 0)
	for _, entry := range parseChangelogEntries(changelogFile) {
		if !isVersionNewer(entry.Version, currentVersion) {
			entries = append(entries, entry)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return isVersionNewer(entries[i].Version, entries[j].Version)
	})
	if len(entries) == 0 {
		return updateNotice{
			CurrentVersion: currentVersion,
			FromVersion:    "0.0.0",
			Versions:       []string{},
			Content:        "# 当前 MaiBot 版本 v" + currentVersion + "\n\n未找到可展示的更新日志条目。",
		}
	}
	fromVersion := "0.0.0"
	if len(entries) > 1 {
		fromVersion = entries[1].Version
	}
	return updateNotice{
		CurrentVersion: currentVersion,
		FromVersion:    fromVersion,
		Versions:       []string{entries[0].Version},
		Content:        "# 当前 MaiBot 版本 v" + currentVersion + "\n\n" + entries[0].Markdown,
	}
}

type updateNoticeState map[string]any

func readUpdateNoticeState(path string) updateNoticeState {
	raw, err := os.ReadFile(path)
	if err != nil {
		return updateNoticeState{}
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return updateNoticeState{}
	}
	state := updateNoticeState{}
	for key, value := range data {
		switch value.(type) {
		case string, float64, bool:
			// bool 对齐 Python：isinstance(value, (str, int)) 中 bool 属 int
			state[key] = value
		}
	}
	return state
}

func writeUpdateNoticeState(path string, state updateNoticeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

func updateNoticeStateText(state updateNoticeState, key string) string {
	value, ok := state[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// updateNoticeStateInt 读取整数状态；对齐 Python _get_state_int 只认整数与整数字符串
//
// JSON 数字统一解成 float64，这里要求无小数部分才接受（3.7 回落默认值，与 Python 一致）。
func updateNoticeStateInt(state updateNoticeState, key string, fallback int) int {
	switch value := state[key].(type) {
	case int:
		return value
	case float64:
		if value == float64(int64(value)) {
			return int(value)
		}
	case string:
		if number, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return number
		}
	}
	return fallback
}

func initializeUpdateNoticeState(path, currentVersion string) (updateNoticeState, bool, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, false, nil
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	state := updateNoticeState{
		"terminal_notified_version": currentVersion,
		"terminal_notice_version":   currentVersion,
		"terminal_notice_count":     3,
		"webui_ack_version":         currentVersion,
	}
	if err := writeUpdateNoticeState(path, state); err != nil {
		return nil, false, err
	}
	return state, true, nil
}

func getPendingUpdateNotice(instanceDir, currentVersion string) (*updateNotice, error) {
	currentVersion = appversion.Resolve(currentVersion)
	statePath := updateNoticeStatePath(instanceDir)
	if _, initialized, err := initializeUpdateNoticeState(statePath, currentVersion); err != nil {
		return nil, err
	} else if initialized {
		return nil, nil
	}
	state := readUpdateNoticeState(statePath)
	fromVersion := updateNoticeStateText(state, "webui_ack_version")
	if fromVersion == "" {
		state["webui_ack_version"] = currentVersion
		if err := writeUpdateNoticeState(statePath, state); err != nil {
			return nil, err
		}
		return nil, nil
	}
	// 旧 Go 版本可能把 dev 写入状态，dev 视为未知起点：不生成公告也不静默改写用户文件
	// 只有显式 ack 才把真实版本写回 webui_ack_version
	if appversion.IsUnknown(fromVersion) {
		return nil, nil
	}
	if !isVersionNewer(currentVersion, fromVersion) {
		return nil, nil
	}
	notice := buildUpdateNotice(fromVersion, currentVersion, changelogPath(instanceDir))
	return &notice, nil
}

func markUpdateNoticeSeen(instanceDir, currentVersion string) error {
	currentVersion = appversion.Resolve(currentVersion)
	statePath := updateNoticeStatePath(instanceDir)
	state := readUpdateNoticeState(statePath)
	state["webui_ack_version"] = currentVersion
	return writeUpdateNoticeState(statePath, state)
}

// getPendingTerminalUpdateNotice 对齐 Python get_pending_update_notice("terminal")
func getPendingTerminalUpdateNotice(instanceDir, currentVersion string) (*updateNotice, error) {
	currentVersion = appversion.Resolve(currentVersion)
	statePath := updateNoticeStatePath(instanceDir)
	if _, initialized, err := initializeUpdateNoticeState(statePath, currentVersion); err != nil {
		return nil, err
	} else if initialized {
		return nil, nil
	}
	state := readUpdateNoticeState(statePath)
	completedVersion := updateNoticeStateText(state, terminalNotifiedKey)
	if completedVersion == "" {
		state[terminalNotifiedKey] = currentVersion
		state[terminalNoticeVersionKey] = currentVersion
		state[terminalNoticeCountKey] = terminalNoticeRepeatCount
		if err := writeUpdateNoticeState(statePath, state); err != nil {
			return nil, err
		}
		return nil, nil
	}
	// dev 是旧版本写入的未知占位值，视为已完成但不改写状态，避免误报从 dev 升级
	if appversion.IsUnknown(completedVersion) {
		return nil, nil
	}
	if !isVersionNewer(currentVersion, completedVersion) {
		return nil, nil
	}
	noticeVersion := updateNoticeStateText(state, terminalNoticeVersionKey)
	noticeCount := updateNoticeStateInt(state, terminalNoticeCountKey, 0)
	if noticeVersion != currentVersion {
		noticeCount = 0
	}
	if noticeCount >= terminalNoticeRepeatCount {
		state[terminalNotifiedKey] = currentVersion
		if err := writeUpdateNoticeState(statePath, state); err != nil {
			return nil, err
		}
		return nil, nil
	}
	notice := buildUpdateNotice(completedVersion, currentVersion, changelogPath(instanceDir))
	return &notice, nil
}

// markTerminalUpdateNoticeSeen 对齐 Python mark_update_notice_seen("terminal")
func markTerminalUpdateNoticeSeen(instanceDir, currentVersion string) error {
	currentVersion = appversion.Resolve(currentVersion)
	statePath := updateNoticeStatePath(instanceDir)
	state := readUpdateNoticeState(statePath)
	noticeVersion := updateNoticeStateText(state, terminalNoticeVersionKey)
	noticeCount := updateNoticeStateInt(state, terminalNoticeCountKey, 0)
	if noticeVersion != currentVersion {
		noticeCount = 0
	}
	noticeCount++
	state[terminalNoticeVersionKey] = currentVersion
	state[terminalNoticeCountKey] = noticeCount
	if noticeCount >= terminalNoticeRepeatCount {
		state[terminalNotifiedKey] = currentVersion
	}
	return writeUpdateNoticeState(statePath, state)
}

// WireTerminalUpdateNotice 在启动阶段调用一次，输出终端更新公告
// 建议调用点：实例目录与构建版本就绪后、进入事件循环前
func WireTerminalUpdateNotice(instanceDir, currentVersion string) {
	wireTerminalUpdateNotice(context.Background(), instanceDir, currentVersion, "", nil)
}

// WireTerminalUpdateNoticeWithPlugins 在输出终端公告后检查并提示不兼容插件
// pluginsDir 为空时跳过插件检查；loader 为空时只检查本地清单
func WireTerminalUpdateNoticeWithPlugins(instanceDir, currentVersion, pluginsDir string, loader selfupdate.MarketplaceLoader) {
	wireTerminalUpdateNotice(context.Background(), instanceDir, currentVersion, pluginsDir, loader)
}

func wireTerminalUpdateNotice(ctx context.Context, instanceDir, currentVersion, pluginsDir string, loader selfupdate.MarketplaceLoader) {
	root, err := updateNoticeRoot(instanceDir)
	if err != nil {
		zap.L().Warn("读取终端更新公告失败", zap.Error(err))
		return
	}
	notice, err := getPendingTerminalUpdateNotice(root, currentVersion)
	if err != nil {
		zap.L().Warn("读取终端更新公告失败", zap.Error(err))
		return
	}
	if notice == nil {
		return
	}
	separator := strings.Repeat("=", 48)
	zap.L().Warn("更新公告\n" + separator + "\n" + notice.Content + "\n" + separator)
	if strings.TrimSpace(pluginsDir) != "" {
		notices, err := selfupdate.CollectUpdateIncompatiblePlugins(
			ctx, notice.FromVersion, notice.CurrentVersion, pluginsDir, loader,
		)
		if err != nil {
			zap.L().Warn("检查插件兼容性失败", zap.Error(err))
		} else if len(notices) > 0 {
			zap.L().Warn(selfupdate.FormatTerminalCompatibilityNotice(notice.FromVersion, notice.CurrentVersion, notices))
		}
	}
	if err := markTerminalUpdateNoticeSeen(root, notice.CurrentVersion); err != nil {
		zap.L().Warn("写入终端更新公告状态失败", zap.Error(err))
	}
}

func getChangelogHistory(instanceDir, currentVersion string, offset, limit int, beforeVersion, section string) []changelogEntry {
	entries := parseChangelogEntries(changelogPath(instanceDir))
	filtered := make([]changelogEntry, 0, len(entries))
	for _, entry := range entries {
		if isVersionNewer(entry.Version, currentVersion) {
			continue
		}
		if beforeVersion != "" && !isVersionNewer(beforeVersion, entry.Version) {
			continue
		}
		if section != "" {
			matched := false
			for _, match := range changelogSectionHeading.FindAllStringSubmatch(entry.Markdown, -1) {
				if strings.Contains(strings.ToLower(match[1]), strings.ToLower(section)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return isVersionNewer(filtered[i].Version, filtered[j].Version)
	})
	if offset >= len(filtered) {
		return nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end]
}
