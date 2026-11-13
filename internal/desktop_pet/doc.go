// Package desktoppet 对齐 MaiBot src/desktop_pet：桌宠窗口生命周期、行协议、皮肤与图文请求
//
// Python 对照：desktop_pet/service.py DesktopPetService、pet_window.py 的 stdin/stdout 协议、
// skins.py 的皮肤校验与安装、attachments.py 的图文请求、rendering.py 的气泡转换
//
// Windows 上 spawn pet_window.py（MAIBOT_DESKTOP_PET=1），逐行读取事件、按 id 等待回执；
// 皮肤清单校验、zip 安装、WebUI 路由、desktop_pet.* 能力、聊天中继、notify_reply 与
// notify_thinking 由本包提供，内联图片经 data URL 进入 Agent 媒体缓存。
// 未实现：PIL 像素级校验。
package desktoppet
