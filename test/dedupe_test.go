package test

import (
	"testing"
	"time"

	platformio "MaiGo/internal/platform_io"
)

func TestDeduplicatorFirstSeenTrue(t *testing.T) {
	dedupe, err := platformio.NewMessageDeduplicator(time.Minute, 100)
	if err != nil {
		t.Fatalf("NewMessageDeduplicator failed: %v", err)
	}
	if !dedupe.MarkSeen("msg-1") {
		t.Fatal("first MarkSeen should return true")
	}
	if dedupe.MarkSeen("msg-1") {
		t.Fatal("second MarkSeen should return false")
	}
}

func TestDeduplicatorExpires(t *testing.T) {
	dedupe, err := platformio.NewMessageDeduplicator(20*time.Millisecond, 100)
	if err != nil {
		t.Fatalf("NewMessageDeduplicator failed: %v", err)
	}
	if !dedupe.MarkSeen("msg-1") {
		t.Fatal("first MarkSeen should return true")
	}
	time.Sleep(50 * time.Millisecond)
	if !dedupe.MarkSeen("msg-1") {
		t.Fatal("MarkSeen after TTL should return true")
	}
}

func TestDeduplicatorRejectsInvalidArguments(t *testing.T) {
	if _, err := platformio.NewMessageDeduplicator(0, 10); err == nil {
		t.Fatal("expected error for zero ttl")
	}
	if _, err := platformio.NewMessageDeduplicator(time.Minute, 0); err == nil {
		t.Fatal("expected error for zero maxEntries")
	}
}

func TestDeduplicatorEvictsWhenFull(t *testing.T) {
	dedupe, err := platformio.NewMessageDeduplicator(time.Minute, 2)
	if err != nil {
		t.Fatalf("NewMessageDeduplicator failed: %v", err)
	}
	dedupe.MarkSeen("a")
	dedupe.MarkSeen("b")
	dedupe.MarkSeen("c")
	// 容量 2：最早的 a 应已被淘汰，重新标记返回 true
	if !dedupe.MarkSeen("a") {
		t.Fatal("expected a to be evicted and re-markable")
	}
	if dedupe.MarkSeen("c") {
		t.Fatal("c should still be deduplicated")
	}
}

func TestDeduplicatorClearResets(t *testing.T) {
	dedupe, err := platformio.NewMessageDeduplicator(time.Minute, 10)
	if err != nil {
		t.Fatalf("NewMessageDeduplicator failed: %v", err)
	}
	dedupe.MarkSeen("a")
	dedupe.Clear()
	if !dedupe.MarkSeen("a") {
		t.Fatal("Clear should forget seen keys")
	}
}

func TestDefaultDeduplicatorWorks(t *testing.T) {
	dedupe, err := platformio.DefaultDeduplicator()
	if err != nil {
		t.Fatalf("DefaultDeduplicator failed: %v", err)
	}
	if !dedupe.MarkSeen("x") || dedupe.MarkSeen("x") {
		t.Fatal("default deduplicator should dedupe repeated keys")
	}
}
