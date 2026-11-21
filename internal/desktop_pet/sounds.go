package desktoppet

import (
	"encoding/base64"
	"os"

	petassets "MaiGo/src/desktop_pet"
)

func SoundPreview(sound string) (string, error) {
	files := map[string]string{
		"wood": "wooden-pop.wav", "bubbles": "soft-bubbles.wav",
		"bell": "warm-bell.wav", "water": "water-drop.wav",
	}
	name := files[sound]
	if name == "" {
		return "", os.ErrNotExist
	}
	data, err := petassets.ReadSound(name)
	if err != nil {
		return "", err
	}
	return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(data), nil
}
