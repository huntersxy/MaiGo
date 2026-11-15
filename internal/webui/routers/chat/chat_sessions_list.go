package chat

import (
	"context"
	"sort"
	"strconv"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/core/internal_chat"
	"MaiGo/internal/platform_io"
)

type SessionListResult struct {
	Items []chatSessionItem
	Total int
}

func ListChatSessions(ctx context.Context, mem *memory.Manager, limit, offset int) (SessionListResult, error) {
	sessionMgr := chatsession.NewManager(mem.GetDB())
	_ = sessionMgr.LoadAllFromDB(ctx)

	items := make([]chatSessionItem, 0, limit+offset)
	for _, kind := range []string{memory.ConversationKindGroup, memory.ConversationKindPrivate} {
		rows, err := mem.ListConversationTargets(ctx, kind, false)
		if err != nil {
			return SessionListResult{}, err
		}
		for _, row := range rows {
			if kind == memory.ConversationKindPrivate && row.TargetID == internalchat.OwnerNumericID {
				continue
			}
			key := chatsession.ConversationKey(kind, row.TargetID)
			item := chatSessionItem{
				ConversationKey: key,
				Kind:            kind,
				TargetID:        row.TargetID,
				Name:            row.Name,
				Active:          row.Active,
				Blocked:         row.Blocked,
			}
			if rec, err := sessionMgr.ResolveSession(ctx, platformio.PlatformQQ, strconv.FormatInt(row.TargetID, 10), kind); err == nil && rec != nil {
				item.SessionID = rec.SessionID
				if display := chatsession.DisplayName(rec); display != "" {
					item.Name = display
				}
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Active != items[j].Active {
			return items[i].Active
		}
		return items[i].ConversationKey < items[j].ConversationKey
	})
	total := len(items)
	if offset >= total {
		items = nil
	} else {
		end := offset + limit
		if end > total {
			end = total
		}
		items = items[offset:end]
	}
	return SessionListResult{Items: items, Total: total}, nil
}
