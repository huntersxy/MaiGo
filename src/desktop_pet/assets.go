package petassets

import "embed"

//go:embed web/sounds/*.wav
var sounds embed.FS

func ReadSound(name string) ([]byte, error) {
	return sounds.ReadFile("web/sounds/" + name)
}
