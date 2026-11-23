package test

import (
	"errors"
	"testing"

	"MaiGo/internal/runtime/execution/committer"
	"MaiGo/internal/runtime/execution/policy"
)

func TestNativeCommitterOrdersReservationsAndDrains(t *testing.T) {
	native := committer.NewNative()
	reserve := func(kind string, target int64) func(func()) {
		t.Helper()
		finish, err := native.Reserve(kind, target)
		if err != nil {
			t.Fatal(err)
		}
		return finish
	}
	first := reserve("group", 1)
	filtered := reserve("group", 1)
	last := reserve("group", 1)
	other := reserve("private", 2)
	result := make(chan string, 4)
	last(func() { result <- "last" })
	other(func() { result <- "other" })
	if got := waitPolicyResult(t, result); got != "other" {
		t.Fatalf("expected another lane to proceed, got %q", got)
	}
	first(func() { result <- "first" })
	filtered(nil)
	nativeStopped := make(chan error, 1)
	go func() { nativeStopped <- native.Shutdown(t.Context()) }()
	if err := waitPolicyResult(t, nativeStopped); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first", "last"} {
		if got := waitPolicyResult(t, result); got != want {
			t.Fatalf("submission order = %q, want %q", got, want)
		}
	}
	if _, err := native.Reserve("group", 1); !errors.Is(err, policy.ErrClosed) {
		t.Fatalf("stopped committer still accepts work: %v", err)
	}
}
