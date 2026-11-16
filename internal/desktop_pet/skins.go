package desktoppet

// 声明式皮肤包校验与安装，对齐 src/desktop_pet/skins.py；仅接收本地图片，不执行皮肤内代码

import (
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
)

const (
	// skinMaxBytes 皮肤图片与压缩包的体积上限
	skinMaxBytes = 32 * 1024 * 1024
	// skinManifestBytes skin.json 的体积上限
	skinManifestBytes = 256 * 1024
	// skinMaxPixels 单张图片的像素上限
	skinMaxPixels = 16_000_000
	// skinMaxAnimationMS 单个动画的时长上限
	skinMaxAnimationMS = 30000
)

var (
	skinStates      = []string{"idle", "thinking", "talking", "dragging"}
	skinEvents      = map[string]struct{}{"click": {}, "happy": {}, "error": {}, "greeting": {}}
	skinMotions     = map[string]struct{}{"none": {}, "bob": {}, "sway": {}, "hop": {}, "shake": {}, "pulse": {}}
	skinIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)
	skinAnimPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

// SkinCanvas 皮肤画布，单位为 CSS 像素
type SkinCanvas struct {
	Width        int   `json:"width"`
	Height       int   `json:"height"`
	Anchor       []int `json:"anchor"`
	BubbleAnchor []int `json:"bubble_anchor"`
	Hitbox       []int `json:"hitbox"`
}

// SkinFrame 动画帧
type SkinFrame struct {
	File       string `json:"file"`
	Rect       []int  `json:"rect"`
	DurationMS int    `json:"duration_ms"`
}

// SkinAnimation 规范化后的动画
type SkinAnimation struct {
	Sequence   []SkinFrame `json:"sequence"`
	DurationMS int         `json:"duration_ms"`
	Motion     string      `json:"motion"`
}

// SkinDocument 窗口可直接安装的皮肤包
type SkinDocument struct {
	SchemaVersion int                      `json:"schema_version"`
	ID            string                   `json:"id"`
	Name          string                   `json:"name"`
	Version       string                   `json:"version"`
	Author        string                   `json:"author"`
	Canvas        SkinCanvas               `json:"canvas"`
	Animations    map[string]SkinAnimation `json:"animations"`
	States        map[string]string        `json:"states"`
	Events        map[string]string        `json:"events"`
	Resources     map[string]string        `json:"resources"`
}

// SkinList 皮肤清单与逐包错误
type SkinList struct {
	Skins  []SkinDocument `json:"skins"`
	Errors []SkinError    `json:"errors"`
}

// SkinError 单个皮肤包的校验失败信息
type SkinError struct {
	Package string `json:"package"`
	Error   string `json:"error"`
}

type skinManifest struct {
	SchemaVersion int                         `json:"schema_version"`
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	Version       string                      `json:"version"`
	Author        any                         `json:"author"`
	Canvas        SkinCanvas                  `json:"canvas"`
	States        map[string]string           `json:"states"`
	Events        map[string]string           `json:"events"`
	Animations    map[string]skinAnimationRaw `json:"animations"`
}

type skinAnimationRaw struct {
	Motion      string          `json:"motion"`
	Sequence    *[]skinFrameRaw `json:"sequence"`
	File        string          `json:"file"`
	FrameWidth  *int            `json:"frame_width"`
	FrameHeight *int            `json:"frame_height"`
	Frames      *int            `json:"frames"`
	FPS         *int            `json:"fps"`
}

type skinFrameRaw struct {
	File       string `json:"file"`
	Rect       []int  `json:"rect"`
	DurationMS *int   `json:"duration_ms"`
}

// builtinSkin 内置皮肤，对齐 skins.BUILTIN
func builtinSkin() SkinDocument {
	durations := map[string]int{
		"idle": 3200, "thinking": 1100, "talking": 1200, "dragging": 1000,
		"squish": 400, "happy": 650, "shake": 500, "blink": 200,
	}
	animations := make(map[string]SkinAnimation, len(durations))
	for name, duration := range durations {
		animations[name] = SkinAnimation{DurationMS: duration}
	}
	states := make(map[string]string, len(skinStates))
	for _, name := range skinStates {
		states[name] = name
	}
	return SkinDocument{
		SchemaVersion: 1,
		ID:            "builtin",
		Name:          "麦麦（内置）",
		Version:       "1",
		Canvas: SkinCanvas{
			Width:        150,
			Height:       107,
			Anchor:       []int{75, 107},
			BubbleAnchor: []int{75, 0},
			Hitbox:       []int{0, 0, 150, 107},
		},
		Animations: animations,
		States:     states,
		Events: map[string]string{
			"click": "squish", "happy": "happy", "error": "shake", "greeting": "happy",
		},
		Resources: map[string]string{},
	}
}

// bundledSkinManifestPaths 返回程序自带皮肤清单，随镜像放在 desktop_pet/presets
func bundledSkinManifestPaths() []string {
	patterns := make([]string, 0, 4)
	if root := strings.TrimSpace(os.Getenv("MAIBOT_DESKTOP_PET_PRESETS")); root != "" {
		patterns = append(patterns, filepath.Join(root, "*", "skin.json"))
	}
	if cwd, err := os.Getwd(); err == nil {
		patterns = append(patterns,
			filepath.Join(cwd, "desktop_pet", "presets", "*", "skin.json"),
			filepath.Join(cwd, "internal", "desktop_pet", "presets", "*", "skin.json"),
		)
	}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		patterns = append(patterns,
			filepath.Join(dir, "desktop_pet", "presets", "*", "skin.json"),
			filepath.Join(dir, "presets", "*", "skin.json"),
		)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if _, dup := seen[match]; dup {
				continue
			}
			seen[match] = struct{}{}
			out = append(out, match)
		}
	}
	return out
}

// skinManifestPaths 列出候选皮肤清单，对齐 skin_directories
func skinManifestPaths(root string) []string {
	patterns := []string{
		filepath.Join(root, "data", "desktop_pet", "skins", "*", "skin.json"),
		filepath.Join(root, "plugins", "*", "skins", "*", "skin.json"),
	}
	out := bundledSkinManifestPaths()
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		out = append(out, matches...)
	}
	sort.Strings(out)
	return out
}

// findSkin 对齐 find_skin：按清单 ID 查找唯一皮肤，builtin 直接返回内置皮肤
func findSkin(root, skinID string, assets bool) (SkinDocument, error) {
	if skinID == "builtin" {
		return builtinSkin(), nil
	}
	matches := make([]string, 0, 1)
	for _, manifestPath := range skinManifestPaths(root) {
		stat, err := os.Stat(manifestPath)
		if err != nil || stat.Size() > skinManifestBytes {
			continue
		}
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var probe struct {
			ID string `json:"id"`
		}
		if err := sonic.Unmarshal(raw, &probe); err != nil {
			continue
		}
		if probe.ID == skinID {
			matches = append(matches, manifestPath)
		}
	}
	if len(matches) != 1 {
		return SkinDocument{}, fmt.Errorf("皮肤 %s 不存在或 ID 重复", skinID)
	}
	return loadSkin(matches[0], assets)
}

// ListSkins 对齐 list_skins：内置皮肤在首位，非法皮肤进 errors
func ListSkins(root string) SkinList {
	result := SkinList{Skins: []SkinDocument{builtinSkin()}, Errors: []SkinError{}}
	for _, manifestPath := range skinManifestPaths(root) {
		skin, err := loadSkin(manifestPath, false)
		if err == nil {
			_, err = findSkin(root, skin.ID, false)
		}
		if err != nil {
			result.Errors = append(result.Errors, SkinError{
				Package: filepath.Base(filepath.Dir(manifestPath)),
				Error:   err.Error(),
			})
			continue
		}
		result.Skins = append(result.Skins, skin)
	}
	return result
}

func loadSkin(manifestPath string, assets bool) (SkinDocument, error) {
	stat, err := os.Stat(manifestPath)
	if err != nil {
		return SkinDocument{}, err
	}
	if stat.Size() > skinManifestBytes {
		return SkinDocument{}, fmt.Errorf("skin.json 不能超过 256 KiB")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return SkinDocument{}, err
	}
	var manifest skinManifest
	if err := sonic.Unmarshal(raw, &manifest); err != nil {
		return SkinDocument{}, fmt.Errorf("解析皮肤清单失败: %w", err)
	}
	if manifest.SchemaVersion != 1 || !skinIDPattern.MatchString(manifest.ID) {
		return SkinDocument{}, fmt.Errorf("无效皮肤版本或 ID")
	}
	if manifest.ID == "builtin" {
		return SkinDocument{}, fmt.Errorf("builtin 是保留 ID")
	}
	if !validSkinText(manifest.Name) {
		return SkinDocument{}, fmt.Errorf("缺少或无效的 name")
	}
	if !validSkinText(manifest.Version) {
		return SkinDocument{}, fmt.Errorf("缺少或无效的 version")
	}

	canvas := manifest.Canvas
	width, err := skinInteger(canvas.Width, 64, 220)
	if err != nil {
		return SkinDocument{}, err
	}
	height, err := skinInteger(canvas.Height, 64, 240)
	if err != nil {
		return SkinDocument{}, err
	}
	if err := checkSkinPoint(canvas.Anchor, width, height); err != nil {
		return SkinDocument{}, err
	}
	if err := checkSkinPoint(canvas.BubbleAnchor, width, height); err != nil {
		return SkinDocument{}, err
	}
	if width-130 > canvas.Anchor[0] || canvas.Anchor[0] > 130 || height-canvas.Anchor[1] > 4 {
		return SkinDocument{}, fmt.Errorf("落脚锚点需在底部 4 像素内，且形象不能超出窗口左右边界")
	}
	if err := checkSkinHitbox(canvas.Hitbox, width, height); err != nil {
		return SkinDocument{}, err
	}

	if len(manifest.Animations) < 1 || len(manifest.Animations) > 64 {
		return SkinDocument{}, fmt.Errorf("动画数量应为 1 至 64")
	}

	loader := &skinResourceLoader{
		dir:        filepath.Dir(manifestPath),
		assets:     assets,
		dimensions: make(map[string][2]int),
		resources:  make(map[string]string),
	}
	normalized := make(map[string]SkinAnimation, len(manifest.Animations))
	for name, animation := range manifest.Animations {
		normalizedAnimation, err := normalizeSkinAnimation(loader, name, animation)
		if err != nil {
			return SkinDocument{}, err
		}
		normalized[name] = normalizedAnimation
	}

	if err := checkSkinStatesAndEvents(manifest.States, manifest.Events, normalized); err != nil {
		return SkinDocument{}, err
	}

	events := manifest.Events
	if events == nil {
		events = map[string]string{}
	}
	return SkinDocument{
		SchemaVersion: 1,
		ID:            manifest.ID,
		Name:          manifest.Name,
		Version:       manifest.Version,
		Author:        truncateRunes(stringifySkinAuthor(manifest.Author), 100),
		Canvas:        canvas,
		Animations:    normalized,
		States:        manifest.States,
		Events:        events,
		Resources:     loader.resources,
	}, nil
}

func normalizeSkinAnimation(loader *skinResourceLoader, name string, animation skinAnimationRaw) (SkinAnimation, error) {
	if !skinAnimPattern.MatchString(name) {
		return SkinAnimation{}, fmt.Errorf("无效动画名")
	}
	motion := animation.Motion
	if motion == "" {
		motion = "none"
	}
	if _, ok := skinMotions[motion]; !ok {
		return SkinAnimation{}, fmt.Errorf("未知动作效果")
	}

	frames := make([]skinFrameInput, 0, 8)
	if animation.Sequence == nil {
		iw, ih, err := loader.load(animation.File)
		if err != nil {
			return SkinAnimation{}, err
		}
		frameWidth := iw
		if animation.FrameWidth != nil {
			frameWidth = *animation.FrameWidth
		}
		if frameWidth, err = skinInteger(frameWidth, 1, iw); err != nil {
			return SkinAnimation{}, err
		}
		frameHeight := ih
		if animation.FrameHeight != nil {
			frameHeight = *animation.FrameHeight
		}
		if frameHeight, err = skinInteger(frameHeight, 1, ih); err != nil {
			return SkinAnimation{}, err
		}
		count := 1
		if animation.Frames != nil {
			count = *animation.Frames
		}
		if count, err = skinInteger(count, 1, 240); err != nil {
			return SkinAnimation{}, err
		}
		fps := 8
		if animation.FPS != nil {
			fps = *animation.FPS
		}
		if fps, err = skinInteger(fps, 1, 60); err != nil {
			return SkinAnimation{}, err
		}
		columns := iw / frameWidth
		if columns < 1 {
			columns = 1
		}
		durationMS := int(math.RoundToEven(1000.0 / float64(fps)))
		for index := 0; index < count; index++ {
			frames = append(frames, skinFrameInput{
				File:        animation.File,
				Rect:        []int{(index % columns) * frameWidth, (index / columns) * frameHeight, frameWidth, frameHeight},
				DurationMS:  durationMS,
				HasDuration: true,
			})
		}
	} else {
		for _, frame := range *animation.Sequence {
			input := skinFrameInput{File: frame.File, Rect: frame.Rect}
			if frame.DurationMS != nil {
				input.DurationMS = *frame.DurationMS
				input.HasDuration = true
			}
			frames = append(frames, input)
		}
	}

	clean, err := cleanSkinFrames(loader, frames)
	if err != nil {
		return SkinAnimation{}, err
	}
	duration := 0
	for _, frame := range clean {
		duration += frame.DurationMS
	}
	if duration > skinMaxAnimationMS {
		return SkinAnimation{}, fmt.Errorf("单个动画不能超过 30 秒")
	}
	return SkinAnimation{Sequence: clean, DurationMS: duration, Motion: motion}, nil
}

func cleanSkinFrames(loader *skinResourceLoader, frames []skinFrameInput) ([]SkinFrame, error) {
	if len(frames) < 1 || len(frames) > 240 {
		return nil, fmt.Errorf("每个动画应为 1 至 240 帧")
	}
	clean := make([]SkinFrame, 0, len(frames))
	for _, frame := range frames {
		iw, ih, err := loader.load(frame.File)
		if err != nil {
			return nil, err
		}
		rect := frame.Rect
		if len(rect) == 0 {
			rect = []int{0, 0, iw, ih}
		}
		if len(rect) != 4 {
			return nil, fmt.Errorf("帧区域必须包含四个坐标")
		}
		x, err := skinInteger(rect[0], 0, iw-1)
		if err != nil {
			return nil, err
		}
		y, err := skinInteger(rect[1], 0, ih-1)
		if err != nil {
			return nil, err
		}
		w, err := skinInteger(rect[2], 1, iw-x)
		if err != nil {
			return nil, err
		}
		h, err := skinInteger(rect[3], 1, ih-y)
		if err != nil {
			return nil, err
		}
		duration := 125
		if frame.HasDuration {
			duration = frame.DurationMS
		}
		if duration, err = skinInteger(duration, 16, 10000); err != nil {
			return nil, err
		}
		clean = append(clean, SkinFrame{File: frame.File, Rect: []int{x, y, w, h}, DurationMS: duration})
	}
	return clean, nil
}

// skinFrameInput 校验前的动画帧，HasDuration 区分缺省值与显式 0
type skinFrameInput struct {
	File        string
	Rect        []int
	DurationMS  int
	HasDuration bool
}

func checkSkinStatesAndEvents(states, events map[string]string, animations map[string]SkinAnimation) error {
	if len(states) != len(skinStates) {
		return fmt.Errorf("必须显式映射 idle/thinking/talking/dragging，事件名需使用标准事件")
	}
	for _, name := range skinStates {
		if _, ok := states[name]; !ok {
			return fmt.Errorf("必须显式映射 idle/thinking/talking/dragging，事件名需使用标准事件")
		}
	}
	for event := range events {
		if _, ok := skinEvents[event]; !ok {
			return fmt.Errorf("必须显式映射 idle/thinking/talking/dragging，事件名需使用标准事件")
		}
	}
	for _, name := range states {
		if _, ok := animations[name]; !ok {
			return fmt.Errorf("状态或事件引用了不存在的动画")
		}
	}
	for _, name := range events {
		if _, ok := animations[name]; !ok {
			return fmt.Errorf("状态或事件引用了不存在的动画")
		}
	}
	return nil
}

func checkSkinPoint(point []int, width, height int) error {
	if len(point) != 2 {
		return fmt.Errorf("锚点必须包含两个坐标")
	}
	if _, err := skinInteger(point[0], 0, width); err != nil {
		return err
	}
	_, err := skinInteger(point[1], 0, height)
	return err
}

func checkSkinHitbox(box []int, width, height int) error {
	if len(box) != 4 {
		return fmt.Errorf("点击区域必须包含 x,y,width,height")
	}
	x, err := skinInteger(box[0], 0, width)
	if err != nil {
		return err
	}
	y, err := skinInteger(box[1], 0, height)
	if err != nil {
		return err
	}
	if _, err := skinInteger(box[2], 1, width-x); err != nil {
		return err
	}
	_, err = skinInteger(box[3], 1, height-y)
	return err
}

func skinInteger(value, low, high int) (int, error) {
	if value < low || value > high {
		return 0, fmt.Errorf("整数应在 %d 至 %d 之间", low, high)
	}
	return value, nil
}

func validSkinText(value string) bool {
	length := len([]rune(value))
	return length >= 1 && length <= 100
}

func stringifySkinAuthor(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

// skinResourceLoader 皮肤资源校验与内联，对齐 load_skin.resource
type skinResourceLoader struct {
	dir        string
	assets     bool
	total      int64
	dimensions map[string][2]int
	resources  map[string]string
}

func (loader *skinResourceLoader) load(filename string) (int, int, error) {
	if dimensions, ok := loader.dimensions[filename]; ok {
		return dimensions[0], dimensions[1], nil
	}
	file, err := loader.resolve(filename)
	if err != nil {
		return 0, 0, err
	}
	stat, err := os.Stat(file)
	if err != nil {
		return 0, 0, fmt.Errorf("皮肤资源读取失败: %w", err)
	}
	loader.total += stat.Size()
	if stat.Size() > skinMaxBytes || loader.total > skinMaxBytes {
		return 0, 0, fmt.Errorf("皮肤图片总大小不能超过 32 MiB")
	}
	info, err := readImageInfo(file)
	if err != nil {
		return 0, 0, err
	}
	if info.Width*info.Height > skinMaxPixels {
		return 0, 0, fmt.Errorf("图片格式或尺寸不支持")
	}
	if info.Animated {
		return 0, 0, fmt.Errorf("请使用静态图片帧或精灵图，不支持动图文件")
	}
	loader.dimensions[filename] = [2]int{info.Width, info.Height}
	if loader.assets {
		raw, err := os.ReadFile(file)
		if err != nil {
			return 0, 0, fmt.Errorf("皮肤资源读取失败: %w", err)
		}
		loader.resources[filename] = "data:" + info.MIME + ";base64," + base64.StdEncoding.EncodeToString(raw)
	}
	return info.Width, info.Height, nil
}

func (loader *skinResourceLoader) resolve(filename string) (string, error) {
	if strings.ContainsAny(filename, "\\:") || path.IsAbs(filename) {
		return "", fmt.Errorf("皮肤资源必须使用包内相对路径")
	}
	for _, part := range strings.Split(filename, "/") {
		if part == ".." {
			return "", fmt.Errorf("皮肤资源必须使用包内相对路径")
		}
	}
	extension := strings.ToLower(path.Ext(filename))
	if extension != ".png" && extension != ".webp" {
		return "", fmt.Errorf("仅允许包内 PNG/WebP 图片")
	}
	resolved := filepath.Join(loader.dir, filepath.FromSlash(filename))
	relative, err := filepath.Rel(loader.dir, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("皮肤资源必须使用包内相对路径")
	}
	return resolved, nil
}

// SkinInteger 对齐 desktop_pet/skins._integer：严格整数范围校验
func SkinInteger(value, low, high int) (int, error) {
	return skinInteger(value, low, high)
}
