package services

// 知识包载入、导入与卸载，对齐 bundle_admin_service.py 的 load/import/uninstall 段

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
)

// loadedBundle 已校验的记忆包内容
type loadedBundle struct {
	Path      string
	Size      int64
	Manifest  map[string]any
	Knowledge map[string]any
	State     map[string]any
	Images    map[string]any
	Members   map[string]bool
	// Checksums 保存成员 sha256，供按需读取资产时复核
	Checksums map[string]string
}

// hasMember 判断成员是否存在
func (l *loadedBundle) hasMember(name string) bool {
	return l != nil && l.Members[name]
}

// loadBundle 对齐 _load_bundle 的校验链
func (s *BundleAdminService) loadBundle(path string) (*loadedBundle, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("记忆包不是有效的 ZIP 文件")
	}
	defer reader.Close()
	if len(reader.File) > MaxBundleMembers {
		return nil, fmt.Errorf("记忆包文件项过多")
	}
	members := map[string]bool{}
	raw := map[string][]byte{}
	var total int64
	for _, entry := range reader.File {
		name := entry.Name
		// 目录项与 Python 一致计入成员集合，由 checksums 覆盖校验拒绝
		if entry.UncompressedSize64 > MaxBundleMemberBytes {
			return nil, fmt.Errorf("记忆包单个文件解压后超过 256 MiB 限制")
		}
		total += int64(entry.UncompressedSize64)
		if total > MaxBundleUncompressedBytes {
			return nil, fmt.Errorf("记忆包解压后超过 1 GiB 限制")
		}
		if members[name] {
			return nil, fmt.Errorf("记忆包包含重复文件项")
		}
		if !safeBundleMemberName(name) {
			return nil, fmt.Errorf("记忆包包含非法文件路径: %s", name)
		}
		members[name] = true
		content, readErr := s.readBundleMember(entry)
		if readErr != nil {
			return nil, readErr
		}
		raw[name] = content
	}
	for _, required := range []string{"manifest.json", "knowledge.json", "checksums.json"} {
		if !members[required] {
			return nil, fmt.Errorf("记忆包缺少 manifest.json、knowledge.json 或 checksums.json")
		}
	}
	manifest, err := parseBundleJSON(raw["manifest.json"])
	if err != nil {
		return nil, fmt.Errorf("记忆包中的 JSON 无法解析")
	}
	knowledge, err := parseBundleJSON(raw["knowledge.json"])
	if err != nil {
		return nil, fmt.Errorf("记忆包中的 JSON 无法解析")
	}
	checksums, err := parseBundleJSON(raw["checksums.json"])
	if err != nil {
		return nil, fmt.Errorf("记忆包中的 JSON 无法解析")
	}
	if asString(manifest["format"]) != BundleFormat {
		return nil, fmt.Errorf("不是 A_Memorix 记忆包")
	}
	version, versionLabel, versionOK := bundleFormatVersion(manifest)
	if !versionOK || (version != BundleLegacyFormatVersion && version != BundleFormatVersion) {
		return nil, fmt.Errorf("暂不支持记忆包版本: %s", versionLabel)
	}
	if version == BundleLegacyFormatVersion && members["images.json"] {
		return nil, fmt.Errorf("v1 记忆包不能包含图片扩展")
	}
	if version == BundleFormatVersion && !members["images.json"] {
		return nil, fmt.Errorf("v2 记忆包缺少 images.json")
	}
	docs, ok := knowledge["docs"].([]any)
	if !ok {
		return nil, fmt.Errorf("knowledge.json 缺少 docs 数组")
	}
	_ = docs
	contentLevel := strings.ToLower(strings.TrimSpace(asString(manifest["content_level"])))
	if contentLevel != "knowledge" && contentLevel != "full" {
		return nil, fmt.Errorf("manifest.content_level 无效")
	}
	var state map[string]any
	if contentLevel == "full" {
		if !members["state.json"] {
			return nil, fmt.Errorf("完整记忆包缺少 state.json")
		}
		state, err = parseBundleJSON(raw["state.json"])
		if err != nil {
			return nil, fmt.Errorf("记忆包中的 JSON 无法解析")
		}
	}
	if strings.ToLower(strings.TrimSpace(asString(checksums["algorithm"]))) != "sha256" {
		return nil, fmt.Errorf("checksums.json 格式无效")
	}
	declaredRaw, ok := checksums["files"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("checksums.json 格式无效")
	}
	declared := map[string]string{}
	for name, value := range declaredRaw {
		declared[name] = asString(value)
	}
	expected := map[string]bool{}
	for name := range members {
		if name == "checksums.json" {
			continue
		}
		expected[name] = true
	}
	missing := []string{}
	unknown := []string{}
	for name := range expected {
		if _, ok := declared[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range declared {
		if !expected[name] {
			unknown = append(unknown, name)
		}
	}
	if len(missing) > 0 || len(unknown) > 0 {
		sort.Strings(missing)
		sort.Strings(unknown)
		parts := []string{}
		if len(missing) > 0 {
			parts = append(parts, "缺少="+strings.Join(missing, ","))
		}
		if len(unknown) > 0 {
			parts = append(parts, "未知="+strings.Join(unknown, ","))
		}
		return nil, fmt.Errorf("checksums.json 未完整覆盖记忆包成员: %s", strings.Join(parts, "; "))
	}
	for name := range expected {
		sum := sha256.Sum256(raw[name])
		if declared[name] != hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("记忆包校验失败: %s", name)
		}
	}
	images := map[string]any{}
	if members["images.json"] {
		images, err = parseBundleJSON(raw["images.json"])
		if err != nil {
			return nil, fmt.Errorf("记忆包中的 JSON 无法解析")
		}
		declaredAssetMembers := map[string]bool{}
		assetRaw, hasAssets := images["assets"]
		if hasAssets && assetRaw != nil {
			assets, ok := assetRaw.([]any)
			if !ok {
				return nil, fmt.Errorf("images.json 包含无效资产记录")
			}
			for _, item := range assets {
				asset, ok := item.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("images.json 包含无效资产记录")
				}
				memberName := asString(asset["member"])
				contentHash := asString(asset["content_hash"])
				if declaredAssetMembers[memberName] || !members[memberName] {
					return nil, fmt.Errorf("图片资产成员缺失或被重复声明")
				}
				declaredAssetMembers[memberName] = true
				if !strings.HasPrefix(memberName, "images/assets/") {
					return nil, fmt.Errorf("图片资产不在 images/assets 目录")
				}
				if declared[memberName] != contentHash {
					return nil, fmt.Errorf("图片资产内容哈希不一致: %s", memberName)
				}
			}
		}
		actualAssetMembers := map[string]bool{}
		for name := range members {
			if strings.HasPrefix(name, "images/assets/") {
				actualAssetMembers[name] = true
			}
		}
		if len(actualAssetMembers) != len(declaredAssetMembers) {
			return nil, fmt.Errorf("记忆包包含未声明或缺失的图片资产")
		}
		for name := range actualAssetMembers {
			if !declaredAssetMembers[name] {
				return nil, fmt.Errorf("记忆包包含未声明或缺失的图片资产")
			}
		}
	}
	digestPayload := map[string]any{"knowledge": knowledge, "state": nil}
	if state != nil {
		digestPayload["state"] = state
	}
	if members["images.json"] {
		digestPayload["images"] = canonicalImageSemantics(images)
	}
	digestBytes, err := pythonCanonicalJSON(digestPayload)
	if err != nil {
		return nil, err
	}
	if sha256Hex(digestBytes) != asString(manifest["content_digest"]) {
		return nil, fmt.Errorf("记忆包内容摘要不一致")
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return nil, statErr
	}
	return &loadedBundle{
		Path: path, Size: info.Size(), Manifest: manifest, Knowledge: knowledge,
		State: state, Images: images, Members: members, Checksums: declared,
	}, nil
}

// readValidatedBundleMember 按需读取已检查成员，并防止检查后文件被替换
func (s *BundleAdminService) readValidatedBundleMember(loaded *loadedBundle, memberName string) ([]byte, error) {
	if loaded == nil || !loaded.Members[memberName] {
		return nil, fmt.Errorf("记忆包缺少成员: %s", memberName)
	}
	reader, err := zip.OpenReader(loaded.Path)
	if err != nil {
		return nil, fmt.Errorf("记忆包在安装前已损坏")
	}
	defer reader.Close()
	names := map[string]bool{}
	content := []byte(nil)
	found := false
	for _, entry := range reader.File {
		if entry == nil {
			continue
		}
		if names[entry.Name] {
			return nil, fmt.Errorf("记忆包成员在校验期间发生变化")
		}
		names[entry.Name] = true
		if entry.Name != memberName {
			continue
		}
		if entry.UncompressedSize64 > MaxBundleMemberBytes {
			return nil, fmt.Errorf("记忆包成员已发生变化: %s", memberName)
		}
		content, err = s.readBundleMember(entry)
		if err != nil {
			return nil, err
		}
		found = true
	}
	if len(names) != len(loaded.Members) || !found {
		return nil, fmt.Errorf("记忆包成员已发生变化: %s", memberName)
	}
	for name := range loaded.Members {
		if !names[name] {
			return nil, fmt.Errorf("记忆包成员已发生变化: %s", memberName)
		}
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != loaded.Checksums[memberName] {
		return nil, fmt.Errorf("记忆包成员在检查后发生变化: %s", memberName)
	}
	return content, nil
}

// readBundleMember 逐块读取成员并校验长度
func (s *BundleAdminService) readBundleMember(entry *zip.File) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("记忆包在安装前已损坏")
	}
	defer reader.Close()
	buffer := &bytes.Buffer{}
	chunk := make([]byte, 1024*1024)
	var length int64
	for {
		n, readErr := reader.Read(chunk)
		if n > 0 {
			length += int64(n)
			if length > MaxBundleMemberBytes {
				return nil, fmt.Errorf("记忆包成员解压长度异常或超过限制: %s", entry.Name)
			}
			buffer.Write(chunk[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("记忆包在安装前已损坏")
		}
	}
	if entry.UncompressedSize64 > 0 && uint64(length) != entry.UncompressedSize64 {
		return nil, fmt.Errorf("记忆包成员解压长度异常: %s", entry.Name)
	}
	return buffer.Bytes(), nil
}

// bundleFormatVersion 对齐 Python int(manifest.get("format_version", 0) or 0)
// 返回解析值、原始值文案与是否可解析，供版本校验使用
func bundleFormatVersion(manifest map[string]any) (int, string, bool) {
	raw, exists := manifest["format_version"]
	if !exists || raw == nil {
		return 0, "None", true
	}
	switch typed := raw.(type) {
	case int64:
		return int(typed), fmt.Sprintf("%d", typed), true
	case int:
		return typed, fmt.Sprintf("%d", typed), true
	case float64:
		// Python 用原始值做错误文案，浮点按 repr 输出（3.0 而不是 3）
		label, err := pythonCanonicalJSON(typed)
		if err != nil {
			label = []byte(asString(typed))
		}
		return int(typed), string(label), true
	case bool:
		if typed {
			return 1, "True", true
		}
		return 0, "False", true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, typed, true
		}
		if parsed, err := strconv.Atoi(trimmed); err == nil {
			return parsed, typed, true
		}
		return 0, typed, false
	default:
		return 0, asString(raw), false
	}
}

// safeBundleMemberName 校验成员路径安全
func safeBundleMemberName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	// PurePosixPath 会折叠空段，尾斜杠目录项不视为非法路径，交由 checksums 覆盖校验拒绝
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// parseBundleJSON 解析 JSON 对象
func parseBundleJSON(payload []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	normalized, ok := normalizeBundleJSONValue(out).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("记忆包中的 JSON 不是对象")
	}
	return normalized, nil
}

// normalizeBundleJSONValue 保留 Python json.loads 的整数与浮点区分
// 否则解析后再编码会把 1 写成 1.0，与导出侧摘要不一致
func normalizeBundleJSONValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, ".eE") {
			if parsed, err := typed.Int64(); err == nil {
				return parsed
			}
		}
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
		return text
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeBundleJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, normalizeBundleJSONValue(item))
		}
		return out
	default:
		return value
	}
}

// validateInstallScope 对齐 _validate_install_scope
func validateInstallScope(payload map[string]any) (string, string, string) {
	scopeType := strings.ToLower(strings.TrimSpace(asString(payload["scope_type"])))
	if scopeType == "" {
		scopeType = "global"
	}
	chatID := strings.TrimSpace(asString(firstNonEmpty(payload["chat_id"], payload["scope_key"])))
	if scopeType == "chat" && chatID == "" {
		return "", "", ""
	}
	scopeKey := chatID
	if scopeType != "chat" {
		chatID = ""
		scopeKey = "global"
	}
	return scopeType, chatID, scopeKey
}

// stableInstallationID 对齐 _stable_installation_id
func stableInstallationID(packageID, version, scopeType, scopeKey string) string {
	seed := fmt.Sprintf("%s\n%s\n%s\n%s", packageID, version, scopeType, scopeKey)
	sum := sha256.Sum256([]byte(seed))
	return "kpi_" + hex.EncodeToString(sum[:])[:32]
}

// resourceKeyForRow 按资源主键生成归属键
func resourceKeyForRow(table string, row map[string]any) (string, error) {
	keys := resourcePrimaryKeys[table]
	if len(keys) == 0 {
		return "", fmt.Errorf("知识包包含未知资源类型: %s", table)
	}
	values := make([]any, 0, len(keys))
	for _, key := range keys {
		values = append(values, row[key])
	}
	encoded, err := canonicalJSON(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// resourceKeyValues 反解资源归属键，兼容历史裸键
func resourceKeyValues(table, resourceKey string) ([]any, error) {
	keys := resourcePrimaryKeys[table]
	if len(keys) == 0 {
		return nil, fmt.Errorf("知识包包含未知资源类型: %s", table)
	}
	var values []any
	if err := json.Unmarshal([]byte(resourceKey), &values); err != nil {
		if len(keys) != 1 {
			return nil, fmt.Errorf("知识包资源主键无效: %s", table)
		}
		values = []any{resourceKey}
	}
	if len(values) != len(keys) {
		return nil, fmt.Errorf("知识包资源主键无效: %s", table)
	}
	return values, nil
}

// deleteResourceRow 按资源主键删除一行
func (s *BundleAdminService) deleteResourceRow(tx *storage.Tx, table, resourceKey string) (int, error) {
	keys := resourcePrimaryKeys[table]
	if len(keys) == 0 {
		return 0, fmt.Errorf("知识包包含未知资源类型: %s", table)
	}
	values, err := resourceKeyValues(table, resourceKey)
	if err != nil {
		return 0, err
	}
	clauses := make([]string, 0, len(keys))
	for _, key := range keys {
		clauses = append(clauses, key+" = ?")
	}
	result, err := tx.Exec("DELETE FROM "+table+" WHERE "+strings.Join(clauses, " AND "), values...)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// nowUnix 当前 Unix 秒
func nowUnix() float64 {
	return float64(time.Now().Unix())
}

// countKeys 返回计数非零的表名
func countKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key, count := range values {
		if count > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
