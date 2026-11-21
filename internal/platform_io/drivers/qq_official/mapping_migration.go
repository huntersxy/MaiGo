package qq_official

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"MaiGo/internal/person_info/identity"

	_ "github.com/glebarez/go-sqlite"
)

type mappingMigrationCandidate struct {
	Source   string   `json:"source"`
	Targets  []string `json:"targets"`
	Target   string   `json:"target"`
	Conflict bool     `json:"conflict"`
	Status   string   `json:"status"`
}

func (a *Adapter) webuiIdentityMode(context.Context, string, string, map[string]any) (any, error) {
	return map[string]bool{"mapping_enabled": a.mappingEnabled.Load()}, nil
}

func (a *Adapter) webuiSetMappingMode(_ context.Context, _, _ string, args map[string]any) (any, error) {
	enabled, ok := args["enabled"].(bool)
	if !ok {
		return nil, errors.New("enabled 必须为布尔值")
	}
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if err := a.saveMappingMode(enabled); err != nil {
		return nil, err
	}
	return map[string]bool{"mapping_enabled": enabled}, nil
}

func (a *Adapter) saveMappingMode(enabled bool) error {
	if a.hostHooks == nil || a.hostHooks.ConfigPath == nil {
		return errors.New("QQ 适配器配置路径未接线")
	}
	path, err := a.hostHooks.ConfigPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := saveIdentityFile(path, []byte(upsertTOMLBool(string(raw), "qq_official", "enable_mapping", enabled))); err != nil {
		return err
	}
	a.mappingEnabled.Store(enabled)
	return nil
}

func (a *Adapter) webuiMappingMigrations(ctx context.Context, _, _ string, _ map[string]any) (any, error) {
	candidates, err := a.mappingMigrationCandidates(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"enabled": a.mappingEnabled.Load(), "candidates": candidates}, nil
}

func (a *Adapter) webuiFinishMapping(ctx context.Context, _, _ string, _ map[string]any) (any, error) {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	candidates, err := a.mappingMigrationCandidates(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.Status != "migrated" && candidate.Status != "empty" {
			return nil, errors.New("仍有未迁移用户或身份冲突，请先完成处理")
		}
	}
	if err := a.saveMappingMode(false); err != nil {
		return nil, err
	}
	return map[string]bool{"enabled": false}, nil
}

func (a *Adapter) mappingMigrationCandidates(ctx context.Context) ([]mappingMigrationCandidate, error) {
	if a.hostHooks == nil || a.hostHooks.InstanceRoot == nil {
		return nil, errors.New("实例目录未接线")
	}
	root := a.hostHooks.InstanceRoot()
	app, account := strings.TrimSpace(a.settings.QQOfficial.AppID), a.settings.QQOfficial.UnifiedAccountID
	bySource := map[string]*mappingMigrationCandidate{}
	add := func(source, target string, conflict bool) {
		item := bySource[source]
		if item == nil {
			item = &mappingMigrationCandidate{Source: source, Targets: []string{}}
			bySource[source] = item
		}
		if !slices.Contains(item.Targets, target) {
			item.Targets = append(item.Targets, target)
		}
		item.Conflict = item.Conflict || conflict
	}
	a.mu.Lock()
	mapping, syncIDs := a.mapping, a.syncIDs
	a.mu.Unlock()
	for openID, id := range mapping.User {
		add(id, openID, false)
	}
	if syncIDs != nil {
		syncIDs.mu.Lock()
		for scope, row := range syncIDs.profiles {
			if scope[0] == app && scope[1] == account {
				manual := mapping.User[scope[4]]
				add(row.UserID, scope[4], row.Conflict || syncIDs.candidates[scope].conflict || (manual != "" && manual != row.UserID))
			}
		}
		syncIDs.mu.Unlock()
	}
	databasePath, err := filepath.Abs(filepath.Join(root, "data", "MaiBot.db"))
	if err != nil {
		return nil, err
	}
	path := filepath.ToSlash(databasePath)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	database, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		return nil, err
	}
	defer database.Close()
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	state, err := identity.Load(ctx, tx, root)
	if err != nil {
		return nil, err
	}
	result := make([]mappingMigrationCandidate, 0, len(bySource))
	for _, source := range slices.Sorted(maps.Keys(bySource)) {
		item := bySource[source]
		var hasData bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM person_info WHERE platform='qq' AND user_id=?)`, source).Scan(&hasData); err != nil {
			return nil, err
		}
		canonical := state.ResolveUserID(PlatformQQ, source)
		if canonical != source {
			item.Target = canonical
		}
		slices.Sort(item.Targets)
		switch {
		case slices.Contains(item.Targets, canonical) && !hasData:
			item.Status = "migrated"
		case item.Conflict || canonical != source:
			item.Status = "conflict"
		case hasData:
			item.Status = "pending"
		default:
			item.Status = "empty"
		}
		result = append(result, *item)
	}
	return result, nil
}
