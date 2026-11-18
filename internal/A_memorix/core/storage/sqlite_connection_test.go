package storage

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Manager {
	t.Helper()
	manager := NewManager(filepath.Join(t.TempDir(), "metadata.db"), 10*time.Second)
	t.Cleanup(func() { _ = manager.CloseAll() })
	conn, err := manager.Conn()
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if _, err := conn.Exec("CREATE TABLE items (value INTEGER NOT NULL)"); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return manager
}

func countItems(t *testing.T, manager *Manager) int {
	t.Helper()
	conn, err := manager.Conn()
	if err != nil {
		t.Fatalf("获取连接失败: %v", err)
	}
	var total int
	if err := conn.QueryRow("SELECT COUNT(*) FROM items").Scan(&total); err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	return total
}

func TestConcurrentTransactionsDoNotInterleave(t *testing.T) {
	manager := openTestStore(t)

	const workers = 24
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			errCh <- manager.Transaction(true, func(tx *Tx) error {
				if _, err := tx.Exec("INSERT INTO items(value) VALUES (?)", value); err != nil {
					return err
				}
				time.Sleep(time.Millisecond)
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("并发事务失败: %v", err)
		}
	}
	if got := countItems(t, manager); got != workers {
		t.Fatalf("提交条数 = %d, 期望 %d", got, workers)
	}
}

func TestNestedScopeRollsBackSavepointOnly(t *testing.T) {
	manager := openTestStore(t)

	err := manager.Transaction(true, func(tx *Tx) error {
		if _, err := tx.Exec("INSERT INTO items(value) VALUES (1)"); err != nil {
			return err
		}
		innerErr := TransactionOn(tx.Conn(), true, func(inner *Tx) error {
			if _, err := inner.Exec("INSERT INTO items(value) VALUES (2)"); err != nil {
				return err
			}
			return errors.New("内层失败")
		})
		if innerErr == nil {
			t.Fatal("内层事务应返回错误")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("外层事务失败: %v", err)
	}
	if got := countItems(t, manager); got != 1 {
		t.Fatalf("提交条数 = %d, 期望 1", got)
	}
}

func TestRollbackRequestAbortsTransaction(t *testing.T) {
	manager := openTestStore(t)

	err := manager.Transaction(true, func(tx *Tx) error {
		if _, err := tx.Exec("INSERT INTO items(value) VALUES (1)"); err != nil {
			return err
		}
		return tx.Conn().Rollback()
	})
	if !errors.Is(err, ErrRollbackRequested) {
		t.Fatalf("错误 = %v, 期望 ErrRollbackRequested", err)
	}
	if got := countItems(t, manager); got != 0 {
		t.Fatalf("提交条数 = %d, 期望 0", got)
	}
}

func TestTransactionReleasesSessionConnection(t *testing.T) {
	manager := openTestStore(t)

	for i := 0; i < 3; i++ {
		if err := manager.Transaction(true, func(tx *Tx) error {
			_, err := tx.Exec("INSERT INTO items(value) VALUES (?)", i)
			return err
		}); err != nil {
			t.Fatalf("第 %d 次事务失败: %v", i, err)
		}
	}
	if got := countItems(t, manager); got != 3 {
		t.Fatalf("提交条数 = %d, 期望 3", got)
	}
}
