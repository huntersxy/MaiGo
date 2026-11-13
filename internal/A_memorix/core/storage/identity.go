package storage

// A_memorix 人物身份与历史称呼，对齐 Python core/storage/metadata_identity.py
// 只按平台用户 ID 合并身份，不用昵称认领其他用户

import (
	"fmt"
	"strings"
	"time"
)

// PersonIdentity 人物身份快照，对应 Python 返回的 dict 键
type PersonIdentity struct {
	PersonID      string   `json:"person_id"`
	Platform      string   `json:"platform"`
	UserID        string   `json:"user_id"`
	CanonicalName string   `json:"canonical_name"`
	Aliases       []string `json:"aliases"`
}

// ToMap 转换为 Python 侧同构 dict
func (p PersonIdentity) ToMap() map[string]any {
	aliases := make([]any, 0, len(p.Aliases))
	for _, alias := range p.Aliases {
		aliases = append(aliases, alias)
	}
	return map[string]any{
		"person_id":      p.PersonID,
		"platform":       p.Platform,
		"user_id":        p.UserID,
		"canonical_name": p.CanonicalName,
		"aliases":        aliases,
	}
}

// ObservePersonIdentity 观察一次人物身份，只按 ID 合并，保留每次观察到的昵称
func (s *Store) ObservePersonIdentity(personID, platform, userID string, aliases []string, groupID, preferredName string) (map[string]any, error) {
	if strings.TrimSpace(personID) == "" || strings.TrimSpace(platform) == "" || strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("身份映射必须包含 person_id、platform 和 user_id")
	}

	names := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		names = append(names, trimmed)
	}

	now := float64(time.Now().UnixNano()) / 1e9
	var canonicalName string
	err := s.Transaction(true, func(tx *Tx) error {
		var existingPlatform, existingUserID, existingName string
		err := tx.QueryRow(
			"SELECT platform, user_id, canonical_name FROM person_identities WHERE person_id = ?",
			personID,
		).Scan(&existingPlatform, &existingUserID, &existingName)
		switch {
		case err == nil:
			if existingPlatform != platform || existingUserID != userID {
				return fmt.Errorf("person_id 已绑定其他平台用户，不能按称呼合并")
			}
			canonicalName = existingName
		case isNoRows(err):
			canonicalName = strings.TrimSpace(preferredName)
			if canonicalName == "" && len(names) > 0 {
				canonicalName = names[0]
			}
			if canonicalName == "" {
				canonicalName = userID
			}
			var collisionPerson string
			collisionErr := tx.QueryRow(
				"SELECT person_id FROM person_identities WHERE canonical_name = ?",
				canonicalName,
			).Scan(&collisionPerson)
			if collisionErr == nil {
				canonicalName = fmt.Sprintf("%s [%s]", canonicalName, personID)
			} else if !isNoRows(collisionErr) {
				return collisionErr
			}
			if _, err := tx.Exec(
				"INSERT INTO person_identities VALUES (?, ?, ?, ?, ?, ?)",
				personID, platform, userID, canonicalName, now, now,
			); err != nil {
				return err
			}
		default:
			return err
		}

		if _, err := tx.Exec("UPDATE person_identities SET updated_at = ? WHERE person_id = ?", now, personID); err != nil {
			return err
		}

		aliasNames := make([]string, 0, len(names)+1)
		aliasNames = append(aliasNames, canonicalName)
		aliasSeen := map[string]struct{}{canonicalName: {}}
		for _, name := range names {
			if _, exists := aliasSeen[name]; exists {
				continue
			}
			aliasSeen[name] = struct{}{}
			aliasNames = append(aliasNames, name)
		}
		for _, name := range aliasNames {
			if _, err := tx.Exec(
				`INSERT INTO person_identity_aliases(person_id, alias, group_id, first_seen, last_seen)
				 VALUES (?, ?, ?, ?, ?)
				 ON CONFLICT(person_id, alias, group_id) DO UPDATE SET last_seen = excluded.last_seen`,
				personID, name, groupID, now, now,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	identity, err := s.GetPersonIdentity(personID)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, fmt.Errorf("身份写入后读取失败: %s", personID)
	}
	return identity.ToMap(), nil
}

// GetPersonIdentity 读取人物身份，不存在时返回 nil
func (s *Store) GetPersonIdentity(personID string) (*PersonIdentity, error) {
	conn, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	var identity PersonIdentity
	err = conn.QueryRow(
		"SELECT person_id, platform, user_id, canonical_name FROM person_identities WHERE person_id = ?",
		personID,
	).Scan(&identity.PersonID, &identity.Platform, &identity.UserID, &identity.CanonicalName)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(
		"SELECT DISTINCT alias FROM person_identity_aliases WHERE person_id = ? ORDER BY alias",
		personID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		identity.Aliases = append(identity.Aliases, alias)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &identity, nil
}

// FindPersonIdentities 按称呼查找人物，同名可能对应多人，调用方不得取第一条猜测身份
func (s *Store) FindPersonIdentities(alias string, platform string) ([]map[string]any, error) {
	conn, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(
		`SELECT DISTINCT i.person_id FROM person_identities i
		   JOIN person_identity_aliases a ON a.person_id = i.person_id
		   WHERE a.alias = ? AND (? = '' OR i.platform = ?) ORDER BY i.person_id`,
		strings.TrimSpace(alias), platform, platform,
	)
	if err != nil {
		return nil, err
	}
	var personIDs []string
	for rows.Next() {
		var personID string
		if err := rows.Scan(&personID); err != nil {
			rows.Close()
			return nil, err
		}
		personIDs = append(personIDs, personID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	identities := make([]map[string]any, 0, len(personIDs))
	for _, personID := range personIDs {
		identity, err := s.GetPersonIdentity(personID)
		if err != nil {
			return nil, err
		}
		if identity == nil {
			continue
		}
		identities = append(identities, identity.ToMap())
	}
	return identities, nil
}
