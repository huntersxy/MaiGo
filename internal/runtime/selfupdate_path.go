package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

func filepathCleanEval(path string) (string, error) {
	path = filepath.Clean(path)
	return filepath.EvalSymlinks(path)
}

func samePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	aClean, errA := filepathCleanEval(a)
	bClean, errB := filepathCleanEval(b)
	if errA == nil {
		a = aClean
	} else {
		a = filepath.Clean(a)
	}
	if errB == nil {
		b = bClean
	} else {
		b = filepath.Clean(b)
	}
	if strings.EqualFold(a, b) {
		return true
	}
	return false
}

func runnerSupervised(base []string) bool {
	for _, kv := range base {
		key, val, ok := strings.Cut(kv, "=")
		if ok && key == EnvRunnerSupervised && strings.TrimSpace(val) == "1" {
			return true
		}
	}
	return strings.TrimSpace(os.Getenv(EnvRunnerSupervised)) == "1"
}
