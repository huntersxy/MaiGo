// Package pluginmarket 对齐 MaiBot src/plugin_market：插件市场信息源、发布索引与安装。
//
// Python 对照：sources.py（官方 HTTP 与 GitHub/CNB/自定义 Git 源）、service.py
// （snapshot/catalog 合并、search/get_detail）、releases.py 与 catalog.py（发布索引校验）、
// documents.py 与 engagement.py（README、统计互动）、routers/plugin/*_install.py（安装链路）。
//
// 差异以 MaiBot 原版对应实现为准。
package pluginmarket
