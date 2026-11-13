package scripts

// LPMM OpenIE JSON 导入脚本，对齐 src/A_memorix/scripts/import_lpmm_json.py

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"maibot-go/internal/A_memorix/core/utils"
)

// lpmmLoggerName 对齐 get_logger("A_Memorix.LPMMImport")
const lpmmLoggerName = "A_Memorix.LPMMImport"

// newImportLPMMParser 构造参数解析器，对齐 _build_arg_parser
func newImportLPMMParser(ctx context.Context) *ArgParser {
	parser := newScriptParser(ctx, "import_lpmm_json.py", "将 LPMM OpenIE JSON 导入 A_Memorix")
	parser.AddPositional("path", "imports/source/lpmm 内的 JSON 文件或目录")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "A_Memorix 数据目录")
	parser.AddBool([]string{"--force"}, "强制重新导入")
	parser.AddInt([]string{"--concurrency", "-c"}, 5, "并发数")
	return parser
}

// RunImportLPMMJSON 执行 LPMM JSON 导入流程，返回进程退出码
func RunImportLPMMJSON(ctx context.Context, argv []string) int {
	parser := newImportLPMMParser(ctx)
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}
	logger := newScriptLogger(ctx, lpmmLoggerName)

	dataDir := ResolveRepoPath(args.String("data-dir"), DefaultDataDir())
	targetPath, err := resolveLPMMSourcePath(args.String("path"), dataDir)
	if err != nil {
		scriptErrorf(ctx, "❌ %v\n", err)
		return 1
	}
	if !pathExists(targetPath) {
		logger.Errorf("路径不存在: %s", targetPath)
		return 0
	}

	files, err := collectLPMMFiles(targetPath)
	if err != nil {
		logger.Errorf("读取导入目录失败: %v", err)
		return 0
	}
	if len(files) == 0 {
		logger.Error("未找到可处理的 JSON 文件")
		return 0
	}

	importer, err := NewAutoImporter(AutoImporterOptions{
		Force:       args.Bool("force"),
		Concurrency: args.Int("concurrency"),
		DataDir:     dataDir,
		Logger:      logger,
	})
	if err != nil {
		scriptErrorf(ctx, "❌ 初始化导入器失败: %v\n", err)
		return 1
	}
	if !importer.Initialize(ctx) {
		logger.Error("初始化存储失败")
		return 0
	}

	converter := newLPMMConverter(logger)
	for _, file := range files {
		filename := filepath.Base(file)
		logger.Infof("正在转换并导入: %s", filename)
		if err := importLPMMFile(ctx, importer, converter, file, filename, logger); err != nil {
			logger.Errorf("处理文件 %s 失败: %v", filename, err)
		}
	}

	if err := importer.Close(); err != nil {
		logger.Warningf("关闭元数据存储失败: %v", err)
	}
	logger.Info("全部处理完成")
	return 0
}

// importLPMMFile 转换并导入单个 LPMM JSON 文件
func importLPMMFile(ctx context.Context, importer *AutoImporter, converter LPMMConverter, file string, filename string, logger *ScriptLogger) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	lpmmData := map[string]any{}
	if err := UnmarshalJSON(raw, &lpmmData); err != nil {
		return err
	}
	memorixData, err := converter.Convert(lpmmData, filename)
	if err != nil {
		return err
	}
	totalItems := len(anySlice(memorixData["paragraphs"]))
	if totalItems <= 0 {
		logger.Warningf("转换结果为空: %s", filename)
		return nil
	}
	progress := newLPMMProgress(logger, filename, totalItems)
	return importer.ImportJSONData(ctx, memorixData, "lpmm_"+filename, progress.Advance)
}

// resolveLPMMSourcePath 对齐 _resolve_lpmm_source_path
func resolveLPMMSourcePath(rawPath string, dataDir string) (string, error) {
	sourceRoot := resolveAbsoluteForCompare(filepath.Join(dataDir, "imports", "source", "lpmm"))
	candidate := ExpandUser(strings.TrimSpace(rawPath))
	if candidate == "" {
		candidate = "."
	}

	var resolved string
	if filepath.IsAbs(candidate) {
		resolved = resolveAbsoluteForCompare(candidate)
	} else {
		repoCandidate := ResolveRepoPath(candidate, "")
		if IsSubPath(sourceRoot, repoCandidate) {
			resolved = repoCandidate
		} else {
			resolved = resolveAbsoluteForCompare(filepath.Join(sourceRoot, candidate))
		}
	}
	if !IsSubPath(sourceRoot, resolved) {
		return "", fmt.Errorf("LPMM JSON 必须位于导入目录: %s", sourceRoot)
	}
	return resolved, nil
}

// collectLPMMFiles 收集目录内的 JSON 文件，对齐 Path.glob 的两级回退
func collectLPMMFiles(targetPath string) ([]string, error) {
	info, err := os.Stat(targetPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{targetPath}, nil
	}
	entries, err := os.ReadDir(targetPath)
	if err != nil {
		return nil, err
	}
	var openieFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if matched, _ := filepath.Match("*-openie.json", entry.Name()); matched {
			openieFiles = append(openieFiles, filepath.Join(targetPath, entry.Name()))
		}
	}
	if len(openieFiles) > 0 {
		return openieFiles, nil
	}
	var jsonFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if matched, _ := filepath.Match("*.json", entry.Name()); matched {
			jsonFiles = append(jsonFiles, filepath.Join(targetPath, entry.Name()))
		}
	}
	return jsonFiles, nil
}

// LPMMConverter LPMM OpenIE 数据转换器，对齐 LPMMConverter
type LPMMConverter struct {
	logger utils.Logger
}

// newLPMMConverter 构造转换器
func newLPMMConverter(logger utils.Logger) LPMMConverter {
	return LPMMConverter{logger: utils.OrLogger(logger)}
}

// Convert 对齐 convert_lpmm_to_memorix
func (c LPMMConverter) Convert(lpmmData map[string]any, filename string) (map[string]any, error) {
	memorixData := map[string]any{"paragraphs": []any{}, "entities": []any{}}
	docs := anySlice(lpmmData["docs"])
	if len(docs) == 0 {
		c.logger.Warning(fmt.Sprintf("文件中未找到 docs 字段: %s", filename))
		return memorixData, nil
	}

	allEntities := map[string]struct{}{}
	paragraphs := make([]any, 0, len(docs))
	for _, item := range docs {
		doc, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("doc 不是对象: %T", item)
		}
		content := strings.TrimSpace(pyString(doc["passage"]))
		if content == "" {
			continue
		}

		relations := make([]any, 0)
		for _, tripleItem := range anySlice(doc["extracted_triples"]) {
			triple, ok := tripleItem.([]any)
			if !ok || len(triple) != 3 {
				continue
			}
			relations = append(relations, map[string]any{
				"subject":   strings.TrimSpace(pyString(triple[0])),
				"predicate": strings.TrimSpace(pyString(triple[1])),
				"object":    strings.TrimSpace(pyString(triple[2])),
			})
		}

		entities := make([]any, 0)
		entityNames := make([]string, 0)
		for _, entityItem := range anySlice(doc["extracted_entities"]) {
			name := strings.TrimSpace(pyString(entityItem))
			if name == "" {
				continue
			}
			entities = append(entities, name)
			entityNames = append(entityNames, name)
			allEntities[name] = struct{}{}
		}
		for _, relation := range relations {
			relationMap := relation.(map[string]any)
			if subject, _ := relationMap["subject"].(string); subject != "" {
				allEntities[subject] = struct{}{}
			}
			if object, _ := relationMap["object"].(string); object != "" {
				allEntities[object] = struct{}{}
			}
		}

		paragraphs = append(paragraphs, map[string]any{
			"hash":      utils.ComputeParagraphHash(content),
			"content":   content,
			"source":    filename,
			"entities":  entities,
			"relations": relations,
		})
	}

	sortedEntities := make([]string, 0, len(allEntities))
	for name := range allEntities {
		sortedEntities = append(sortedEntities, name)
	}
	sort.Strings(sortedEntities)
	entityItems := make([]any, 0, len(sortedEntities))
	for _, name := range sortedEntities {
		entityItems = append(entityItems, name)
	}

	memorixData["paragraphs"] = paragraphs
	memorixData["entities"] = entityItems
	return memorixData, nil
}

// lpmmProgress 导入进度输出，替代 rich.Progress 进度条
// 差异说明：Python 是终端进度条，这里按 10% 粒度写脚本日志
type lpmmProgress struct {
	logger   *ScriptLogger
	label    string
	total    int
	current  int
	lastStep int
}

// newLPMMProgress 构造进度输出
func newLPMMProgress(logger *ScriptLogger, label string, total int) *lpmmProgress {
	return &lpmmProgress{logger: logger, label: label, total: total, lastStep: -1}
}

// Advance 推进进度
func (p *lpmmProgress) Advance(step int) {
	if p == nil || p.total <= 0 {
		return
	}
	p.current += step
	if p.current > p.total {
		p.current = p.total
	}
	percent := p.current * 100 / p.total
	if p.lastStep < 0 || percent == 100 || percent/10 != p.lastStep/10 {
		p.logger.Infof("Importing %s: %d/%d (%d%%)", p.label, p.current, p.total, percent)
		p.lastStep = percent
	}
}
