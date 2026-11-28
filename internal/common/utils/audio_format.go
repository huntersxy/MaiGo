package utils

// DetectAudioFormat 根据文件头返回扩展名与 MIME，对齐 Python detect_audio_format
func DetectAudioFormat(data []byte) (extension string, mimeType string) {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		return "wav", "audio/wav"
	}
	if len(data) >= 4 && string(data[:4]) == "OggS" {
		return "ogg", "audio/ogg"
	}
	if len(data) >= 4 && string(data[:4]) == "fLaC" {
		return "flac", "audio/flac"
	}
	if len(data) >= 8 && string(data[4:8]) == "ftyp" {
		return "mp4", "audio/mp4"
	}
	if len(data) >= 4 && data[0] == 0x1a && data[1] == 0x45 && data[2] == 0xdf && data[3] == 0xa3 {
		return "webm", "audio/webm"
	}
	if len(data) >= 3 && string(data[:3]) == "ID3" {
		return "mp3", "audio/mpeg"
	}
	if len(data) >= 2 && data[0] == 0xFF && data[1]&0xE6 == 0xE2 {
		return "mp3", "audio/mpeg"
	}
	if len(data) >= 2 && (data[0] == 0xFF && (data[1] == 0xF1 || data[1] == 0xF9)) {
		return "aac", "audio/aac"
	}
	return "", "application/octet-stream"
}