package transport

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const (
	namedPipePrefix        = `\\.\pipe\`
	namedPipeAltPrefix     = `//./pipe/`
	defaultNamedPipePrefix = "maibot-plugin"
)

var namedPipeSanitizeRE = regexp.MustCompile(`[^0-9A-Za-z._-]+`)

// IsNamedPipeAddress 判断是否为 Windows Named Pipe 地址（对齐 MaiBot transport.factory）
func IsNamedPipeAddress(addr string) bool {
	lower := strings.ToLower(strings.TrimSpace(addr))
	return strings.HasPrefix(lower, strings.ToLower(namedPipePrefix)) ||
		strings.HasPrefix(lower, strings.ToLower(namedPipeAltPrefix))
}

// NormalizePipeAddress 规范化 Named Pipe 地址，对齐 named_pipe._normalize_pipe_address
func NormalizePipeAddress(pipeName string) string {
	pipeName = strings.TrimSpace(pipeName)
	if IsNamedPipeAddress(pipeName) {
		return pipeName
	}

	var sanitized string
	if pipeName != "" {
		sanitized = namedPipeSanitizeRE.ReplaceAllString(pipeName, "-")
		sanitized = strings.Trim(sanitized, "-.")
	}
	if sanitized == "" {
		sanitized = fmt.Sprintf("%s-%d-%s", defaultNamedPipePrefix, os.Getpid(), uuid.New().String()[:8])
	}
	return namedPipePrefix + sanitized
}
