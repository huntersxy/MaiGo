package qq_official

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/snowluma"
)

const (
	identityObservationTTL = 120 * time.Second
	identitySettleTime     = 5 * time.Second
	identityEvidenceTTL    = 10 * time.Minute
	identityProfileTTL     = 90 * 24 * time.Hour
	maxIdentityRows        = 2000
)

type identityScope [5]string

func (scope identityScope) group() [4]string { return [4]string(scope[:4]) }

type identityObservation struct {
	scope     identityScope
	id        string
	print     string
	stamp     float64
	received  time.Time
	processed bool
}

type identityCandidate struct {
	user     string
	prints   map[string]bool
	created  time.Time
	touched  time.Time
	conflict bool
}

type identityProfile struct {
	Scope     []string `json:"scope"`
	UserID    string   `json:"user_id"`
	Nickname  string   `json:"nickname"`
	Cardname  string   `json:"cardname"`
	Timestamp float64  `json:"timestamp"`
	Updated   float64  `json:"updated_at"`
	Conflict  bool     `json:"conflict"`
}

type identitySync struct {
	mu           sync.Mutex
	observations []identityObservation
	candidates   map[identityScope]identityCandidate
	profiles     map[identityScope]identityProfile
	used         map[string]time.Time
	path         string
	dirty        bool
}

func loadIdentitySync(directory string) (*identitySync, error) {
	state := &identitySync{
		path: filepath.Join(directory, "auto_user_profiles.json"), candidates: make(map[identityScope]identityCandidate),
		profiles: make(map[identityScope]identityProfile), used: make(map[string]time.Time),
	}
	file, err := os.Open(state.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const limit = 8 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return nil, fmt.Errorf("自动身份映射文件读取失败或超过大小限制: %v", err)
	}
	var rows []identityProfile
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return nil, fmt.Errorf("自动身份映射文件必须是列表: %v", err)
	}
	for _, profile := range rows[max(0, len(rows)-5000):] {
		if len(profile.Scope) != 5 || slices.ContainsFunc(profile.Scope, func(value string) bool {
			return value == "" || utf8.RuneCountInString(value) > 128
		}) || !isDecimal(profile.UserID) || len(profile.UserID) > 32 {
			return nil, fmt.Errorf("自动身份映射的作用域或 QQ 号无效")
		}
		profile.Nickname, profile.Cardname = clampIdentityName(profile.Nickname), clampIdentityName(profile.Cardname)
		state.profiles[identityScope(profile.Scope)] = profile
	}
	state.prune(time.Now())
	return state, nil
}

func (s *identitySync) prune(now time.Time) {
	s.observations = slices.DeleteFunc(s.observations, func(row identityObservation) bool {
		return now.Sub(row.received) >= identityObservationTTL
	})
	maps.DeleteFunc(s.candidates, func(_ identityScope, row identityCandidate) bool { return now.Sub(row.created) >= identityEvidenceTTL })
	maps.DeleteFunc(s.used, func(_ string, used time.Time) bool { return now.Sub(used) >= identityObservationTTL })
	wall := identitySeconds(now)
	maps.DeleteFunc(s.profiles, func(_ identityScope, row identityProfile) bool {
		return wall-row.Updated >= identityProfileTTL.Seconds()
	})
}

func (s *identitySync) observe(scope identityScope, message map[string]any) {
	now := time.Now()
	stamp, err := strconv.ParseFloat(asString(message["timestamp"]), 64)
	if s == nil || err != nil || !nearbyIdentityTime(stamp, identitySeconds(now), identityObservationTTL.Seconds()) {
		return
	}
	parts, _ := message["raw_message"].([]map[string]any)
	row := identityObservation{scope: scope, id: asString(message["message_id"]), print: platformio.IdentityTextFingerprint(parts), stamp: stamp, received: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	var indexes []int
	for i, old := range s.observations {
		if old.scope == scope && old.id == row.id {
			return
		}
		if old.scope.group() == scope.group() {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) >= 128 {
		index := indexes[0]
		s.observations = slices.Delete(s.observations, index, index+1)
	}
	if len(s.observations) >= maxIdentityRows {
		s.observations = slices.Delete(s.observations, 0, 1)
	}
	s.observations = append(s.observations, row)
}

func (s *identitySync) groups(app, account string, binding idMapping) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	seen := make(map[string]bool)
	for _, row := range s.observations {
		if !row.processed && row.scope[0] == app && row.scope[1] == account && binding.Group[row.scope[2]] == row.scope[3] {
			seen[row.scope[3]] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func (s *identitySync) reconcile(app, account string, incoming []snowluma.IdentityObservation) {
	now := time.Now()
	wall := identitySeconds(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	remote := make(map[string]snowluma.IdentityObservation)
	for _, row := range incoming[:min(maxIdentityRows, len(incoming))] {
		if row.Key != "" && row.AccountID == account && nearbyIdentityTime(row.Timestamp, wall, identityObservationTTL.Seconds()) {
			remote[row.Key] = row
		}
	}
	remoteBuckets := make(map[[2]string][]snowluma.IdentityObservation)
	latest := make(map[[2]string]snowluma.IdentityObservation)
	for _, row := range remote {
		if row.Fingerprint != "" {
			key := [2]string{row.GroupID, row.Fingerprint}
			remoteBuckets[key] = append(remoteBuckets[key], row)
		}
		key := [2]string{row.GroupID, row.UserID}
		if old, ok := latest[key]; !ok || row.Timestamp > old.Timestamp || (row.Timestamp == old.Timestamp && row.Key < old.Key) {
			latest[key] = row
		}
	}
	localBuckets := make(map[[2]string][]float64)
	for _, row := range s.observations {
		if row.scope[0] == app && row.scope[1] == account {
			key := [2]string{row.scope[3], row.print}
			localBuckets[key] = append(localBuckets[key], row.stamp)
		}
	}
	for index := range s.observations {
		row := &s.observations[index]
		if row.processed || row.scope[0] != app || row.scope[1] != account || now.Sub(row.received) < identitySettleTime {
			continue
		}
		if row.print == "" {
			row.processed = true
			continue
		}
		key := [2]string{row.scope[3], row.print}
		var matched *snowluma.IdentityObservation
		for _, candidate := range remoteBuckets[key] {
			if !nearbyIdentityTime(candidate.Timestamp, row.stamp, 2) {
				continue
			}
			if matched != nil {
				matched = nil
				break
			}
			value := candidate
			matched = &value
		}
		if matched == nil || !s.used[matched.Key].IsZero() {
			continue
		}
		peers := 0
		for _, stamp := range localBuckets[key] {
			if nearbyIdentityTime(stamp, matched.Timestamp, 2) {
				peers++
			}
		}
		if peers == 1 {
			row.processed = true
			s.used[matched.Key] = now
			if isDecimal(matched.UserID) && len(matched.UserID) <= 32 {
				s.recordEvidence(row.scope, row.print, *matched, now)
			}
		}
	}
	for scope, profile := range s.profiles {
		if scope[0] != app || scope[1] != account || profile.Conflict || s.candidates[scope].conflict {
			continue
		}
		row, exists := latest[[2]string{scope[3], profile.UserID}]
		if exists && row.Timestamp > profile.Timestamp {
			s.remember(scope, row, wall)
		}
	}
	for len(s.used) > maxIdentityRows {
		oldest := ""
		for id, used := range s.used {
			if oldest == "" || used.Before(s.used[oldest]) || (used.Equal(s.used[oldest]) && id < oldest) {
				oldest = id
			}
		}
		delete(s.used, oldest)
	}
}

func (s *identitySync) recordEvidence(scope identityScope, print string, row snowluma.IdentityObservation, now time.Time) {
	candidate, exists := s.candidates[scope]
	if !exists {
		candidate = identityCandidate{user: row.UserID, prints: make(map[string]bool), created: now}
	}
	wasConflict := candidate.conflict
	profile, hasProfile := s.profiles[scope]
	candidate.touched = now
	candidate.conflict = candidate.conflict || candidate.user != row.UserID || (hasProfile && profile.UserID != row.UserID)
	if !candidate.conflict {
		if len(candidate.prints) < 3 {
			candidate.prints[print] = true
		}
		if (!hasProfile && len(candidate.prints) >= 3) || (hasProfile && !profile.Conflict && row.Timestamp >= profile.Timestamp) {
			for owner, other := range s.profiles {
				if owner != scope && owner.group() == scope.group() && other.UserID == row.UserID {
					candidate.conflict = true
					break
				}
			}
			if !candidate.conflict {
				s.remember(scope, row, identitySeconds(now))
			}
		}
	}
	if candidate.conflict && !wasConflict {
		zap.L().Warn("QQ 静默身份匹配存在冲突，暂停自动绑定", zap.String("group", scope[3]), zap.String("openid", scope[4]))
	}
	if candidate.conflict && hasProfile && !profile.Conflict {
		profile.Conflict = true
		s.profiles[scope] = profile
		s.dirty = true
	}
	s.candidates[scope] = candidate
	if len(s.candidates) > 1000 {
		oldest := scope
		for other, item := range s.candidates {
			if item.touched.Before(s.candidates[oldest].touched) {
				oldest = other
			}
		}
		delete(s.candidates, oldest)
	}
}

func (s *identitySync) remember(scope identityScope, row snowluma.IdentityObservation, now float64) {
	updated := identityProfile{Scope: slices.Clone(scope[:]), UserID: row.UserID, Nickname: clampIdentityName(row.Nickname),
		Cardname: clampIdentityName(row.Cardname), Timestamp: row.Timestamp, Updated: now}
	existing, exists := s.profiles[scope]
	if !exists || existing.UserID != updated.UserID || existing.Nickname != updated.Nickname || existing.Cardname != updated.Cardname || now-existing.Updated >= 3600 {
		s.dirty = true
	} else {
		updated.Updated = existing.Updated
	}
	s.profiles[scope] = updated
	if len(s.profiles) > 5000 {
		oldest := scope
		for other, item := range s.profiles {
			if item.Timestamp < s.profiles[oldest].Timestamp {
				oldest = other
			}
		}
		delete(s.profiles, oldest)
	}
}

func (s *identitySync) save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	s.prune(time.Now())
	rows := make([]identityProfile, 0, len(s.profiles))
	for _, row := range s.profiles {
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b identityProfile) int { return slices.Compare(a.Scope, b.Scope) })
	s.mu.Unlock()
	encoded, err := json.Marshal(rows)
	if err == nil {
		err = saveIdentityFile(s.path, encoded)
	}
	if err == nil {
		s.mu.Lock()
		s.dirty = false
		s.mu.Unlock()
	}
	return err
}

func (s *identitySync) profilesInGroup(app, account, openID, binding string) map[string]identityProfile {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	result := make(map[string]identityProfile)
	for scope, row := range s.profiles {
		if scope[0] == app && scope[1] == account && scope[2] == openID && scope[3] == binding {
			row.Conflict = row.Conflict || s.candidates[scope].conflict
			result[scope[4]] = row
		}
	}
	return result
}

func (a *Adapter) profileGroup(mapping idMapping, openID string) map[string]identityProfile {
	if binding := mapping.Group[openID]; openID != "" && isDecimal(binding) {
		a.mu.Lock()
		state := a.syncIDs
		a.mu.Unlock()
		return state.profilesInGroup(strings.TrimSpace(a.settings.QQOfficial.AppID), a.settings.QQOfficial.UnifiedAccountID, openID, binding)
	}
	return nil
}

func (a *Adapter) syncIdentities(ctx context.Context) error {
	app, account := strings.TrimSpace(a.settings.QQOfficial.AppID), a.settings.QQOfficial.UnifiedAccountID
	a.mu.Lock()
	state, mapping := a.syncIDs, a.mapping
	a.mu.Unlock()
	if state == nil || app == "" || !isDecimal(account) {
		return nil
	}
	var syncErr error
	for groupIDs := range slices.Chunk(state.groups(app, account, mapping), 128) {
		response, err := a.identity.callObservations(ctx, account, groupIDs)
		if err != nil {
			syncErr = err
			break
		}
		protocol, _ := asInt(response["protocol"])
		if protocol != 1 || response["observations"] == nil {
			syncErr = fmt.Errorf("SnowLuma 未提供身份指纹协议 v1")
			break
		}
		var observations []snowluma.IdentityObservation
		raw, err := json.Marshal(response["observations"])
		if err == nil {
			err = json.Unmarshal(raw, &observations)
		}
		if err != nil {
			syncErr = err
			break
		}
		state.reconcile(app, account, observations)
	}
	return errors.Join(syncErr, state.save())
}

func (a *Adapter) groupUserMapping(mapping idMapping, openID string) (idMapping, map[string]identityProfile) {
	if !a.settings.QQOfficial.SilentUserSync {
		return mapping, nil
	}
	profiles := a.profileGroup(mapping, openID)
	if len(profiles) == 0 {
		return mapping, nil
	}
	manual := mapping.User
	mapping.User = make(map[string]string, len(manual)+len(profiles))
	for id, row := range profiles {
		if !row.Conflict {
			mapping.User[id] = row.UserID
		}
	}
	maps.Copy(mapping.User, manual)
	return mapping, profiles
}

func identitySeconds(now time.Time) float64 { return float64(now.UnixMicro()) / 1e6 }

func nearbyIdentityTime(a, b, delta float64) bool { return math.Abs(a-b) <= delta }

func clampIdentityName(name string) string {
	if len(name) <= 128 {
		return name
	}
	count := 0
	for index := range name {
		if count == 128 {
			return name[:index]
		}
		count++
	}
	return name
}
