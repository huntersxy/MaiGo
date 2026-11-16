package search

// WebUI 答疑历史与续聊上下文，对齐 src/webui/services/ai_help_history.py

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	database "MaiGo/internal/common/database"
)

// HelpTurnReply 保存到答疑轮次中的回复摘要
type HelpTurnReply struct {
	Answer      string         `json:"answer"`
	Suggestions []string       `json:"suggestions"`
	Sources     []Source       `json:"sources"`
	Artifacts   []HelpArtifact `json:"artifacts"`
}

// HelpTurnDetail 一轮答疑
type HelpTurnDetail struct {
	ID       int            `json:"id"`
	Question string         `json:"question"`
	Reply    *HelpTurnReply `json:"reply"`
	Error    string         `json:"error"`
}

// HelpSessionSummary 答疑会话列表项
type HelpSessionSummary struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	UpdatedAt float64 `json:"updated_at"`
}

// HelpSessionDetail 答疑会话详情
type HelpSessionDetail struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	UpdatedAt float64          `json:"updated_at"`
	Turns     []HelpTurnDetail `json:"turns"`
}

// HelpHistoryPair 续聊时带入的问题与答案
type HelpHistoryPair struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// ListHelpSessions 最近 50 个答疑会话
func ListHelpSessions(db *gorm.DB) ([]HelpSessionSummary, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	var rows []database.WebUIHelpSession
	if err := db.Order("updated_at DESC, id").Limit(50).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]HelpSessionSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, HelpSessionSummary{ID: row.ID, Title: row.Title, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

// GetHelpSession 读取会话与全部轮次
func GetHelpSession(db *gorm.DB, sessionID string) (HelpSessionDetail, bool, error) {
	if db == nil {
		return HelpSessionDetail{}, false, fmt.Errorf("数据库未初始化")
	}
	var session database.WebUIHelpSession
	if err := db.Where("id = ?", sessionID).Take(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return HelpSessionDetail{}, false, nil
		}
		return HelpSessionDetail{}, false, err
	}
	var turns []database.WebUIHelpTurn
	if err := db.Where("session_id = ?", sessionID).Order("id").Find(&turns).Error; err != nil {
		return HelpSessionDetail{}, false, err
	}
	detail := HelpSessionDetail{ID: session.ID, Title: session.Title, UpdatedAt: session.UpdatedAt}
	detail.Turns = make([]HelpTurnDetail, 0, len(turns))
	for _, turn := range turns {
		item := HelpTurnDetail{ID: int(turn.ID), Question: turn.Question, Error: turn.Error}
		if turn.ReplyJSON != nil {
			var reply HelpTurnReply
			if json.Unmarshal([]byte(*turn.ReplyJSON), &reply) == nil {
				item.Reply = &reply
			}
		}
		detail.Turns = append(detail.Turns, item)
	}
	return detail, true, nil
}

// DeleteHelpSession 在同一事务中删除会话与轮次
func DeleteHelpSession(db *gorm.DB, sessionID string) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("数据库未初始化")
	}
	deleted := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var session database.WebUIHelpSession
		if err := tx.Where("id = ?", sessionID).Take(&session).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		deleted = true
		if err := tx.Where("session_id = ?", sessionID).Delete(&database.WebUIHelpTurn{}).Error; err != nil {
			return err
		}
		return tx.Delete(&session).Error
	})
	return deleted, err
}

// BeginHelpTurn 登记新一轮问题；新会话直接创建，旧会话读取最近八轮成功问答
func BeginHelpTurn(db *gorm.DB, sessionID, question string, newSession bool) (int, []HelpHistoryPair, bool, error) {
	if db == nil {
		return 0, nil, false, fmt.Errorf("数据库未初始化")
	}
	var turnID int
	history := []HelpHistoryPair{}
	err := db.Transaction(func(tx *gorm.DB) error {
		now := float64(time.Now().UnixNano()) / 1e9
		if newSession {
			if err := tx.Create(&database.WebUIHelpSession{ID: sessionID, Title: question, UpdatedAt: now}).Error; err != nil {
				return err
			}
		} else {
			var session database.WebUIHelpSession
			if err := tx.Where("id = ?", sessionID).Take(&session).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return nil
				}
				return err
			}
			if err := tx.Model(&database.WebUIHelpSession{}).Where("id = ?", sessionID).
				Update("updated_at", now).Error; err != nil {
				return err
			}
			var previous []database.WebUIHelpTurn
			if err := tx.Where("session_id = ? AND reply_json IS NOT NULL AND error = ''", sessionID).
				Order("id DESC").Limit(8).Find(&previous).Error; err != nil {
				return err
			}
			for index := len(previous) - 1; index >= 0; index-- {
				var reply HelpTurnReply
				if previous[index].ReplyJSON == nil || json.Unmarshal([]byte(*previous[index].ReplyJSON), &reply) != nil {
					continue
				}
				answer := reply.Answer
				if runes := []rune(answer); len(runes) > 2000 {
					answer = string(runes[:2000])
				}
				history = append(history, HelpHistoryPair{Question: previous[index].Question, Answer: answer})
			}
		}
		turn := database.WebUIHelpTurn{SessionID: sessionID, Question: question}
		if err := tx.Create(&turn).Error; err != nil {
			return err
		}
		turnID = int(turn.ID)
		return nil
	})
	if turnID == 0 {
		return 0, nil, false, err
	}
	return turnID, history, true, err
}

// FinishHelpTurn 保存回复或错误；回复里的 grounding_error 同时写入轮次错误
func FinishHelpTurn(db *gorm.DB, turnID int, response *Response, errorMessage string) error {
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	updates := map[string]any{}
	if response != nil {
		reply := HelpTurnReply{
			Answer:      response.Answer,
			Suggestions: response.Suggestions,
			Sources:     response.Sources,
			Artifacts:   response.Artifacts,
		}
		encoded, err := json.Marshal(reply)
		if err != nil {
			return err
		}
		updates["reply_json"] = string(encoded)
		updates["error"] = response.GroundingError
	} else {
		updates["error"] = errorMessage
	}
	return db.Model(&database.WebUIHelpTurn{}).Where("id = ?", turnID).Updates(updates).Error
}
