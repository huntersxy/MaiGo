package identity

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

type PrivateRoute struct {
	Platform        string `json:"platform"`
	UserID          string `json:"user_id"`
	TransportUserID string `json:"transport_user_id"`
	AccountID       string `json:"account_id"`
	Scope           string `json:"scope"`
}

type State struct {
	Version        int                          `json:"version"`
	Aliases        map[string]map[string]string `json:"aliases"`
	PrivateRoutes  []PrivateRoute               `json:"private_routes"`
	UserTransports map[string]map[string]string `json:"user_transports,omitempty"`
}

type Queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func AliasPath(root string) string {
	return filepath.Join(root, "data", "person_identity_aliases.json")
}

func LoadDatabase(ctx context.Context, db *gorm.DB, root string) (State, error) {
	if db == nil || db.Dialector.Name() != "sqlite" {
		return Load(ctx, nil, root)
	}
	query, ok := db.Statement.ConnPool.(Queryer)
	if !ok {
		return State{}, errors.New("人物身份数据库连接不可用")
	}
	return Load(ctx, query, root)
}

func Load(ctx context.Context, db Queryer, root string) (State, error) {
	if db != nil {
		var hasTable bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main.sqlite_schema WHERE name='person_identity_state' AND type='table')`).Scan(&hasTable); err != nil {
			return State{}, err
		}
		if hasTable {
			var data []byte
			err := db.QueryRowContext(ctx, `SELECT payload FROM main.person_identity_state WHERE id=1`).Scan(&data)
			if err == nil {
				return Decode(data)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return State{}, err
			}
		}
	}
	if root != "" {
		data, err := os.ReadFile(AliasPath(root))
		if err == nil {
			return Decode(data)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return State{}, err
		}
	}
	return State{Version: 1, Aliases: map[string]map[string]string{}, PrivateRoutes: []PrivateRoute{}}, nil
}

func Decode(data []byte) (State, error) {
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Version != 1 || state.Aliases == nil || state.PrivateRoutes == nil {
		return State{}, errors.New("人物身份别名状态格式错误")
	}
	for _, mappings := range []map[string]map[string]string{state.Aliases, state.UserTransports} {
		for platform, users := range mappings {
			if !ValidID(platform) || users == nil {
				return State{}, errors.New("人物身份映射缺少平台")
			}
			for source, target := range users {
				if !ValidID(source) || !ValidID(target) {
					return State{}, errors.New("人物身份映射缺少用户 ID")
				}
			}
		}
	}
	for _, users := range state.Aliases {
		for source := range users {
			if _, err := ResolveAlias(users, source); err != nil {
				return State{}, err
			}
		}
	}
	seen := map[[4]string]struct{}{}
	for _, route := range state.PrivateRoutes {
		if !ValidID(route.Platform) || !ValidID(route.UserID) || !ValidID(route.TransportUserID) {
			return State{}, errors.New("人物私聊路由缺少平台或用户 ID")
		}
		key := [4]string{route.Platform, route.UserID, route.AccountID, route.Scope}
		if _, exists := seen[key]; exists {
			return State{}, errors.New("人物私聊路由重复")
		}
		seen[key] = struct{}{}
	}
	return state, nil
}

func ValidID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsRune(value, 0)
}

func ResolveAlias(mappings map[string]string, userID string) (string, error) {
	for steps := 0; ; steps++ {
		next, exists := mappings[userID]
		if !exists {
			return userID, nil
		}
		if steps >= len(mappings) {
			return "", errors.New("人物身份别名存在循环")
		}
		userID = next
	}
}

func (state State) ResolveUserID(platform, userID string) string {
	resolved, err := ResolveAlias(state.Aliases[platform], userID)
	if err != nil {
		return userID
	}
	return resolved
}

func (state State) ResolvePersonID(personID string) string {
	for platform, mappings := range state.Aliases {
		for source := range mappings {
			if PersonID(platform, source) == personID {
				return PersonID(platform, state.ResolveUserID(platform, source))
			}
		}
	}
	return personID
}

func (state State) ResolvePrivateTransportUser(platform, userID, accountID, scope string) string {
	for _, route := range state.PrivateRoutes {
		if route.Platform == platform && route.UserID == userID && route.AccountID == accountID && route.Scope == scope {
			return route.TransportUserID
		}
	}
	if physicalID := state.UserTransports[platform][userID]; physicalID != "" {
		return physicalID
	}
	return userID
}

func PersonID(platform string, userID any) string {
	if parts := strings.SplitN(strings.TrimSpace(platform), "-", 3); len(parts) > 1 {
		platform = parts[1]
	} else {
		platform = parts[0]
	}
	digest := md5.Sum([]byte(platform + "_" + fmt.Sprint(userID)))
	return hex.EncodeToString(digest[:])
}
