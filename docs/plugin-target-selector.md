# 插件配置目标选择

`x-widget: target` 用于搜索并多选已知群或用户。列表元素包含 `platform`、`type`、`id` 和 `name`：

- `platform` 的默认值限制平台，空字符串不限平台。
- `type` 的默认值必须为 `group` 或 `user`。
- `id` 为平台真实 ID，必须非空；`name` 仅供展示，不参与权限判断。

字段通过 `ui_type: target` 和 `item_fields` 交给 Dashboard。目标表示平台账号或群，不是某个机器人的投递路由，不能代替包含 `account_id`、`scope` 的会话身份。

`GET /api/chat/targets` 按平台和 ID 返回去重后的目标。群从 ChatSession 查询，用户从 PersonInfo 查询，未建立私聊的用户也可选择。搜索默认返回至多 50 项，`limit` 可在 1 至 100 之间指定。

QQ 官方适配器的禁言、撤回和踢人允许群，以及禁言和踢人保护用户，均使用结构化目标。关闭映射时保护名单只匹配 OpenID，开启后可匹配关联身份。
