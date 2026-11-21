package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"MaiGo/internal/person_info/identity"

	_ "github.com/glebarez/go-sqlite"
	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
)

var ErrPersonMigrationStale = errors.New("人物迁移预览已过期，请重新预览")

type PersonMigrationIdentity struct {
	Platform         string                  `json:"platform"`
	Source           string                  `json:"source"`
	Target           string                  `json:"target"`
	SourcePerson     string                  `json:"source_person"`
	TargetPerson     string                  `json:"target_person"`
	UserMap          map[string]string       `json:"user_map"`
	PersonMap        map[string]string       `json:"person_map"`
	Sessions         map[string]string       `json:"sessions"`
	PlatformSessions []string                `json:"platform_sessions"`
	PrivateRoutes    []identity.PrivateRoute `json:"private_routes"`
}

type PersonMigrationAliases struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type PersonMigrationPlan struct {
	Version       int                       `json:"version"`
	Root          string                    `json:"root"`
	Identity      PersonMigrationIdentity   `json:"identity"`
	Databases     []PersonMigrationDatabase `json:"databases"`
	Conflicts     []PersonMigrationConflict `json:"conflicts"`
	Decisions     map[string]string         `json:"decisions"`
	Ready         bool                      `json:"ready"`
	Configuration map[string]*string        `json:"configuration"`
	Aliases       PersonMigrationAliases    `json:"aliases"`
	Digest        string                    `json:"digest,omitempty"`
}

type PersonMigrationExtension struct {
	Database  string
	BuildPlan func(*PersonDatabasePlan, PersonMigrationIdentity) error
}

var personMigrationExtensions sync.Map

func RegisterPersonMigrationExtension(name string, factory func(string) (PersonMigrationExtension, error)) error {
	if !identity.ValidID(name) || strings.Contains(name, ":") || name == "main" || strings.HasPrefix(name, "memory-") || factory == nil {
		return errors.New("迁移扩展名称或工厂无效")
	}
	if _, exists := personMigrationExtensions.LoadOrStore(name, factory); exists {
		return fmt.Errorf("重复迁移扩展：%s", name)
	}
	return nil
}

func (plan PersonMigrationPlan) Counts() map[string]map[string]int {
	counts := make(map[string]map[string]int, len(plan.Databases))
	for _, database := range plan.Databases {
		counts[database.Name] = database.Counts
	}
	return counts
}

func (plan PersonMigrationPlan) digest() (string, error) {
	plan.Digest = ""
	return personMigrationHash(plan)
}

func personMigrationHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func ReadPersonMigrationJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	return decoder.Decode(target)
}

func WritePersonMigrationJSON(path string, value any) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".person-migration-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := json.NewEncoder(temp).Encode(value); err != nil {
		temp.Close()
		return err
	}
	if err := errors.Join(temp.Sync(), temp.Close()); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func personMigrationAbs(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func personMigrationWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

func personMigrationFileHash(path string) ([]byte, *string, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(content)
	fingerprint := hex.EncodeToString(sum[:])
	return content, &fingerprint, nil
}

type personMigrationParticipant struct {
	name     string
	path     string
	schema   string
	isMemory bool
	build    func(*PersonDatabasePlan, PersonMigrationIdentity) error
}

type personMigrationLayout struct {
	root          string
	configuration map[string]*string
	participants  []personMigrationParticipant
}

func discoverPersonMigrationLayout(root string, extensions []string) (personMigrationLayout, error) {
	var layout personMigrationLayout
	root, err := personMigrationAbs(root)
	if err != nil {
		return layout, err
	}
	layout.root = root
	mainPath, err := personMigrationAbs(filepath.Join(root, "data", "MaiBot.db"))
	if err != nil {
		return layout, err
	}
	if !personMigrationWithin(root, mainPath) {
		return layout, errors.New("主数据库位于实例外")
	}
	layout.participants = []personMigrationParticipant{{name: "main", path: mainPath, schema: "main", build: buildPersonMainMigration}}
	configPath := filepath.Join(root, "config", "bot_config.toml")
	content, fingerprint, err := personMigrationFileHash(configPath)
	if err != nil {
		return layout, err
	}
	layout.configuration = map[string]*string{configPath: fingerprint}
	memoryDirectories := []string{filepath.Join(root, "data", "a-memorix"), filepath.Join(root, "data", "plugins", "a-dawn.a-memorix")}
	if fingerprint != nil {
		var config struct {
			Memory struct {
				Storage struct {
					DataDir string `toml:"data_dir"`
				} `toml:"storage"`
			} `toml:"a_memorix"`
		}
		if err := toml.Unmarshal(content, &config); err != nil {
			return layout, err
		}
		if directory := config.Memory.Storage.DataDir; directory != "" {
			if !filepath.IsAbs(directory) {
				directory = filepath.Join(root, directory)
			}
			memoryDirectories = append(memoryDirectories, filepath.Clean(directory))
		}
	}
	seen := map[string]bool{mainPath: true}
	for index, directory := range slices.Compact(memoryDirectories) {
		path, err := personMigrationAbs(filepath.Join(directory, "metadata", "metadata.db"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return layout, err
		}
		if !personMigrationWithin(root, path) {
			return layout, errors.New("记忆数据库位于实例外，需先将数据归入实例后再迁移")
		}
		if seen[path] {
			if path == mainPath {
				return layout, errors.New("记忆数据库与主数据库重复")
			}
			continue
		}
		seen[path] = true
		layout.participants = append(layout.participants, personMigrationParticipant{
			name: fmt.Sprintf("memory-%d", index), path: path, isMemory: true, build: buildPersonMemoryMigration,
		})
	}
	seenNames := map[string]bool{}
	for _, name := range extensions {
		if seenNames[name] {
			return layout, fmt.Errorf("重复迁移扩展：%s", name)
		}
		seenNames[name] = true
		factory, ok := personMigrationExtensions.Load(name)
		if !ok {
			return layout, fmt.Errorf("未登记迁移扩展：%s", name)
		}
		extension, err := factory.(func(string) (PersonMigrationExtension, error))(root)
		if err != nil {
			return layout, err
		}
		path, err := personMigrationAbs(extension.Database)
		if err != nil {
			return layout, err
		}
		if extension.BuildPlan == nil || seen[path] || !personMigrationWithin(root, path) {
			return layout, errors.New("迁移扩展数据库须属于实例且不可重复")
		}
		seen[path] = true
		layout.participants = append(layout.participants, personMigrationParticipant{name: name, path: path, build: extension.BuildPlan})
	}
	for index := 1; index < len(layout.participants); index++ {
		layout.participants[index].schema = fmt.Sprintf("migration_%d", index)
	}
	return layout, nil
}

func personMigrationURI(path, mode string) string {
	slashed := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed, RawQuery: "mode=" + mode}).String()
}

func (layout personMigrationLayout) transaction(ctx context.Context, write bool, run func(*sql.Conn) error) error {
	mode, begin := "ro", "BEGIN"
	if write {
		mode, begin = "rw", "BEGIN IMMEDIATE"
	}
	db, err := sql.Open("sqlite", personMigrationURI(layout.participants[0].path, mode)+"&_pragma=busy_timeout(30000)&_pragma=foreign_keys(0)")
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, participant := range layout.participants[1:] {
		if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS `+personQuote(participant.schema), personMigrationURI(participant.path, mode)); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	if err := run(conn); err != nil {
		return err
	}
	if write {
		_, err = conn.ExecContext(ctx, "COMMIT")
	}
	return err
}

func PreviewPersonMigration(ctx context.Context, root, platform, source, target string, decisions map[string]string, extensions ...string) (PersonMigrationPlan, error) {
	var preview PersonMigrationPlan
	if !identity.ValidID(platform) || !identity.ValidID(source) || !identity.ValidID(target) || source == target {
		return preview, errors.New("平台和来源、目标 ID 必须非空且无首尾空白，来源和目标必须不同")
	}
	layout, err := discoverPersonMigrationLayout(root, extensions)
	if err != nil {
		return preview, err
	}
	err = layout.transaction(ctx, false, func(conn *sql.Conn) error {
		var buildErr error
		preview, _, buildErr = layout.preview(ctx, conn, platform, source, target, decisions)
		return buildErr
	})
	return preview, err
}

func (layout personMigrationLayout) preview(ctx context.Context, conn *sql.Conn, platform, source, target string, decisions map[string]string) (PersonMigrationPlan, []*PersonDatabasePlan, error) {
	preview := PersonMigrationPlan{Version: 3, Root: layout.root, Conflicts: []PersonMigrationConflict{}, Configuration: layout.configuration, Decisions: decisions}
	if decisions == nil {
		preview.Decisions = map[string]string{}
	}
	state, err := identity.Load(ctx, conn, layout.root)
	if err != nil {
		return preview, nil, err
	}
	before, err := personMigrationHash(state)
	if err != nil {
		return preview, nil, err
	}
	if state.ResolveUserID(platform, source) != source {
		return preview, nil, errors.New("来源已迁移，请使用它当前指向的身份")
	}
	target = state.ResolveUserID(platform, target)
	if target == source {
		return preview, nil, errors.New("来源和目标已经是同一身份")
	}
	identityContext := PersonMigrationIdentity{
		Platform: platform, Source: source, Target: target,
		SourcePerson: identity.PersonID(platform, source), TargetPerson: identity.PersonID(platform, target),
		UserMap: map[string]string{source: target}, PrivateRoutes: state.PrivateRoutes,
		Sessions: map[string]string{}, PlatformSessions: []string{},
	}
	identityContext.PersonMap = map[string]string{identityContext.SourcePerson: identityContext.TargetPerson}
	preview.Identity = identityContext
	var plans []*PersonDatabasePlan
	for _, participant := range layout.participants {
		if participant.isMemory {
			if err := checkPersonMemoryWriteQueue(filepath.Dir(filepath.Dir(participant.path)), identityContext); err != nil {
				return preview, nil, err
			}
		}
		plan, err := newPersonDatabasePlan(ctx, conn, participant.schema, participant.name, participant.path, decisions)
		if err != nil {
			return preview, nil, err
		}
		if participant.name == "main" {
			if transports := state.UserTransports[platform]; transports[source] != "" {
				transports[target] = plan.Choose("user_transport", "transport_user_id", transports[source], transports[target]).(string)
				delete(transports, source)
			}
		}
		if err := participant.build(plan, identityContext); err != nil {
			return preview, nil, err
		}
		if participant.name != "main" && !participant.isMemory {
			fingerprint, err := plan.fingerprint()
			if err != nil {
				return preview, nil, err
			}
			plan.Fingerprint = &fingerprint
		}
		plans = append(plans, plan)
		preview.Databases = append(preview.Databases, plan.PersonMigrationDatabase)
		preview.Conflicts = append(preview.Conflicts, plan.conflicts...)
	}
	slices.SortFunc(preview.Conflicts, func(left, right PersonMigrationConflict) int { return strings.Compare(left.ID, right.ID) })
	expected := map[string]bool{}
	for _, conflict := range preview.Conflicts {
		if expected[conflict.ID] {
			return preview, nil, fmt.Errorf("冲突 ID 重复：%s", conflict.ID)
		}
		expected[conflict.ID] = true
	}
	for id, choice := range preview.Decisions {
		if !expected[id] || (choice != "source" && choice != "target") {
			return preview, nil, fmt.Errorf("冲突选择无效：%s", id)
		}
	}
	preview.Ready = len(expected) == len(preview.Decisions)
	if state.Aliases[platform] == nil {
		state.Aliases[platform] = map[string]string{}
	}
	state.Aliases[platform][source] = target
	for alias := range state.Aliases[platform] {
		state.Aliases[platform][alias] = state.ResolveUserID(platform, alias)
	}
	after, err := json.Marshal(state)
	if err != nil {
		return preview, nil, err
	}
	preview.Aliases = PersonMigrationAliases{Path: identity.AliasPath(layout.root), Before: before, After: string(after)}
	preview.Digest, err = preview.digest()
	return preview, plans, err
}

func (plan *PersonDatabasePlan) fingerprint() (string, error) {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	rows, err := plan.conn.QueryContext(plan.ctx, `SELECT name, type, sql FROM `+personQuote(plan.schema)+`.sqlite_schema ORDER BY name,type`)
	if err != nil {
		return "", err
	}
	objects, err := personMigrationRows(rows)
	rows.Close()
	if err != nil {
		return "", err
	}
	if err := encoder.Encode(objects); err != nil {
		return "", err
	}
	for _, object := range objects {
		if object["type"] != "table" {
			continue
		}
		entries, err := plan.Rows(object["name"].(string), "1")
		if err != nil {
			return "", err
		}
		if err := encoder.Encode(entries); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func buildPersonMainMigration(plan *PersonDatabasePlan, move PersonMigrationIdentity) error {
	count, err := plan.RowCount("person_info", "platform=? AND user_id=?", move.Platform, move.Source)
	if err != nil {
		return err
	}
	if count == 0 {
		return errors.New("来源身份没有人物资料")
	}
	return plan.Transform("person_info", "platform=? AND user_id IN (?,?)", []any{move.Platform, move.Source, move.Target}, func(row map[string]any) (map[string]any, error) {
		if row["user_id"] == move.Source {
			row["user_id"], row["person_id"] = move.Target, move.TargetPerson
		}
		return row, nil
	}, func(key string, source, target map[string]any) (map[string]any, error) {
		cards, err := mergePersonGroupCards(plan, key, source["group_cardname"], target["group_cardname"])
		if err != nil {
			return nil, err
		}
		delete(source, "group_cardname")
		result := plan.Combine(key, source, target)
		result["group_cardname"] = cards
		return result, nil
	}, false)
}

func mergePersonGroupCards(plan *PersonDatabasePlan, key string, source, target any) (string, error) {
	type card struct {
		GroupID string `json:"group_id"`
		Name    string `json:"group_cardname"`
	}
	parse := func(raw any) ([]card, error) {
		result := []card{}
		if raw == nil || raw == "" {
			return result, nil
		}
		text, ok := raw.(string)
		if !ok {
			return nil, errors.New("群名片不是 JSON 文本")
		}
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, entry := range result {
			if entry.GroupID == "" || seen[entry.GroupID] {
				return nil, errors.New("群名片缺少群 ID 或群 ID 重复")
			}
			seen[entry.GroupID] = true
		}
		return result, nil
	}
	destination, err := parse(target)
	if err != nil {
		return "", err
	}
	incoming, err := parse(source)
	if err != nil {
		return "", err
	}
	for _, entry := range incoming {
		index := slices.IndexFunc(destination, func(existing card) bool { return existing.GroupID == entry.GroupID })
		if index == -1 {
			destination = append(destination, entry)
		} else {
			destination[index].Name = plan.Choose(key, "group_cardname."+entry.GroupID, entry.Name, destination[index].Name).(string)
		}
	}
	encoded, err := json.Marshal(destination)
	return string(encoded), err
}

type personMigrationJob struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Root    string `json:"root"`
}

func ExecutePersonMigration(ctx context.Context, root string, reviewed PersonMigrationPlan) (string, error) {
	digest, err := reviewed.digest()
	if err != nil || reviewed.Version != 3 || reviewed.Digest != digest || !reviewed.Ready || len(reviewed.Databases) == 0 || reviewed.Databases[0].Name != "main" {
		return "", errors.New("迁移计划已修改或未确认全部冲突，请重新预览")
	}
	move := reviewed.Identity
	if !identity.ValidID(move.Platform) || !identity.ValidID(move.Source) || !identity.ValidID(move.Target) || move.Source == move.Target {
		return "", errors.New("迁移身份无效，请重新预览")
	}
	var extensions []string
	for _, database := range reviewed.Databases[1:] {
		if !strings.HasPrefix(database.Name, "memory-") {
			extensions = append(extensions, database.Name)
		}
	}
	layout, err := discoverPersonMigrationLayout(root, extensions)
	if err != nil {
		return "", err
	}
	if reviewed.Root != layout.root || len(reviewed.Databases) != len(layout.participants) {
		return "", ErrPersonMigrationStale
	}
	for index, participant := range layout.participants {
		if database := reviewed.Databases[index]; database.Path != participant.path || database.Name != participant.name {
			return "", ErrPersonMigrationStale
		}
	}
	configurationHash, _ := personMigrationHash(layout.configuration)
	reviewedHash, _ := personMigrationHash(reviewed.Configuration)
	if configurationHash != reviewedHash {
		return "", ErrPersonMigrationStale
	}
	jobParent := filepath.Join(layout.root, "data", "person-migrations")
	if err := os.MkdirAll(jobParent, 0o700); err != nil {
		return "", err
	}
	jobParent, err = personMigrationAbs(jobParent)
	if err != nil || !personMigrationWithin(layout.root, jobParent) {
		return "", errors.New("人物迁移任务目录位于实例外")
	}
	jobID := strings.ReplaceAll(uuid.NewString(), "-", "")
	job := filepath.Join(jobParent, jobID)
	if err := os.Mkdir(job, 0o700); err != nil {
		return "", err
	}
	statePath := filepath.Join(job, "state.json")
	state := personMigrationJob{Version: 2, Status: "validating", Root: layout.root}
	if err := WritePersonMigrationJSON(statePath, state); err != nil {
		return "", err
	}
	err = layout.transaction(ctx, true, func(conn *sql.Conn) error {
		lockedLayout, err := discoverPersonMigrationLayout(layout.root, extensions)
		if err != nil {
			return err
		}
		lockedHash, _ := personMigrationHash(lockedLayout.configuration)
		if lockedHash != configurationHash || len(lockedLayout.participants) != len(layout.participants) {
			return ErrPersonMigrationStale
		}
		for index, participant := range lockedLayout.participants {
			if original := layout.participants[index]; participant.name != original.name || participant.path != original.path {
				return ErrPersonMigrationStale
			}
		}
		fresh, plans, err := layout.preview(ctx, conn, move.Platform, move.Source, move.Target, reviewed.Decisions)
		if err != nil {
			return errors.Join(ErrPersonMigrationStale, err)
		}
		freshConflicts, _ := personMigrationHash(fresh.Conflicts)
		reviewedConflicts, _ := personMigrationHash(reviewed.Conflicts)
		if !fresh.Ready || fresh.Aliases.Before != reviewed.Aliases.Before || freshConflicts != reviewedConflicts {
			return ErrPersonMigrationStale
		}
		for index, database := range fresh.Databases {
			previous := reviewed.Databases[index].Fingerprint
			if database.Fingerprint != nil && (previous == nil || *previous != *database.Fingerprint) {
				return ErrPersonMigrationStale
			}
		}
		if err := WritePersonMigrationJSON(filepath.Join(job, "plan.json"), fresh); err != nil {
			return err
		}
		state.Status = "applying"
		if err := WritePersonMigrationJSON(statePath, state); err != nil {
			return err
		}
		for _, plan := range plans {
			if err := plan.apply(); err != nil {
				return err
			}
			markerTable := plan.qualified("person_migration_commits")
			if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+markerTable+` (job_id TEXT PRIMARY KEY)`); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO `+markerTable+` VALUES (?)`, jobID); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS main.person_identity_state (id INTEGER PRIMARY KEY CHECK(id=1), payload TEXT NOT NULL)`); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO main.person_identity_state (id,payload) VALUES (1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, fresh.Aliases.After)
		return err
	})
	if err != nil {
		state.Status = "failed"
		return "", errors.Join(err, WritePersonMigrationJSON(statePath, state))
	}
	state.Status = "completed"
	if err := WritePersonMigrationJSON(statePath, state); err != nil {
		return job, fmt.Errorf("数据库已提交，任务状态写入失败；请执行 recover：%w", err)
	}
	return job, nil
}

func RecoverPersonMigration(ctx context.Context, root, job string) error {
	root, err := personMigrationAbs(root)
	if err != nil {
		return err
	}
	job, err = personMigrationAbs(job)
	if err != nil {
		return err
	}
	if filepath.Dir(job) != filepath.Join(root, "data", "person-migrations") {
		return errors.New("任务不属于当前实例")
	}
	statePath := filepath.Join(job, "state.json")
	var state personMigrationJob
	if err := ReadPersonMigrationJSON(statePath, &state); err != nil {
		return err
	}
	if state.Version != 2 || state.Root != root {
		return errors.New("任务版本或实例不匹配")
	}
	var plan PersonMigrationPlan
	if err := ReadPersonMigrationJSON(filepath.Join(job, "plan.json"), &plan); errors.Is(err, os.ErrNotExist) {
		state.Status = "failed"
		return WritePersonMigrationJSON(statePath, state)
	} else if err != nil {
		return err
	}
	if len(plan.Databases) == 0 || plan.Root != root {
		return errors.New("任务缺少参与数据库")
	}
	committed := 0
	for _, database := range plan.Databases {
		path, err := personMigrationAbs(database.Path)
		if err != nil {
			return err
		}
		if !personMigrationWithin(root, path) {
			return errors.New("迁移数据库位于实例外")
		}
		marked, err := personMigrationMarked(ctx, path, filepath.Base(job))
		if err != nil {
			return err
		}
		if marked {
			committed++
		}
	}
	if committed > 0 && committed != len(plan.Databases) {
		return errors.New("参与数据库提交状态不一致，请依据任务记录修复；不会覆盖在线数据")
	}
	state.Status = "failed"
	if committed == len(plan.Databases) {
		state.Status = "completed"
	}
	return WritePersonMigrationJSON(statePath, state)
}

func personMigrationMarked(ctx context.Context, path, jobID string) (bool, error) {
	db, err := sql.Open("sqlite", personMigrationURI(path, "ro"))
	if err != nil {
		return false, err
	}
	defer db.Close()
	var marked bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name='person_migration_commits' AND type='table')`).Scan(&marked); err != nil || !marked {
		return false, err
	}
	err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM person_migration_commits WHERE job_id=?)`, jobID).Scan(&marked)
	return marked, err
}
