package scripts

// LPMM 离线转换入口，对齐 src/A_memorix/scripts/convert_lpmm.py
// parquet 读取、GraphML 转换与双池清单写入拆分在同目录的 convert_lpmm_*.go

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// lpmmConverterLoggerName 对齐 get_logger("A_Memorix.LPMMConverter")
const lpmmConverterLoggerName = "A_Memorix.LPMMConverter"

// RunConvertLPMM 执行 convert_lpmm.py 等价流程，返回进程退出码
func RunConvertLPMM(ctx context.Context, argv []string) int {
	return RunConvertLPMMWithIO(ctx, argv, ScriptStdout(ctx), ScriptStderr(ctx))
}

// RunConvertLPMMWithIO 使用指定输出执行转换，供 WebUI 进程内调用
func RunConvertLPMMWithIO(ctx context.Context, argv []string, stdout io.Writer, stderr io.Writer) int {
	parser := NewArgParser("convert_lpmm.py", "将 LPMM 数据转换为 A_Memorix 格式")
	parser.Stdout = stdout
	parser.Stderr = stderr
	parser.AddString([]string{"--input", "-i"}, "", "包含 LPMM 数据的输入目录 (parquet, graphml)")
	parser.AddString([]string{"--output", "-o"}, "", "A_memorix 数据的输出目录")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "A_Memorix 数据目录")
	parser.AddInt([]string{"--dim"}, 384, "Embedding 维度 (必须与 LPMM 模型匹配)")
	parser.AddInt([]string{"--batch-size"}, 1024, "Parquet 分批读取大小 (默认 1024)")
	parser.AddBool([]string{"--skip-relation-vector-rebuild"}, "兼容旧参数；当前会直接使用 relation.parquet 中已有的向量")
	parser.AddBool([]string{"--allow-unsafe-pickle"}, "允许读取 LPMM graph_structure.pkl。该格式会反序列化 pickle，只应在信任输入来源时开启。")
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}

	rawInput := strings.TrimSpace(args.String("input"))
	rawOutput := strings.TrimSpace(args.String("output"))
	if rawInput == "" || rawOutput == "" {
		missing := make([]string, 0, 2)
		if rawInput == "" {
			missing = append(missing, "--input/-i")
		}
		if rawOutput == "" {
			missing = append(missing, "--output/-o")
		}
		fmt.Fprintf(stderr, "convert_lpmm.py: error: the following arguments are required: %s\n", strings.Join(missing, ", "))
		return 2
	}

	logger := NewScriptLoggerTo(lpmmConverterLoggerName, stderr)
	dataDir := ResolveRepoPath(args.String("data-dir"), DefaultDataDir())
	inputRoot := resolveAbsoluteForCompare(filepath.Join(dataDir, "imports", "source", "lpmm"))
	outputRoot := resolveAbsoluteForCompare(filepath.Join(dataDir, "imports", "converted"))
	inputPath, err := resolveLPMMImportPath(rawInput, inputRoot, "LPMM 输入")
	if err != nil {
		logger.Error(err.Error())
		return 1
	}
	outputPath, err := resolveLPMMImportPath(rawOutput, outputRoot, "LPMM 输出")
	if err != nil {
		logger.Error(err.Error())
		return 1
	}
	if !pathExists(inputPath) {
		logger.Errorf("输入目录不存在: %s", inputPath)
		return 1
	}

	converter := newLPMMParquetConverter(inputPath, outputPath, args.Int("dim"), args.Int("batch-size"), args.Bool("allow-unsafe-pickle"), logger)
	if err := converter.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "❌ 转换失败: %v\n", err)
		return 1
	}
	return 0
}

// resolveLPMMImportPath 对齐 _resolve_import_path
func resolveLPMMImportPath(raw, root, label string) (string, error) {
	candidate := ExpandUser(strings.TrimSpace(raw))
	if candidate == "" {
		candidate = "."
	}
	var resolved string
	if filepath.IsAbs(candidate) {
		resolved = resolveAbsoluteForCompare(candidate)
	} else {
		repoCandidate := resolveAbsoluteForCompare(ResolveRepoPath(candidate, ""))
		if IsSubPath(root, repoCandidate) {
			resolved = repoCandidate
		} else {
			resolved = resolveAbsoluteForCompare(filepath.Join(root, candidate))
		}
	}
	if !IsSubPath(root, resolved) {
		return "", fmt.Errorf("%s必须位于导入目录: %s", label, root)
	}
	return resolved, nil
}

// pathIsDir 判断路径是否为目录
func pathIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// validateLPMMInput 对齐 _validate_input
func validateLPMMInput(inputPath string) error {
	if !pathIsDir(inputPath) {
		return fmt.Errorf("输入路径必须为目录: %s", inputPath)
	}
	paragraphPath := filepath.Join(inputPath, "paragraph.parquet")
	entityPath := filepath.Join(inputPath, "entity.parquet")
	if !pathIsFile(paragraphPath) && !pathIsFile(entityPath) {
		return fmt.Errorf("输入目录至少需要 paragraph.parquet 或 entity.parquet")
	}
	return nil
}
