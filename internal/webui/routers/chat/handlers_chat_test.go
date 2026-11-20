package chat

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/database/migrations"
)

func TestChatSessionMessagesRequiresAuthAndReadsSessionID(t *testing.T) {
	root := t.TempDir()
	db, err := memory.OpenDB(filepath.Join(root, "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := migration.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	manager, err := memory.NewManager(db, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		authenticated bool
		wantStatus    int
	}{
		{false, http.StatusUnauthorized},
		{true, http.StatusNotFound},
	} {
		router := chi.NewRouter()
		Mount(router, Deps{Memory: manager, RequireAuth: func(w http.ResponseWriter, _ *http.Request) bool {
			if !tc.authenticated {
				w.WriteHeader(http.StatusUnauthorized)
			}
			return tc.authenticated
		}})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sessions/missing-session/messages", nil))
		if response.Code != tc.wantStatus {
			t.Fatalf("authenticated=%t: got %d, want %d", tc.authenticated, response.Code, tc.wantStatus)
		}
	}
}
