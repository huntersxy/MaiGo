package migration

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// migrateV54 把 Go 自造的 stickers 表收敛到 Python 的 images 单表。
//
// Python 只有一张 images 表，用 image_type 区分 EMOJI / IMAGE；Go 早期单独建了
// stickers 表，列名与语义都不同（file_hash/file_name/use_count vs
// image_hash/full_path/query_count），且缺 full_path/no_file_flag/record_time 等列。
//
// image_type 落库值是 SQLAlchemy Enum 的枚举名（大写），依据 Python v2_to_v3.py:240
// 的 `image_type != "EMOJI"` 与 VARCHAR(5) 宽度。full_path 按
// serialize_stored_image_path 语义存相对实例根的 POSIX 路径；迁移期拿不到运行期
// 配置，按 basename 归一为 data/emoji/<file_name>。
func migrateV54(ctx context.Context, q querier) error {
	// 1) 建 images 表（v17 新建库已含此表时 ensure 语义由 IF NOT EXISTS 保证）
	if err := q.Exec(ctx, imagesTableCreateStatement(q.Dialect())); err != nil {
		return err
	}
	if err := ensureIndex(ctx, q, "ix_images_image_hash", "images", "image_hash"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, q, "ix_images_record_time", "images", "record_time"); err != nil {
		return err
	}
	// 2) 早期 Go 版本按小写枚举值写库，统一成 Python 的枚举名
	if err := q.Exec(ctx, "UPDATE images SET image_type = UPPER(image_type) WHERE image_type IN ('emoji','image')"); err != nil {
		return err
	}
	// 3) 备份（v54 会删表，这里留一份可回溯的 CSV 快照；仅 PG 有 export，SQLite 直接拷文件即可）
	if q.Dialect() == DialectPostgres {
		if err := backupImagesSnapshot(ctx, q); err != nil {
			return err
		}
	}
	// 4) stickers → images 数据搬运
	legacy, err := tableExists(ctx, q, "stickers")
	if err != nil {
		return err
	}
	if legacy {
		if err := migrateStickersIntoImages(ctx, q); err != nil {
			return err
		}
	}
	// 5) 删除 Go 自造表（索引随表消失）
	return dropTable(ctx, q, "stickers")
}

// imagesTableCreateStatement 返回对齐 Python Images 模型的 PostgreSQL 建表语句
//
// Python 生产库是 SQLite，SQLModel 编译出的列类型为：
// image_hash VARCHAR(255)、description VARCHAR、full_path VARCHAR(1024)、
// image_type VARCHAR(5)、record_time/register_time/last_used_time DATETIME。
// PostgreSQL 没有无长度的 VARCHAR，description/full_path 用 TEXT 等价表达
// （TEXT 无长度上限，比 VARCHAR(1024) 更宽松；SQLite 本身也不强制长度）。
// 其余列按 SQLModel 默认值翻译，image_type 的 CHECK 镜像 SQLAlchemy Enum 的取值集合。
func imagesTableCreateStatement(d dialect) string {
	idColumn := "BIGSERIAL PRIMARY KEY"
	timeType := "TIMESTAMPTZ"
	now := "now()"
	if d == DialectSQLite {
		idColumn = "INTEGER PRIMARY KEY AUTOINCREMENT"
		timeType = "DATETIME"
		now = "CURRENT_TIMESTAMP"
	}
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS images (
	id %s,
	image_hash VARCHAR(255) NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	full_path TEXT NOT NULL,
	image_type VARCHAR(5) NOT NULL DEFAULT 'EMOJI' CHECK (image_type IN ('EMOJI','IMAGE')),
	query_count BIGINT NOT NULL DEFAULT 0,
	is_registered BOOLEAN NOT NULL DEFAULT FALSE,
	is_banned BOOLEAN NOT NULL DEFAULT FALSE,
	no_file_flag BOOLEAN NOT NULL DEFAULT FALSE,
	record_time %s NOT NULL DEFAULT %s,
	register_time %s,
	last_used_time %s,
	vlm_processed BOOLEAN NOT NULL DEFAULT FALSE
)`, idColumn, timeType, now, timeType, timeType)
}

// migrateStickersIntoImages 把 stickers 表已有表情记录搬进 images
//
// 幂等：按 (image_hash, image_type='EMOJI') 判重，重复执行不会产生重复行。
func migrateStickersIntoImages(ctx context.Context, q querier) error {
	now := "now()"
	if q.Dialect() == DialectSQLite {
		now = "CURRENT_TIMESTAMP"
	}
	statement := fmt.Sprintf(`INSERT INTO images (
	image_hash, description, full_path, image_type, query_count,
	is_registered, is_banned, no_file_flag, record_time,
	register_time, last_used_time, vlm_processed)
SELECT
	s.file_hash,
	COALESCE(s.description, ''),
	'data/emoji/' || s.file_name,
	'EMOJI',
	COALESCE(s.use_count, 0),
	COALESCE(s.is_registered, TRUE),
	COALESCE(s.is_banned, FALSE),
	FALSE,
	COALESCE(s.created_at, %s),
	COALESCE(s.updated_at, s.created_at, %s),
	COALESCE(s.updated_at, s.created_at, %s),
	COALESCE(s.vlm_processed, FALSE)
FROM stickers s
WHERE NOT EXISTS (
	SELECT 1 FROM images i WHERE i.image_hash = s.file_hash AND i.image_type = 'EMOJI'
)`, now, now, now)
	if err := q.Exec(ctx, statement); err != nil {
		return fmt.Errorf("搬运 stickers 到 images 失败: %w", err)
	}
	return nil
}

// imagesBackupFileName v54 生成的 images 快照文件名
const imagesBackupFileName = "images-backup-v54.csv"

// imagesBackupDir 返回快照输出目录：<实例根>/data
//
// 与 SQLite 库文件同处，随 data 卷一起备份，不会落到源码仓库目录。
func imagesBackupDir() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); value != "" {
		return filepath.Join(value, "data")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Join(cwd, "data")
}

// backupImagesSnapshot 把 images 全量导出为 CSV，放在进程工作目录
//
// 时间列在 SQL 里 ::text 转字符串，避免驱动把 timestamptz 塞进 sql.NullString 失败。
// 每次执行都覆盖，二次迁移时刷新为最新快照。
func backupImagesSnapshot(ctx context.Context, q querier) error {
	rows, err := q.Query(ctx, `SELECT id, image_hash, description, full_path, image_type,
		query_count, is_registered, is_banned, no_file_flag,
		record_time::text, register_time::text, last_used_time::text, vlm_processed
		FROM images ORDER BY id`)
	if err != nil {
		return fmt.Errorf("导出 images 快照失败: %w", err)
	}
	defer rows.Close()

	// 快照写到实例 data 目录（与库文件同处），不再落进程工作目录污染仓库
	targetDir := imagesBackupDir()
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("导出 images 快照失败: %w", err)
	}
	file, err := os.Create(filepath.Join(targetDir, imagesBackupFileName))
	if err != nil {
		return fmt.Errorf("导出 images 快照失败: %w", err)
	}
	defer file.Close()
	writer := csv.NewWriter(file)

	header := []string{
		"id", "image_hash", "description", "full_path", "image_type",
		"query_count", "is_registered", "is_banned", "no_file_flag",
		"record_time", "register_time", "last_used_time", "vlm_processed",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("导出 images 快照失败: %w", err)
	}
	for rows.Next() {
		var (
			id           int64
			imageHash    string
			description  string
			fullPath     string
			imageType    string
			queryCount   int64
			isRegistered bool
			isBanned     bool
			noFileFlag   bool
			recordTime   string
			registerTime sql.NullString
			lastUsedTime sql.NullString
			vlmProcessed bool
		)
		if err := rows.Scan(&id, &imageHash, &description, &fullPath, &imageType,
			&queryCount, &isRegistered, &isBanned, &noFileFlag,
			&recordTime, &registerTime, &lastUsedTime, &vlmProcessed); err != nil {
			return fmt.Errorf("导出 images 快照失败: %w", err)
		}
		record := []string{
			fmt.Sprint(id), imageHash, description, fullPath, imageType,
			fmt.Sprint(queryCount), fmt.Sprint(isRegistered), fmt.Sprint(isBanned),
			fmt.Sprint(noFileFlag), recordTime, registerTime.String,
			lastUsedTime.String, fmt.Sprint(vlmProcessed),
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("导出 images 快照失败: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("导出 images 快照失败: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("导出 images 快照失败: %w", err)
	}
	return nil
}
