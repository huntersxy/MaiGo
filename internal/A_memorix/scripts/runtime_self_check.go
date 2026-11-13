package scripts

// 真实 embedding 与运行时配置自检，对齐 src/A_memorix/scripts/runtime_self_check.py
// 存储初始化复用 core/runtime 的 initialize_storage_async，不做内核重写

import (
	"context"
	"os"

	"github.com/bytedance/sonic"

	"maibot-go/internal/A_memorix/core/embedding"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// runtimeSelfCheckSampleText 对齐 _DEFAULT_SAMPLE_TEXT
const runtimeSelfCheckSampleText = "A_Memorix runtime self check"

// runtimeSelfCheckLoggerName 对齐 get_logger("A_Memorix.RuntimeSelfCheck")
const runtimeSelfCheckLoggerName = "A_Memorix.RuntimeSelfCheck"

// reportJSON 报告输出编码器：key 排序保证可复现
// 差异说明：Python json.dumps 保留字典插入顺序，这里按键名排序
var reportJSON = sonic.Config{EscapeHTML: false, SortMapKeys: true}.Froze()

// runtimeSelfCheckCommand 单条自检结果
type runtimeSelfCheckCommand struct {
	platform         string
	configPath       string
	dataDir          string
	useConfigDataDir bool
	sampleText       string
	asJSON           bool
}

// newRuntimeSelfCheckParser 构造参数解析器，对齐 _build_arg_parser
func newRuntimeSelfCheckParser(ctx context.Context) *ArgParser {
	parser := newScriptParser(ctx, "runtime_self_check.py", "A_Memorix runtime self-check")
	parser.AddString([]string{"--config"}, DefaultConfigPath(), "config.toml path (default: config/a_memorix.toml)")
	parser.AddString([]string{"--data-dir"}, "", "optional data dir override; default resolved from config.storage.data_dir")
	parser.AddBool([]string{"--use-config-data-dir"}, "use config.storage.data_dir directly instead of an isolated temp dir")
	parser.AddString([]string{"--sample-text"}, runtimeSelfCheckSampleText, "sample text used for real embedding probe")
	parser.AddBool([]string{"--json"}, "print JSON report")
	return parser
}

// RunRuntimeSelfCheck 执行 runtime_self_check.py 等价流程，返回进程退出码
func RunRuntimeSelfCheck(ctx context.Context, argv []string) int {
	parser := newRuntimeSelfCheckParser(ctx)
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}
	logger := newScriptLogger(ctx, runtimeSelfCheckLoggerName)

	configPath := ResolveRepoPath(args.String("config"), DefaultConfigPath())
	if _, err := os.Stat(configPath); err != nil {
		scriptPrintf(ctx, "❌ 配置文件不存在: %s\n", configPath)
		return 2
	}

	config, err := LoadTOML(configPath)
	if err != nil {
		scriptErrorf(ctx, "❌ 加载配置失败: %v\n", err)
		return 1
	}

	isolated := false
	var storageDir string
	switch {
	case args.String("data-dir") != "":
		storageDir = ResolveRepoPath(args.String("data-dir"), DefaultDataDir())
	case args.Bool("use-config-data-dir"):
		rawDataDir := utils.PyStr(NestedGet(config, "storage.data_dir", "./data"))
		if rawDataDir == "None" || rawDataDir == "" {
			rawDataDir = "./data"
		}
		storageDir = ResolveRepoPath(rawDataDir, DefaultDataDir())
	default:
		tempDir, err := os.MkdirTemp("", SelfCheckTempPrefix)
		if err != nil {
			scriptErrorf(ctx, "❌ 创建临时数据目录失败: %v\n", err)
			return 1
		}
		storageDir = tempDir
		isolated = true
	}
	if isolated {
		defer func() { _ = os.RemoveAll(storageDir) }()
	}

	SetNested(config, "storage.data_dir", storageDir)

	plugin := newScriptPlugin(config)
	sampleText := args.String("sample-text")
	if sampleText == "" {
		sampleText = runtimeSelfCheckSampleText
	}

	if initErr := memruntime.InitializeStorageAsync(ctx, plugin, nil); initErr != nil {
		scriptErrorf(ctx, "❌ 初始化存储失败: %v\n", initErr)
		if plugin.metadataStore != nil {
			_ = plugin.metadataStore.Close()
		}
		return 1
	}

	report := utils.RunEmbeddingRuntimeSelfCheck(ctx, utils.SelfCheckParams{
		Config:           plugin.GetConfig,
		VectorStore:      vectorStoreSnapshotSource(plugin.vectorStore),
		EmbeddingManager: scriptSelfCheckEmbedding{adapter: plugin.embeddingManager},
		// Python 脚本未传段落池/图谱池，快照按不可用输出
		SampleText: sampleText,
		Logger:     logger,
	})

	if closeErr := plugin.metadataStore.Close(); closeErr != nil {
		scriptErrorf(ctx, "warning: metadata store close failed: %v\n", closeErr)
	}

	if args.Bool("json") {
		payload := report.ToDict()
		payload["data_dir"] = storageDir
		payload["isolated_data_dir"] = isolated
		text, marshalErr := reportJSON.MarshalIndent(payload, "", "  ")
		if marshalErr != nil {
			scriptErrorf(ctx, "❌ 序列化自检报告失败: %v\n", marshalErr)
			return 1
		}
		scriptPrintln(ctx, string(text))
	} else {
		scriptPrintln(ctx, "A_Memorix Runtime Self-Check")
		scriptPrintf(ctx, "ok: %s\n", pyBoolText(report.OK))
		scriptPrintf(ctx, "code: %s\n", report.Code)
		scriptPrintf(ctx, "message: %s\n", report.Message)
		scriptPrintf(ctx, "configured_dimension: %d\n", report.ConfiguredDimension)
		scriptPrintf(ctx, "vector_store_dimension: %d\n", report.VectorStoreDimension)
		scriptPrintf(ctx, "detected_dimension: %d\n", report.DetectedDimension)
		scriptPrintf(ctx, "encoded_dimension: %d\n", report.EncodedDimension)
		scriptPrintf(ctx, "elapsed_ms: %.2f\n", report.ElapsedMS)
	}
	if report.OK {
		return 0
	}
	return 1
}

// scriptSelfCheckEmbedding 把嵌入适配器适配成自检接口
type scriptSelfCheckEmbedding struct {
	adapter *embedding.EmbeddingAPIAdapter
}

// DetectDimension 对齐 _detect_dimension
func (m scriptSelfCheckEmbedding) DetectDimension(ctx context.Context) (int, error) {
	return m.adapter.DetectDimension(ctx)
}

// Encode 对齐 encode(str) 的单条分支
func (m scriptSelfCheckEmbedding) Encode(ctx context.Context, text string) ([]float32, error) {
	return m.adapter.EncodeOne(ctx, text, nil)
}

// GetRequestedDimension 对齐 _get_requested_dimension 读到的配置维度
func (m scriptSelfCheckEmbedding) GetRequestedDimension() int {
	return m.adapter.GetRequestedDimension()
}

// vectorStoreSnapshotSource 归一 typed-nil 向量存储
func vectorStoreSnapshotSource(store *storage.VectorStore) utils.VectorStoreSnapshotSource {
	if store == nil {
		return nil
	}
	return store
}

// pyBoolText 对齐 print(bool) 的大写输出
func pyBoolText(value bool) string {
	if value {
		return "True"
	}
	return "False"
}
