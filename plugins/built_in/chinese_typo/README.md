# 麦麦小工具

Go 实现沿用 `builtin.chinese-typo` 和实例目录下的 `config/builtin_plugins/chinese_typo/config.toml`。Dashboard 通过原有插件接口管理启停、表单、原始 TOML 和重置。`config_schema.json` 由同目录的 Python 配置模型通过 MaiBot SDK 2.23.0 导出。

错字词库沿用 `typo_words.tsv`。规则模式共享 Go jieba 词典，按原始词频计分。`pinyin_tone3.tsv.gz` 来自 pypinyin 0.55.0，对 `PINYIN_DICT` 的单字和 `PHRASES_DICT` 的词语分别提取 `Style.TONE3` 首选读音，原始许可证见 `pinyin_tone3.LICENSE`。Go 和 Python 的随机源独立，同一个数值种子不保证输出相同。

关键词反应保留关键词优先、规则顺序、命名捕获组和跳过语法错误的处理。Go 使用 regexp2 支持常见 Python 正则，包括前后查找和反向引用。两个引擎的部分边界行为会有差异，Go 对单个表达式设匹配超时。
