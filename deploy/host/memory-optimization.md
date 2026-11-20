# MaiGo 运行时内存优化报告

分析基线为 `b4d66a7`，测量时间为 2026 年 11 月 20 日（北京时间）。

**建议先把单 Bot 的常态工作集压进 300 MB，再验证 200 MB。** 保留 WebUI、QQ 原生适配器、图片与表情包、A_Memorix 和现有 Python 插件。收益最大的方向是精简内置「麦麦小工具」使用的拼音和分词数据，并让 Go 与 Python 共用分词词典。仅调整 Go GC 参数，无法回收 Python 的词典。

300 MB 约为 286 MiB，200 MB 约为 191 MiB。下文的测量统一使用 MiB。

## 测量口径

- 生产部署的一个 `maigo.service` 包含 Go 宿主、每个 Bot 的 Go Worker，以及有启用中的 Python 插件时启动的 Runner。当前 Go 启动主线调用一次 `StartRunner`，使用 `third_party` 组同时加载所需内置与社区插件。仓库中有双分组实现，当前主线的实测只有一个 Python 进程。 [系统服务](maigo.service) · [启动装配](../../internal/bootstrap/main.go) · [Runner 插件目录](../../internal/plugin_runtime/host/plugin_dirs.go)
- 服务内存预算以 cgroup v2 的 `memory.current`、`memory.peak` 为准，同时看 `memory.stat` 的 `anon`、`file`、`inactive_file` 和 `kernel`。RSS 相加会把宿主和 Worker 的共享代码页重复计算。PSS 将共享页按使用者分摊，适合定位进程贡献。Go pprof 的 `inuse_space` 是采样后的活对象堆，可用于归因，范围比进程或服务内存小。
- 线上后台只读确认了一个运行中的 Bot、原生 SnowLuma 与 QQ 官方适配器，以及启用中的「麦麦小工具」和 `partner-game 2.0.2`。A_Memorix 自检显示 1,024 维、混合检索就绪。该后台接口提供功能和版本状态，线上 Linux PSS、cgroup 内存及真实向量条数尚未取得。
- 本地使用 Linux/amd64、Go 1.26.5、CPython 3.12、MaiBot wheel 1.4.5、插件 SDK 2.23.0，固定 `GOMAXPROCS=2`。开发构建带调试信息，生产发布采用 Alpine 静态构建。表中 RSS 和 PSS 样本来自本地，不能直接替代生产 cgroup 数据。

## 实测归因

### 单 Bot 启动基线

以下为空闲样本的近 8 秒中位数。启动基线使用初始大小约 0.85 MiB 的测试数据库和休眠的 A_Memorix，两组实例可同时测量，PSS 中的文件页会受共享比例影响。后面的负载使用独立的一套进程树。

| 启用项 | Go 宿主及 Worker RSS 合计 | Python RSS / PSS | 全树 RSS 合计 | 全树 PSS |
| --- | ---: | ---: | ---: | ---: |
| 无 Python 插件，自动跳过 Runner | 170.5 | 无 | 170.5 | 54.0 |
| 一个轻量插件 | 约 170 | 35.2 / 29.6 | 205.5 | 83.2 |
| 麦麦小工具，刚加载 | 约 172 | 89.6 / 83.6 | 261.0 | 137.7 |
| 同上，`GOGC=60 GOMEMLIMIT=120MiB` | 约 169 | 89.6 / 84.0 | 258.1 | 135.5 |

在报告基线的 systemd 环境模板中，`GOGC=80 GOMEMLIMIT=384MiB`。调到 60 / 120 后，上面这个带插件的空闲试验只减少约 2 MiB PSS。该参数对 Go 运行时的内存是软目标，每个 Go 进程分别应用。Python、部分非 Go 堆分配和内核文件页都要另外计算。 [环境模板](maigo.env.example) · [Go GC 指南](https://go.dev/doc/gc-guide)

### Python Runner

在一个独立解释器内逐步执行并取 PSS。每次采样前做 Python GC。

| 阶段 | 进程 PSS |
| --- | ---: |
| CPython 解释器 | 5.7 |
| 导入 Runner 入口 | 27.1 |
| 导入内置麦麦小工具 | 83.3 |
| 创建规则模式错字生成器 | 86.1 |
| 首次真正调用 jieba 分词 | 144.8 |
| 再读取规则模式整词频率表 | 185.6 |

另一轮单独归因中，`import jieba` 增加约 2.8 MiB PSS，随后 `import pypinyin` 增加约 51.5 MiB。业务热路径还会建立 Python jieba 前缀词典和另外一张整词频率表。完整热态的开销主要来自内置插件保留的数据。Python Runner 的最低实测约 30 MiB PSS，线上第三方插件仍需要它。 `partner-game` 当前公开版本的外部 Python 依赖是 `aiohttp`。该插件的实际群聊玩法、头像和数据规模会影响峰值。 [错字规则](../../plugins/built_in/chinese_typo/typo_generator.py) · [错字词库模式](../../plugins/built_in/chinese_typo/dictionary_typo.py) · [第三方插件](https://github.com/onelxzy/partner-game/tree/e36bdeb)

### Go Worker 与聊天负载

在独立测试实例中开启 A_Memorix，使用本机模拟的 Planner、回复模型和 1,024 维 embedding。用 WebUI WebSocket 发 24 条合成消息，每 4 条附一个 80×80 图片或表情附件，共 6 个附件，测试同时加载「麦麦小工具」和 `partner-game`。这批请求触发了 Planner 和记忆检索，模拟模型未生成实际可见回复，因此这组数字反映入站、规划、检索及插件已加载的开销。Python 热态以上一节的独立测量补充。

| 同一负载下的配置 | 全树 RSS 峰值 | 全树 PSS 峰值 | 全树匿名 PSS 峰值 |
| --- | ---: | ---: | ---: |
| 两个 Python 插件，部署模板中的 Go GC 值 | 350.8 | 281.4 | 197.1 |
| 两个 Python 插件，`GOGC=60 GOMEMLIMIT=120MiB` | 342.7 | 274.3 | 189.6 |
| 无 Python 插件，A_Memorix 仍开启，同样的紧凑 GC 参数 | 247.0 | 181.9 | 103.7 |
| 两个 Python 插件，主动预热 Python 错字规则与词频表，部署模板中的 Go GC 值 | 459.6 | 391.0 | 306.8 |
| Python 只加载 `partner-game`，部署模板中的 Go GC 值 | 300.6 | 231.9 | 147.7 |

同一组负载把 Go GC 调紧后，全树峰值 PSS 少了约 7 MiB。主动预热在实际 Runner 内执行，预热完成后 Python 进程稳态约 199 MiB PSS。只加载游戏插件的对照约 43 MiB，两个进程树的峰值相差 159 MiB PSS。该差值是移除 Python 错字数据的对照，Go 替换实现的常驻数据还要扣除。另一次补测使用真实的上下文消息编号，接收 12 条入站消息和 2 次可见 Bot 回复事件，预热后的全树峰值为 380.6 MiB PSS。真实群聊游戏并发、长时间运行和大媒体峰值待压测。

负载后 Worker 的 Go heap profile 显示，约 41.2 MiB 存活 Go 堆中，`jiebacut.NewDictionary` 的归因约为 31.0 MiB，其中约 10.5 MiB 在 `bufio.Scanner.Text` 的字符串。词典已经在 Go 进程级共享，高频词、检索和搜索可以复用。词频表使用 `map[string]float64`，同时为每个词登记零频前缀。压缩这份结构比压低闲时 GC 比例更有空间。 [Go 词典](../../internal/chat/replyer/jiebacut/jieba_cut.go) · [A_Memorix 分词](../../internal/A_memorix/core/retrieval/jieba_segmenter.go)

A_Memorix 在构造稀疏检索时会把词典加载到 Worker，即使数据库中的聊天记录还很少。还需为以下随工作量增长的部分留出裕量。

| 项目 | 现有开销及可优化点 |
| --- | --- |
| A_Memorix 活跃向量 | 运行时索引保存 `[]float32`，1,024 维单向量约 4 KiB，累计 1 万条约 39 MiB、2 万条约 78 MiB、5 万条约 195 MiB。多个向量池与活跃模型空间按保留总数合计。另有 ID、写缓冲、训练样本、索引切换等开销。磁盘元数据标记 `sq8` 时，Go 的内存搜索仍按 Float32 精确内积实施。 [向量存储](../../internal/A_memorix/core/storage/vector_store.go) · [索引加载](../../internal/A_memorix/core/storage/vector_store_persist.go) |
| SQLite | 主库连接上限为 1。A_Memorix 每个 `sql.DB` 最多开 16 条连接、最多保留 4 条空闲连接，`cache_size=-64000` 逐连接应用，约 62.5 MiB 的建议页缓存上限会随读写实际填充。日常压力看并发读、排序、导入及重建，不能按连接数直接估计实际占用。 [主库](../../internal/common/database/manager.go) · [记忆数据库](../../internal/A_memorix/core/storage/sqlite_connection.go) |
| 媒体与 WebUI | 表情缩略图缓存最多 512 条、约 32 MiB 的 Base64。解码、缩放、WebSocket 与供应商 JSON 会产生临时副本。以 4,096×4,096 像素的 RGBA 为例，只展开一次就是 64 MiB。实际内存应按像素与同时处理字节量预算。 [表情缓存](../../internal/maisaka/visual/emoji_image_cache.go) · [宿主代理](../../internal/host/webui.go) |
| 外部进程 | HTML 渲染配置走外部 Playwright 服务。部署环境中的 NapCat、Chromium、另起进程的 MCP 或本地模型需要按实际归属计费。与 MaiGo 放在同一 cgroup 的子进程必须纳入预算。 [渲染服务](../../internal/services/html_render/service.go) |

## 优化顺序

| 顺序 | 优化 | 保持行为的条件与代价 |
| --- | --- | --- |
| 1 | 给麦麦小工具做 Go 版可选替换，保留现有 Python Runner 供社区插件使用。优先复用 Go jieba，开发期由锁定版本的拼音和词频数据生成紧凑只读表，关键词和错字继续走现有 Hook。 | 保留插件 ID、配置、快捷关键词、两种错字模式、保护片段和纠正行为。同一词的多音字和中性声调都要与 Python 对照。整词候选、同分排序和概率分布要验算。按热态剖析，消除 Python 重复数据有约 100–150 MiB 的毛收益空间，Go 实现保留的表仍需实测并扣除。 [插件行为](../../plugins/built_in/chinese_typo/plugin.py) · [榫卯](../../README.md) |
| 2 | 压缩 Go 共享词典。先避免 `Scanner.Text` 保留整行，随后试紧凑 Trie 或排序词表和前缀索引，另用小型可变层处理用户词典。 | 对齐原词频、总词频、零频前缀、HMM、词典更新、分词次序。第一步对应约 10.5 MiB 的当前归因，实际只释放整行与词条之差。整体先按 10–20 MiB 的节省目标做 A/B，避免把这笔目标和第一步叠加。 |
| 3 | 在保证原图与上下文的前提下，减少媒体重复持有。保存一次原始媒体，缓存用哈希指向已有内容，供应商请求只在最终编组时编码。为解码和缩略图设置按字节的动态并发预算。 | 缓存可先对照 32、16、8 MiB 上限。降低 32→8 MiB 最多节约 24 MiB 满缓存空间，命中率下降会增加磁盘读、缩放和首次请求耗时。大图排队时应保持发送顺序和可见进度。 |
| 4 | 预发布环境缩减 SQLite 页缓存与空闲池，增加对普通对话和批量维护的并发约束。 | 从主库和记忆库每连接 8–16 MiB、记忆库 2–4 个连接、1–2 个空闲连接的候选组合起测。维持现有 WAL、事务、外键和持久性语义。对比 `SQLITE_BUSY`、查询延迟和导入速度，收益在大库和多连接场景更明显。 |
| 5 | 当向量规模开始挤占预算时，改为连续行块、只读文件映射或定长缓冲读取，保留原始位宽，以同样的精确内积和有界 Top-K 搜索。 | 与现有精确排序、并列规则、过滤和 tombstone 保持一致。索引重建、切换和旧视图复用盘上快照，避免短时间装进两份全量数组。文件页仍由 cgroup 计费，尤其要测全库扫完后的峰值和冷读延迟。 |
| 6 | 在业务内存稳定后微调 Go GC。 | 用 `GOGC=80/60` 和 `GOMEMLIMIT=160/120MiB` 做对照，记录 GC CPU 和 p95 延迟。对不同大小的 Bot 可配独立参数。热态堆本身接近上限时，继续降低上限会增加 GC 压力。 |

若希望先交付一个小改动，可把 Python 插件的拼音导入延迟到规则模式首次使用，关键词反应与词库模式可避开整套拼音对象。词库模式仍会建立 Python jieba 字典。规则模式一旦热起来，最终收益取决于是否去掉重复词典，因此延迟导入只适合作为补充。

## 300 MB 与 200 MB 的边界

| 目标 | 可行性与实施组合 |
| --- | --- |
| **约 300 MB** | 单 Bot、保留现有第三方 Runner、使用远端模型且向量体量适中，只加载游戏插件的全树峰值 PSS 已为 232 MiB，压缩词典和媒体复制可给新实现与文件页腾出空间。优先实施第 1–3 项，再按生产采样调 SQLite。可用全树 PSS p95 ≤ 220 MiB 作筛选门槛，为文件页、内核和峰值保留空间，最终以 cgroup 峰值 ≤ 286 MiB 验收。 |
| **约 200 MB** | 适合作为小到中型实例的严格优化目标。保留第三方 Runner 时，只加载游戏插件的本地峰值仍比 191 MiB 高约 41 MiB PSS。还需把 Go 词典压缩、约束 SQLite 和媒体突发，并使增长中的 A_Memorix 向量能够从磁盘按块精确搜索。先在全树 PSS p95 ≤ 150 MiB 的档位压测，再以 cgroup 峰值 ≤ 191 MiB 验收。长期档位取决于数据规模、可接受的冷读取延迟和插件负载。 |

对 200 MB 的第一轮验证，保留原图、上下文窗口、完整记忆、1,024 维 embedding 及精确检索结果。优先权衡磁盘 I/O、首次访问延迟及大图并发。多 Bot、同机浏览器或本地模型需要另算预算。

## 生产验收

1. 在相同 Bot、数据集和插件版本下记录冷启动、首条中文、图文并发、空闲回落和重启。用 24–72 小时覆盖媒体处理、稀疏检索、向量重建、学习与 Dashboard 长连接。
2. 比较服务内存峰值、p95、`memory.events` 的 `high` 与 `oom_kill`。按 PID 记录 PSS 与匿名页，区分页缓存和堆。同时比较回复 p95、后台 p95、GC CPU、SQLite busy 比例和媒体队列长度。建议以 p95 回复延迟增幅 ≤ 10% 作为试运行门槛。
3. 回放文本、普通图片、表情包、消息引用、错字与纠正、关键词 Hook、插件重载、记忆检索及写回。核对 Planner/VLM 仍有图片附件，以及 A_Memorix 精确检索返回的 ID、顺序和分数。先在同一台机器上以旧版对照，再决定是否调整 systemd 内存上限。
4. 宿主部署模板提供了仅监听回环的 Go pprof 配置。生产可在服务器内用 `curl --fail http://127.0.0.1:6060/debug/pprof/heap` 抓 Worker profile，使用 `go tool pprof -top -sample_index=inuse_space` 分析归因。需要确认 GC 后堆时，再单独抓一次 `heap?gc=1`，常态采样避免频繁强制 GC。 [pprof 启动](../../internal/bootstrap/pprof_server.go)

在服务器本机可用以下只读命令采集一次，沿同一口径定期记录即可。

```bash
maigo_cgroup="/sys/fs/cgroup$(systemctl show maigo.service -p ControlGroup --value)"
for maigo_file in memory.current memory.peak memory.events; do
    printf '%s\n' "$maigo_file"
    cat "$maigo_cgroup/$maigo_file"
done
awk '$1 ~ /^(anon|file|inactive_file|kernel|sock)$/ { print }' "$maigo_cgroup/memory.stat"
while read -r maigo_pid; do
    printf 'PID %s\n' "$maigo_pid"
    awk '/^(Rss|Pss|Pss_Anon|Pss_File):/ { print }' "/proc/$maigo_pid/smaps_rollup"
done < "$maigo_cgroup/cgroup.procs"
```

报告基于源码、离线实验和线上后台只读状态。两档内存目标均待生产数据回放和 cgroup 验收。
