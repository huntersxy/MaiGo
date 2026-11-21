package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	memory "MaiGo/internal/common/database"
	migration "MaiGo/internal/common/database/migrations"
	"MaiGo/internal/person_info"
	webuichat "MaiGo/internal/webui/routers/chat"

	"github.com/go-chi/chi/v5"
)

func TestChatTargetsFindsUsersWithoutPrivateSessions(t *testing.T) {
	root := t.TempDir()
	db, err := memory.OpenDB(filepath.Join(root, "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := migration.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	manager, err := memory.NewManager(db, root)
	if err != nil {
		t.Fatal(err)
	}
	alias := "Known Name"
	if err := db.Create(&personinfo.PersonInfo{PersonID: "person", Platform: "qq", UserID: "openid", UserNickname: "Nickname", PersonName: &alias}).Error; err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	webuichat.Mount(router, webuichat.Deps{Memory: manager})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/targets?target_type=user&platform=qq&search=Known", nil))
	var result struct {
		Success bool
		Targets []map[string]string
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || !result.Success || len(result.Targets) != 1 {
		t.Fatalf("unexpected targets response: %d %s (%v)", response.Code, response.Body, err)
	}
	if target := result.Targets[0]; target["id"] != "openid" || target["name"] != "Nickname" || target["platform"] != "qq" || target["type"] != "user" {
		t.Fatalf("unexpected target: %v", target)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/targets?target_type=user&limit=0", nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid limit was not rejected: %d %s", response.Code, response.Body)
	}
}
