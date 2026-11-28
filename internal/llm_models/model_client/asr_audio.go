package model_client

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"MaiGo/internal/common/utils"
)

// TranscriptionFilename 根据文件头为转写接口还原文件名，对齐 Python asr_audio.transcription_filename
func TranscriptionFilename(data []byte) string {
	extension, _ := utils.DetectAudioFormat(data)
	if extension == "" {
		return "audio"
	}
	return "audio." + extension
}

// AudioFile 上传转写用的文件名、字节与 MIME
type AudioFile struct {
	Name     string
	Data     []byte
	MimeType string
}

// PrepareDeepseekAudio 准备 DeepSeek 接受的 Ogg Opus 或 16 位 PCM WAV，对齐 Python prepare_deepseek_audio
func PrepareDeepseekAudio(data []byte) (AudioFile, error) {
	if len(data) > 27 && bytes.HasPrefix(data, []byte("OggS")) {
		offset := 27 + int(data[26])
		if offset+8 <= len(data) && bytes.Equal(data[offset:offset+8], []byte("OpusHead")) {
			if len(data) > 8*1024*1024 {
				return AudioFile{}, fmt.Errorf("DeepSeek ASR 音频超过 8 MiB")
			}
			return AudioFile{Name: "audio.ogg", Data: data, MimeType: "audio/ogg"}, nil
		}
	}
	if len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		if wav, err := parsePCMWAV(data); err == nil {
			if wav.frames > 120*wav.sampleRate {
				return AudioFile{}, fmt.Errorf("DeepSeek ASR 音频超过 120 秒")
			}
			if wav.sampleWidth == 2 && (wav.channels == 1 || wav.channels == 2) &&
				containsInt([]int{8000, 16000, 22050, 24000, 32000, 44100, 48000}, wav.sampleRate) &&
				len(data) <= 8*1024*1024 {
				return AudioFile{Name: "audio.wav", Data: data, MimeType: "audio/wav"}, nil
			}
		}
	}
	pcm, err := decodeAudioToPCMS16LE(data, 121*time.Second)
	if err != nil {
		return AudioFile{}, err
	}
	if len(pcm) > 120*16000*2 {
		return AudioFile{}, fmt.Errorf("DeepSeek ASR 音频超过 120 秒")
	}
	wavBytes, err := encodePCMS16WAV(pcm, 16000, 1)
	if err != nil {
		return AudioFile{}, err
	}
	return AudioFile{Name: "audio.wav", Data: wavBytes, MimeType: "audio/wav"}, nil
}

type pcmWAVHeader struct {
	frames      int
	sampleRate  int
	channels    int
	sampleWidth int
}

func parsePCMWAV(data []byte) (pcmWAVHeader, error) {
	if len(data) < 44 {
		return pcmWAVHeader{}, fmt.Errorf("WAV 头不完整")
	}
	channels := int(binary.LittleEndian.Uint16(data[22:24]))
	sampleRate := int(binary.LittleEndian.Uint32(data[24:28]))
	sampleWidth := int(binary.LittleEndian.Uint16(data[34:36]))
	dataSize := int(binary.LittleEndian.Uint32(data[40:44]))
	frames := dataSize / max(1, channels*sampleWidth)
	return pcmWAVHeader{frames: frames, sampleRate: sampleRate, channels: channels, sampleWidth: sampleWidth}, nil
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func decodeAudioToPCMS16LE(data []byte, limit time.Duration) ([]byte, error) {
	ffmpeg, err := locateFFmpeg()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-v", "error", "-protocol_whitelist", "pipe",
		"-i", "pipe:0", "-map", "0:a:0",
		"-t", fmt.Sprintf("%.0f", limit.Seconds()),
		"-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1",
	)
	cmd.Stdin = bytes.NewReader(data)
	output, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		if stderr == "" {
			stderr = err.Error()
		}
		return nil, fmt.Errorf("语音解码失败：%s", stderr)
	}
	if len(output) == 0 {
		return nil, fmt.Errorf("语音解码失败")
	}
	return output, nil
}

func encodePCMS16WAV(pcm []byte, sampleRate, channels int) ([]byte, error) {
	var buf bytes.Buffer
	dataSize := len(pcm)
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*channels*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(channels*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	if _, err := io.Copy(&buf, bytes.NewReader(pcm)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func locateFFmpeg() (string, error) {
	if ffmpeg, err := exec.LookPath("ffmpeg"); err == nil {
		return ffmpeg, nil
	}
	projectRoot := strings.TrimSpace(os.Getenv("MAIBOT_PLUGIN_PROJECT_ROOT"))
	if projectRoot != "" {
		matches, _ := filepath.Glob(filepath.Join(projectRoot, "imageio_ffmpeg", "binaries", "ffmpeg-*"))
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	return "", fmt.Errorf("语音解码需要 FFmpeg")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
