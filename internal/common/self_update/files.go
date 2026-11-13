package selfupdate

import (
	"os"
	"path/filepath"
)

func readLocalFile(root, name string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
