package contract

import (
	"net/http"
	"strings"
	"testing"
)

func TestProviderRequestIDPrefersStandardHeader(t *testing.T) {
	header := http.Header{}
	header.Set("X-Oneapi-Request-Id", "oneapi-1")
	header.Set("X-Request-Id", "req-1")
	if got := ProviderRequestID(header); got != "req-1" {
		t.Fatalf("ProviderRequestID = %q, want %q", got, "req-1")
	}
}

func TestProviderRequestIDFallsBackToGatewayHeader(t *testing.T) {
	header := http.Header{}
	header.Set("CF-Ray", "8f2c1d3e4a5b6c7d-SJC")
	if got := ProviderRequestID(header); got != "8f2c1d3e4a5b6c7d-SJC" {
		t.Fatalf("ProviderRequestID = %q", got)
	}
}

func TestProviderRequestIDTrimsAndLimits(t *testing.T) {
	header := http.Header{}
	header.Set("Request-Id", "  abc\x00def  ")
	if got := ProviderRequestID(header); got != "abcdef" {
		t.Fatalf("ProviderRequestID = %q, want %q", got, "abcdef")
	}
	header.Set("Request-Id", strings.Repeat("a", 200))
	if got := ProviderRequestID(header); len(got) != providerRequestIDMaxLen {
		t.Fatalf("ProviderRequestID length = %d, want %d", len(got), providerRequestIDMaxLen)
	}
}

func TestProviderRequestIDMissing(t *testing.T) {
	if got := ProviderRequestID(http.Header{}); got != "" {
		t.Fatalf("ProviderRequestID = %q, want empty", got)
	}
}
