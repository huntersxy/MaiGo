# 人物身份合并与数据迁移

本命令把同一平台的来源用户合并到目标用户。它不会自动判定两个 ID 是否属于同一人。历史消息、旧私聊及其归属不改写；迁移后新消息使用已登记的别名，私聊发送按连接和作用域反查平台接收者。

```powershell
MaiGo person-migrate --directory C:/Bots/mai preview --platform qq --source OLD_ID --target NEW_ID --output C:/Temp/person-plan.json
MaiGo person-migrate --directory C:/Bots/mai execute --plan C:/Temp/person-plan.json
MaiGo person-migrate --directory C:/Bots/mai recover --job C:/Bots/mai/data/person-migrations/JOB_ID
```

`--directory` 指向单个实例。检查 `person-plan.review.json` 中的变更计数和冲突。若有冲突，为每个冲突 ID 选择 `source` 或 `target`，将映射作为 JSON 传给 `preview --decisions` 重新生成计划。不要编辑计划正文。执行时在写锁内重建计划，已确认冲突变化会要求重新预览。

主库与参与的 A_Memorix 数据库在同一次 SQLite 写事务中提交，并分别记录提交标记。SQLite 在多个 WAL 数据库遇到进程或系统故障时不保证跨库原子性；`recover` 核对标记并更新任务记录，不会覆盖数据。

Go 的 `--extension` 接受通过 `services.RegisterPersonMigrationExtension` 编译登记的名称，不加载 Python callable。扩展数据库必须位于实例内，且不能与其他参与库重复。
