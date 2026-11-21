package qq_official

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const bindingDeleteJournal = "binding_delete_pending.json"

var bindingFiles = [...]string{"id_map.json", "auto_user_profiles.json", unionIdentitiesFile}

func recoverBindingDelete(directory string) error {
	journal := filepath.Join(directory, bindingDeleteJournal)
	raw, err := os.ReadFile(journal)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var files map[string]string
	if err := json.Unmarshal(raw, &files); err != nil || len(files) != len(bindingFiles) {
		return errors.New("绑定删除记录无效")
	}
	for _, filename := range bindingFiles {
		if !json.Valid([]byte(files[filename])) {
			return errors.New("绑定删除记录内容无效")
		}
	}
	for _, filename := range bindingFiles {
		if err := saveIdentityFile(filepath.Join(directory, filename), []byte(files[filename])); err != nil {
			return err
		}
	}
	return os.Remove(journal)
}

func (a *Adapter) webuiDeleteBindings(ctx context.Context, _, _ string, args map[string]any) (any, error) {
	kind, _ := args["kind"].(string)
	raw, _ := args["openids_json"].(string)
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil || (kind != "group" && kind != "user") || len(values) == 0 || len(values) > 10000 {
		return nil, errors.New("请选择群或用户绑定")
	}
	selected := map[string]bool{}
	for _, value := range values {
		if value == "" || utf8.RuneCountInString(value) > 128 {
			return nil, errors.New("绑定 OpenID 无效")
		}
		selected[value] = true
	}
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	if err := a.stopLocked(ctx); err != nil {
		return nil, err
	}
	directory := resolveDataDir(a.hostHooks)
	err := a.deleteBindings(directory, kind, selected)
	if err := errors.Join(err, a.startLocked(context.WithoutCancel(ctx))); err != nil {
		return nil, err
	}
	return map[string]int{"deleted": len(selected)}, nil
}

func (a *Adapter) deleteBindings(directory, kind string, selected map[string]bool) error {
	if err := recoverBindingDelete(directory); err != nil {
		return err
	}
	mapping, err := loadIDMapping(directory)
	if err != nil {
		return err
	}
	identities, err := loadUnionIdentities(directory)
	if err != nil {
		return err
	}
	syncIDs, err := loadIdentitySync(directory)
	if err != nil {
		return err
	}
	app := strings.TrimSpace(a.settings.QQOfficial.AppID)
	entries, scopeIndex := mapping.User, 4
	if kind == "group" {
		entries, scopeIndex = mapping.Group, 2
	}
	maps.DeleteFunc(entries, func(openID, _ string) bool { return selected[openID] })
	profiles := []identityProfile{}
	for scope, row := range syncIDs.profiles {
		if scope[0] != app || !selected[scope[scopeIndex]] {
			profiles = append(profiles, row)
		}
	}
	slices.SortFunc(profiles, func(left, right identityProfile) int { return slices.Compare(left.Scope, right.Scope) })
	identities.Bindings = slices.DeleteFunc(slices.Clone(identities.Bindings), func(row unionBinding) bool {
		return row.AppID == app && ((kind == "group" && selected[row.GroupOpenID]) || (kind == "user" && selected[row.OpenID]))
	})
	files := map[string]string{}
	for index, content := range []any{mapping, profiles, identities} {
		raw, err := json.Marshal(content)
		if err != nil {
			return err
		}
		files[bindingFiles[index]] = string(raw)
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return err
	}
	if err := saveIdentityFile(filepath.Join(directory, bindingDeleteJournal), raw); err != nil {
		return err
	}
	return recoverBindingDelete(directory)
}
