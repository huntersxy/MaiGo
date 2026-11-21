package qq_official

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const unionIdentitiesFile = "union_user_ids.json"

type unionBinding struct {
	AppID       string  `json:"app_id"`
	GroupOpenID string  `json:"group_openid"`
	OpenID      string  `json:"openid"`
	UnionOpenID string  `json:"union_openid"`
	QQID        string  `json:"qq_id"`
	QQSource    string  `json:"qq_source"`
	ObservedAt  float64 `json:"observed_at"`
}

type unionIdentities struct {
	Version  int            `json:"version"`
	Bindings []unionBinding `json:"bindings"`
}

func loadUnionIdentities(directory string) (unionIdentities, error) {
	store := unionIdentities{Version: 1, Bindings: []unionBinding{}}
	data, err := os.ReadFile(filepath.Join(directory, unionIdentitiesFile))
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return store, err
	}
	store = unionIdentities{}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store); err != nil || !json.Valid(data) || store.Version != 1 || store.Bindings == nil {
		return store, errors.New("不支持的 Union 身份文件格式")
	}
	seen := map[[3]string]bool{}
	for _, row := range store.Bindings {
		key := [3]string{row.AppID, row.GroupOpenID, row.OpenID}
		if row.AppID == "" || row.OpenID == "" || row.UnionOpenID == "" || seen[key] ||
			(row.QQID == "" && row.QQSource != "") || (row.QQID != "" && (!isDecimal(row.QQID) || (row.QQSource != "manual" && row.QQSource != "silent"))) {
			return store, errors.New("Union 身份记录无效或重复")
		}
		seen[key] = true
	}
	return store, nil
}

func (a *Adapter) observeUnionIdentity(group, openID string, author map[string]any, qqID string, manual bool) error {
	app := strings.TrimSpace(a.settings.QQOfficial.AppID)
	value, _ := author["union_openid"].(string)
	value = strings.TrimSpace(value)
	if app == "" || openID == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	index := slices.IndexFunc(a.union.Bindings, func(row unionBinding) bool {
		return row.AppID == app && row.GroupOpenID == group && row.OpenID == openID
	})
	row := unionBinding{AppID: app, GroupOpenID: group, OpenID: openID, UnionOpenID: value, ObservedAt: float64(time.Now().UnixMicro()) / 1e6}
	if index >= 0 {
		row = a.union.Bindings[index]
		if value != "" && value != row.UnionOpenID {
			return fmt.Errorf("官方 OpenID 的 Union 身份发生冲突: %s", openID)
		}
	} else if value == "" {
		return nil
	}
	if qqID != "" {
		if !isDecimal(qqID) {
			return errors.New("Union 身份的历史 QQ 号必须为数字")
		}
		row.QQID, row.QQSource = qqID, "silent"
		if manual {
			row.QQSource = "manual"
		}
	}
	if index >= 0 && row == a.union.Bindings[index] {
		return nil
	}
	updated := unionIdentities{Version: 1, Bindings: slices.Clone(a.union.Bindings)}
	if index < 0 {
		updated.Bindings = append(updated.Bindings, row)
	} else {
		updated.Bindings[index] = row
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	if err := saveIdentityFile(filepath.Join(resolveDataDir(a.hostHooks), unionIdentitiesFile), encoded); err != nil {
		return err
	}
	a.union = updated
	return nil
}

func (identities unionIdentities) resolveUser(app, group, user string, legacy map[string]string) (string, error) {
	if user == "" {
		return "", errors.New("缺少用户 OpenID")
	}
	unions := map[string]bool{}
	known := map[string]bool{}
	for _, row := range identities.Bindings {
		if row.AppID != app {
			continue
		}
		known[row.OpenID] = true
		if row.OpenID == user || row.UnionOpenID == user || row.QQID == user || legacy[row.OpenID] == user {
			if row.OpenID == user && row.GroupOpenID == group {
				return user, nil
			}
			unions[row.UnionOpenID] = true
		}
	}
	if len(unions) > 1 {
		return "", fmt.Errorf("用户 %s 对应多个 Union 身份", user)
	}
	candidates := map[string]bool{}
	for openID, id := range legacy {
		if id == user && !known[openID] {
			candidates[openID] = true
		}
	}
	var best unionBinding
	for _, row := range identities.Bindings {
		if row.AppID == app && row.GroupOpenID == group && unions[row.UnionOpenID] && (best.OpenID == "" || row.ObservedAt > best.ObservedAt || (row.ObservedAt == best.ObservedAt && row.OpenID > best.OpenID)) {
			best = row
		}
	}
	if best.OpenID != "" {
		candidates[best.OpenID] = true
	}
	if len(candidates) > 1 {
		return "", fmt.Errorf("用户 %s 对应多个场景 OpenID", user)
	}
	for candidate := range candidates {
		return candidate, nil
	}
	if len(unions) > 0 || isDecimal(user) {
		return "", fmt.Errorf("用户 %s 缺少当前场景 OpenID", user)
	}
	return user, nil
}
