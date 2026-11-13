// Package emojisystem 对齐 MaiBot src/emoji_system：情绪标签解析、候选采样与按情绪选图。
//
// 持久化复用 internal/memory 的 images 表（image_type='EMOJI'），与 Python 共用单表口径。
// Describe 经 TaskVLM + emoji_content_analysis 写 description；RunMaintenance 对齐
// periodic_emoji_maintenance 与 check_emoji_file_integrity：扫描 MAIBOT_STICKER_DIR 新文件、
// SHA256 去重、修正扩展名、写入 images，满额且 do_replace 时走 emoji_replace 决策。
// 完整性检查只删除已注册且文件丢失的记录，未注册的保留为描述缓存。
package emojisystem
