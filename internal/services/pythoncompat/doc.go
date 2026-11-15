// Package pythoncompat 的边界与选型说明。
//
// # 什么时候该放到 Python 侧车
//
// 判据是「复杂度来自复刻 Python 字节级行为」，而不是「Go 写不好」。典型特征：
//
//   - 依赖 Python 生态的随机数、数值、分词实现（numpy PCG64、pypinyin、jieba）
//   - 输出要与原版逐字节一致，否则检索/学习数据会对不上
//   - 上游一升级，Go 侧就得跟着改一遍
//
// 反例（不要放到侧车）：
//
//   - `internal/A_memorix/**`：这是 Go 重写的主要成果，侧车化等于白移植。
//     它的 jieba 分词、BM25、向量存储都留在 Go 侧。
//   - 请求热路径上的纯计算：一次 HTTP 往返的代价远高于本地算法。
//
// # 当前托管的能力
//
//	/typo     中文错别字注入，调用 src/chat/utils/typo_generator.py
//	/kmeans   表达向量 cosine k-means，调用 src/chat/replyer/expression_vector_index.py
//
// 两者都有 Go 侧的本地实现作为回落：侧车未配置、超时或返回异常时，
// 调用方会警告并继续用本地实现，功能不会因为侧车挂掉而消失。
//
// # 配置
//
//	MAIBOT_COMPAT_URL      侧车根地址，如 http://python-compat:8767；为空表示不启用
//	MAIBOT_COMPAT_TOKEN    可选 Bearer token
//	MAIBOT_COMPAT_TIMEOUT  秒，默认 30（内部最小 120，聚类批量上传较慢）
//
// 进程级客户端由 DefaultClient 提供；测试可用 SetDefaultClient 注入。
package pythoncompat
