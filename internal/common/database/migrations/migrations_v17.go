package migration

import (
	"fmt"

	"gorm.io/gorm"
)

const v17BaseName = "clean_semantic_session_schema"

func initializeV17Schema(db *gorm.DB) error {
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE mai_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id BIGINT NOT NULL,
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('group','private')),
			target_id BIGINT NOT NULL CHECK(target_id>0),
			user_id BIGINT NOT NULL,
			user_nickname TEXT NOT NULL,
			processed_plain_text TEXT NOT NULL,
			reply_to BIGINT,
			is_mentioned BOOLEAN NOT NULL DEFAULT false,
			timestamp DATETIME NOT NULL,
			received_at DATETIME,
			reply_frequency REAL,
			raw_content BLOB,
			media_refs TEXT,
			platform TEXT NOT NULL DEFAULT 'qq',
			session_id TEXT,
			external_message_id TEXT NOT NULL DEFAULT '',
			external_target_id TEXT NOT NULL DEFAULT '',
			external_user_id TEXT NOT NULL DEFAULT '',
			account_id TEXT NOT NULL DEFAULT '',
			scope TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX uq_message_onebot_id ON mai_messages(message_id)`,
		`CREATE INDEX idx_mai_messages_scope_id ON mai_messages(conversation_kind,target_id,id)`,
		`CREATE INDEX idx_mai_messages_time ON mai_messages(timestamp)`,
		`CREATE INDEX idx_mai_messages_received_at ON mai_messages(received_at)`,
		`CREATE INDEX idx_mai_messages_session_time ON mai_messages(session_id,timestamp DESC)`,
		`CREATE TABLE maisaka_monitor_events (
			event_id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			timestamp REAL NOT NULL,
			schema_version INTEGER NOT NULL DEFAULT 1,
			payload_json TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX ix_maisaka_monitor_events_event_type ON maisaka_monitor_events(event_type)`,
		`CREATE INDEX ix_maisaka_monitor_events_session_id ON maisaka_monitor_events(session_id)`,
		`CREATE INDEX ix_maisaka_monitor_events_session_event ON maisaka_monitor_events(session_id,event_id)`,
		`CREATE INDEX ix_maisaka_monitor_events_type_event ON maisaka_monitor_events(event_type,event_id)`,
		`CREATE INDEX ix_maisaka_monitor_events_timestamp ON maisaka_monitor_events(timestamp)`,
		`CREATE INDEX ix_maisaka_monitor_events_created_at ON maisaka_monitor_events(created_at)`,
		`CREATE TABLE message_forwards (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			handle TEXT NOT NULL UNIQUE,
			message_log_id BIGINT NOT NULL REFERENCES mai_messages(id) ON DELETE CASCADE,
			parent_handle TEXT REFERENCES message_forwards(handle) ON DELETE CASCADE,
			nodes TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE conversation_targets (
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('group','private')),
			target_id BIGINT NOT NULL CHECK(target_id>0),
			name TEXT NOT NULL DEFAULT '',
			remote_remark TEXT NOT NULL DEFAULT '',
			blocked BOOLEAN NOT NULL DEFAULT false,
			extra_prompt TEXT NOT NULL DEFAULT '',
			active BOOLEAN NOT NULL DEFAULT true,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(conversation_kind,target_id)
		)`,
		`CREATE INDEX idx_conversation_targets_list ON conversation_targets(conversation_kind,active DESC,target_id)`,
		`CREATE TABLE chat_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL UNIQUE,
			created_timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_active_timestamp DATETIME,
			user_id TEXT,
			user_nickname TEXT,
			user_cardname TEXT,
			group_id TEXT,
			group_name TEXT,
			platform TEXT NOT NULL,
			account_id TEXT,
			scope TEXT
		)`,
		`CREATE INDEX idx_chat_sessions_platform ON chat_sessions(platform)`,
		`CREATE INDEX idx_chat_sessions_group_id ON chat_sessions(group_id)`,
		`CREATE INDEX idx_chat_sessions_user_id ON chat_sessions(user_id)`,
		`CREATE INDEX idx_chat_sessions_account_id ON chat_sessions(account_id)`,
		`CREATE INDEX idx_chat_sessions_scope ON chat_sessions(scope)`,
		`CREATE TABLE conversation_sessions (
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('group','private')),
			target_id BIGINT NOT NULL CHECK(target_id>0),
			next_input_seq BIGINT NOT NULL DEFAULT 0,
			next_visible_seq BIGINT NOT NULL DEFAULT 0,
			reference_seen TEXT NOT NULL DEFAULT '{"auto":{},"tool":{}}',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(conversation_kind,target_id)
		)`,
		`CREATE TABLE conversation_transcript (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('group','private')),
			target_id BIGINT NOT NULL CHECK(target_id>0),
			input_seq BIGINT,
			visible_seq BIGINT,
			entry_kind TEXT NOT NULL CHECK(entry_kind IN ('user','assistant','tool','internal_reference','observation','agent_message','system_prompt','context_digest','group_note','system_note')),
			message_log_id BIGINT REFERENCES mai_messages(id) ON DELETE RESTRICT,
			run_id TEXT NOT NULL DEFAULT '',
			tool_call_id TEXT NOT NULL DEFAULT '',
			tool_name TEXT NOT NULL DEFAULT '',
			tool_status TEXT NOT NULL DEFAULT '' CHECK(tool_status IN ('','planned','running','succeeded','failed','unknown','not_executed')),
			payload TEXT NOT NULL,
			result_ref TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE UNIQUE INDEX uq_transcript_input_seq ON conversation_transcript(conversation_kind,target_id,input_seq) WHERE input_seq IS NOT NULL`,
		`CREATE UNIQUE INDEX uq_transcript_visible_seq ON conversation_transcript(conversation_kind,target_id,visible_seq) WHERE visible_seq IS NOT NULL`,
		`CREATE UNIQUE INDEX uq_transcript_tool_call ON conversation_transcript(conversation_kind,target_id,run_id,tool_call_id) WHERE entry_kind='tool' AND tool_call_id<>''`,
		`CREATE TABLE topic_threads (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('group','private')),
			target_id BIGINT NOT NULL CHECK(target_id>0),
			status TEXT NOT NULL DEFAULT 'dormant' CHECK(status IN ('active','dormant')),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_topic_threads_scope ON topic_threads(conversation_kind,target_id,status,updated_at DESC,id DESC)`,
		`CREATE TABLE topic_assignments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_log_id BIGINT NOT NULL UNIQUE REFERENCES mai_messages(id) ON DELETE CASCADE,
			topic_id BIGINT REFERENCES topic_threads(id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX idx_topic_assignments_topic ON topic_assignments(topic_id,message_log_id)`,
		`CREATE TABLE topic_summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			topic_id BIGINT NOT NULL REFERENCES topic_threads(id) ON DELETE RESTRICT,
			version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
			summary_json TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(topic_id,version)
		)`,
		`CREATE TABLE topic_summary_sources (
			summary_id BIGINT NOT NULL REFERENCES topic_summaries(id) ON DELETE CASCADE,
			message_log_id BIGINT NOT NULL REFERENCES mai_messages(id) ON DELETE RESTRICT,
			is_anchor BOOLEAN NOT NULL DEFAULT false,
			PRIMARY KEY(summary_id,message_log_id)
		)`,
		`CREATE INDEX idx_topic_sources_anchor ON topic_summary_sources(summary_id,is_anchor)`,
		`CREATE TABLE context_compaction_checkpoints (
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('group','private')),
			target_id BIGINT NOT NULL CHECK(target_id>0),
			version BIGINT NOT NULL DEFAULT 0,
			through_visible_seq BIGINT,
			through_message_log_id BIGINT,
			overview TEXT NOT NULL DEFAULT '',
			active_topic_versions TEXT NOT NULL DEFAULT '[]',
			reference_baseline TEXT NOT NULL DEFAULT '',
			reference_versions TEXT NOT NULL DEFAULT '{}',
			batch_id TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(conversation_kind,target_id)
		)`,
		`CREATE TABLE knowledge_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			subject_type TEXT NOT NULL CHECK(subject_type IN ('group','user','self','object')),
			subject_target TEXT NOT NULL CHECK(trim(subject_target)<>''),
			memory_type TEXT NOT NULL CHECK(memory_type IN ('profile','experience')),
			label TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL CHECK(trim(content)<>''),
			pinned BOOLEAN NOT NULL DEFAULT false,
			status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
			version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
			content_hash TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_knowledge_subject ON knowledge_items(subject_type,subject_target,memory_type,status)`,
		`CREATE INDEX idx_knowledge_pinned ON knowledge_items(pinned,status)`,
		`CREATE INDEX idx_knowledge_status_updated ON knowledge_items(status,updated_at DESC,id DESC)`,
		`CREATE UNIQUE INDEX uq_knowledge_content ON knowledge_items(subject_type,subject_target,memory_type,content_hash)`,
		`CREATE TABLE knowledge_revisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			item_id BIGINT NOT NULL REFERENCES knowledge_items(id) ON DELETE CASCADE,
			version BIGINT NOT NULL,
			before_content TEXT NOT NULL DEFAULT '',
			after_content TEXT NOT NULL DEFAULT '',
			before_status TEXT NOT NULL DEFAULT '',
			after_status TEXT NOT NULL DEFAULT '',
			before_pinned BOOLEAN NOT NULL DEFAULT false,
			after_pinned BOOLEAN NOT NULL DEFAULT false,
			reason TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(item_id,version)
		)`,
		`CREATE INDEX idx_knowledge_revisions_item ON knowledge_revisions(item_id,version DESC)`,
		`CREATE TABLE emotion_states (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			about_user_id BIGINT NOT NULL DEFAULT 0,
			kind TEXT NOT NULL,
			strength REAL NOT NULL DEFAULT 0,
			occurred_at DATETIME NOT NULL,
			anchor_at DATETIME NOT NULL,
			decay_class TEXT NOT NULL DEFAULT 'lingering' CHECK(decay_class IN ('brief','lingering')),
			reason TEXT NOT NULL DEFAULT '',
			version BIGINT NOT NULL DEFAULT 1,
			ended_at DATETIME,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(about_user_id,kind)
		)`,
		`CREATE TABLE emotion_revisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			emotion_id BIGINT NOT NULL REFERENCES emotion_states(id) ON DELETE CASCADE,
			before_strength REAL NOT NULL DEFAULT 0,
			after_strength REAL NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			version BIGINT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(emotion_id,version)
		)`,
		`CREATE INDEX idx_emotion_current ON emotion_states(about_user_id,ended_at,anchor_at)`,
		`CREATE TABLE mood_state (
			id INTEGER PRIMARY KEY,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			energy REAL NOT NULL DEFAULT 0.5,
			sociability REAL NOT NULL DEFAULT 0.5,
			last_reason TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO mood_state(id,energy,sociability,last_reason) VALUES(1,0.5,0.5,'')`,
		`CREATE TABLE member_profiles (
			user_id BIGINT PRIMARY KEY,
			nickname TEXT NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE member_names (
			user_id BIGINT NOT NULL REFERENCES member_profiles(user_id) ON DELETE CASCADE,
			group_id BIGINT NOT NULL,
			value TEXT NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(user_id,group_id,value)
		)`,
		`CREATE TABLE person_info (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			is_known BOOLEAN NOT NULL DEFAULT false,
			person_id TEXT NOT NULL UNIQUE,
			person_name TEXT,
			name_reason TEXT,
			platform TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			user_nickname TEXT NOT NULL DEFAULT '',
			group_cardname VARCHAR,
			memory_points VARCHAR,
			know_counts BIGINT NOT NULL DEFAULT 0,
			first_known_time DATETIME,
			last_known_time DATETIME
		)`,
		`CREATE INDEX idx_person_info_platform ON person_info(platform)`,
		`CREATE INDEX idx_person_info_user_id ON person_info(user_id)`,
		`CREATE INDEX idx_person_info_person_name ON person_info(person_name)`,
		// images 对齐 MaiBot database_model.Images（src/common/database/database_model.py:133-155）
		// image_type 落库是 SQLAlchemy Enum 的枚举名（大写 EMOJI/IMAGE），见 v54 注释
		`CREATE TABLE images (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			image_hash VARCHAR(255) NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			full_path TEXT NOT NULL,
			image_type VARCHAR(5) NOT NULL DEFAULT 'EMOJI' CHECK (image_type IN ('EMOJI','IMAGE')),
			query_count BIGINT NOT NULL DEFAULT 0,
			is_registered BOOLEAN NOT NULL DEFAULT FALSE,
			is_banned BOOLEAN NOT NULL DEFAULT FALSE,
			no_file_flag BOOLEAN NOT NULL DEFAULT FALSE,
			record_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			register_time DATETIME,
			last_used_time DATETIME,
			vlm_processed BOOLEAN NOT NULL DEFAULT FALSE
		)`,
		`CREATE INDEX ix_images_image_hash ON images(image_hash)`,
		`CREATE INDEX ix_images_record_time ON images(record_time)`,
		`CREATE TABLE model_call_hourly (
			bucket_start DATETIME NOT NULL,
			task TEXT NOT NULL,
			model TEXT NOT NULL,
			request_count BIGINT NOT NULL DEFAULT 0,
			failure_count BIGINT NOT NULL DEFAULT 0,
			latency_ms_sum BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			usage_reported_count BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY(bucket_start,task,model)
		)`,
		`CREATE TABLE message_traffic_hourly (
			bucket_start DATETIME NOT NULL,
			direction TEXT NOT NULL CHECK(direction IN ('inbound','outbound')),
			message_count BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY(bucket_start,direction)
		)`,
		`CREATE TABLE message_traffic_chat_hourly (
			bucket_start DATETIME NOT NULL,
			direction TEXT NOT NULL CHECK(direction IN ('inbound','outbound')),
			conversation_key TEXT NOT NULL CHECK(trim(conversation_key)<>''),
			message_count BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY(bucket_start,direction,conversation_key)
		)`,
		`CREATE INDEX idx_message_traffic_chat_hourly_bucket ON message_traffic_chat_hourly(bucket_start DESC)`,
		`CREATE TABLE online_time_daily (
			day_date DATE NOT NULL PRIMARY KEY,
			online_seconds BIGINT NOT NULL DEFAULT 0 CHECK(online_seconds>=0)
		)`,
		`CREATE TABLE session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			from_session_kind TEXT NOT NULL CHECK(from_session_kind IN ('group','private')),
			from_target_id BIGINT NOT NULL CHECK(from_target_id>0),
			to_session_kind TEXT NOT NULL CHECK(to_session_kind IN ('group','private')),
			to_target_id BIGINT NOT NULL CHECK(to_target_id>0),
			text TEXT NOT NULL CHECK(trim(text)<>''),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE app_config (
			config_key TEXT PRIMARY KEY CHECK(trim(config_key)<>''),
			config_value TEXT NOT NULL,
			version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE model_profiles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			provider TEXT NOT NULL DEFAULT 'openai_responses',
			base_url TEXT NOT NULL,
			model_name TEXT NOT NULL,
			context_window INT NOT NULL CHECK(context_window>0),
			thinking_type TEXT NOT NULL DEFAULT '',
			reasoning_effort TEXT NOT NULL DEFAULT '',
			api_key_ciphertext BLOB NOT NULL,
			api_key_nonce BLOB NOT NULL,
			api_key_key_version INT NOT NULL DEFAULT 1,
			api_key_hint TEXT NOT NULL DEFAULT '',
			active BOOLEAN NOT NULL DEFAULT false,
			version BIGINT NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CHECK(thinking_type IN ('','enabled','disabled')),
			CHECK(reasoning_effort IN ('','low','medium','high','xhigh','max'))
		)`,
		`CREATE UNIQUE INDEX uq_model_profiles_active ON model_profiles(active) WHERE active`,
		`CREATE TABLE mcp_servers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			enabled BOOLEAN NOT NULL DEFAULT true,
			transport TEXT NOT NULL,
			config_ciphertext BLOB NOT NULL,
			config_nonce BLOB NOT NULL,
			key_version INT NOT NULL DEFAULT 1,
			version BIGINT NOT NULL DEFAULT 1,
			position INT NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE expressions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			situation TEXT NOT NULL,
			style TEXT NOT NULL,
			content_list TEXT NOT NULL DEFAULT '[]',
			count INT NOT NULL DEFAULT 0,
			last_active_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			create_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			session_id TEXT,
			checked BOOLEAN NOT NULL DEFAULT false,
			modified_by TEXT CHECK(modified_by IS NULL OR modified_by IN ('AI','USER'))
		)`,
		`CREATE INDEX idx_expressions_situation ON expressions(situation)`,
		`CREATE INDEX idx_expressions_style ON expressions(style)`,
		`CREATE INDEX idx_expressions_last_active ON expressions(last_active_time)`,
		`CREATE INDEX idx_expressions_session ON expressions(session_id)`,
		`CREATE TABLE jargons (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			evidence_messages TEXT,
			meaning TEXT NOT NULL,
			session_id_dict TEXT NOT NULL DEFAULT '{}',
			count INT NOT NULL DEFAULT 0,
			is_jargon BOOLEAN DEFAULT false,
			is_complete BOOLEAN NOT NULL DEFAULT false,
			is_global BOOLEAN NOT NULL DEFAULT false,
			last_inference_count INT NOT NULL DEFAULT 0,
			created_by TEXT NOT NULL DEFAULT 'AI' CHECK(created_by IN ('AI','MANUAL')),
			created_timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_jargons_content ON jargons(content)`,
		`CREATE INDEX idx_jargons_created ON jargons(created_timestamp)`,
		`CREATE TABLE high_frequency_terms (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id TEXT NOT NULL,
			term TEXT NOT NULL,
			rank INT NOT NULL DEFAULT 0,
			occurrence_count INT NOT NULL DEFAULT 0,
			message_count INT NOT NULL DEFAULT 0,
			frequency REAL NOT NULL DEFAULT 0,
			message_frequency REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(chat_id, term)
		)`,
		`CREATE INDEX idx_high_frequency_terms_chat_id ON high_frequency_terms(chat_id)`,
		`CREATE INDEX idx_high_frequency_terms_chat_rank ON high_frequency_terms(chat_id, rank)`,
		`CREATE INDEX idx_high_frequency_terms_updated ON high_frequency_terms(updated_at)`,
		// 列集与类型对齐 MaiBot src/files/models.py：
		// 时间列是 float（Unix 秒），不是 DATETIME；source/source_key 由模型直接声明；
		// Python create_all 不产生服务端默认值，这里同样不写 DEFAULT。
		`CREATE TABLE file_assets (
			file_ref TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			size BIGINT NOT NULL,
			sha256 TEXT NOT NULL,
			origin TEXT NOT NULL,
			state TEXT NOT NULL,
			source TEXT NOT NULL,
			source_key TEXT UNIQUE,
			error TEXT NOT NULL,
			created_at REAL NOT NULL,
			accessed_at REAL NOT NULL,
			reserved_bytes BIGINT NOT NULL,
			leased_until REAL NOT NULL,
			open_readers INTEGER NOT NULL,
			transfer_id TEXT NOT NULL,
			transfer_started REAL NOT NULL
		)`,
		`CREATE INDEX idx_file_assets_sha256 ON file_assets(sha256)`,
		`CREATE INDEX idx_file_assets_state ON file_assets(state)`,
		`CREATE TABLE file_parses (
			file_ref TEXT PRIMARY KEY REFERENCES file_assets(file_ref),
			parser_version TEXT NOT NULL,
			encoding TEXT NOT NULL,
			state TEXT NOT NULL,
			error TEXT NOT NULL,
			updated_at REAL NOT NULL
		)`,
		`CREATE TABLE file_bindings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_ref TEXT NOT NULL REFERENCES file_assets(file_ref),
			session_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			created_at REAL NOT NULL,
			UNIQUE(file_ref, session_id)
		)`,
		`CREATE INDEX idx_file_bindings_session ON file_bindings(session_id)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("初始化 v17 schema 失败: %w", err)
		}
	}
	return nil
}
