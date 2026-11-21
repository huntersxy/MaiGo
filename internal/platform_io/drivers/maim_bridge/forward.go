package maimbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/common/message_server"
	"MaiGo/internal/platform_io"
)

func forwardComponentFromSegments(nodes []platformio.ForwardNode) *messagecomponent.ForwardNodeComponent {
	if len(nodes) == 0 {
		return nil
	}
	forward := &messagecomponent.ForwardNodeComponent{ForwardComponents: make([]messagecomponent.ForwardComponent, 0, len(nodes))}
	for _, node := range nodes {
		components := make([]messagecomponent.Component, 0, len(node.Segments))
		for _, segment := range node.Segments {
			components = append(components, forwardMessageComponent(segment))
		}
		forward.ForwardComponents = append(forward.ForwardComponents, messagecomponent.ForwardComponent{
			UserID: forwardString(node.UserID), UserNickname: node.Nickname, UserCardname: forwardString(node.Cardname),
			MessageID: node.MessageID, Content: components,
		})
	}
	return forward
}

func forwardMessageComponent(segment platformio.Segment) messagecomponent.Component {
	switch segment.Kind {
	case platformio.SegmentText:
		return messagecomponent.TextComponent{Text: segment.Text}
	case platformio.SegmentAt:
		return messagecomponent.AtComponent{TargetUserID: segment.AtUserID, Nickname: forwardString(segment.Extra["name"]), Cardname: forwardString(segment.Extra["cardname"])}
	case platformio.SegmentReply:
		return messagecomponent.ReplyComponent{
			TargetMessageID: firstTextValue(segment.Extra["id"], segment.Text), TargetContent: forwardString(segment.Extra["target_message_content"]),
			SenderID: forwardString(segment.Extra["sender_id"]), SenderNickname: forwardString(segment.Extra["sender_nickname"]), SenderCardname: forwardString(segment.Extra["sender_cardname"]),
		}
	case platformio.SegmentForward:
		if forward := forwardComponentFromSegments(segment.ForwardNodes); forward != nil {
			return *forward
		}
	case platformio.SegmentImage, platformio.SegmentEmoji, platformio.SegmentVoice, platformio.SegmentRecord:
		binary, _ := base64.StdEncoding.DecodeString(segment.Extra["base64"])
		switch segment.Kind {
		case platformio.SegmentImage:
			return messagecomponent.NewImageComponent("", segment.Text, binary)
		case platformio.SegmentEmoji:
			return messagecomponent.NewEmojiComponent("", segment.Text, binary)
		default:
			voice := messagecomponent.NewVoiceComponent("", segment.Text, binary)
			voice.Source = firstTextValue(segment.URL, segment.Extra["file"])
			return voice
		}
	case platformio.SegmentFile:
		return messagecomponent.FileComponent{
			Name:     firstTextValue(segment.Extra["name"], segment.Text),
			Size:     firstTextValue(segment.Extra["size"], segment.Extra["file_size"]),
			MimeType: firstTextValue(segment.Extra["mime_type"], segment.Extra["mimeType"]),
			FileRef:  firstTextValue(segment.Extra["file_ref"], segment.URL),
		}
	}
	return messagecomponent.DictComponent{Type: string(segment.Kind), Data: map[string]any{"type": string(segment.Kind), "data": segment.Data}}
}

func forwardString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// forwardNodesToMaimData 将平台转发节点编码为 maim_message 的 data 数组
// 每个元素对齐 ForwardComponent.to_seg 的 MessageBase dict：单个消息段 + 发送者 user_info
func forwardNodesToMaimData(ctx context.Context, nodes []platformio.ForwardNode) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		userInfo := maimmsg.UserInfo{
			UserID:       strings.TrimSpace(node.UserID),
			UserNickname: strings.TrimSpace(node.Nickname),
			UserCardname: strings.TrimSpace(node.Cardname),
		}
		for _, segment := range node.Segments {
			maimSeg, err := segmentToMaimSeg(ctx, segment)
			if err != nil {
				return nil, fmt.Errorf("maimbridge: 合并转发节点消息段无效: %w", err)
			}
			out = append(out, map[string]any{
				"message_info":    map[string]any{"user_info": userInfo},
				"message_segment": maimSeg,
			})
		}
	}
	return out, nil
}

// forwardNodesFromMaimData 解析 maim_message 合并转发 data 数组
func forwardNodesFromMaimData(data json.RawMessage) ([]platformio.ForwardNode, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("maimbridge: 合并转发缺少节点数据")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		var wrapped struct {
			Data     []json.RawMessage `json:"data"`
			Messages []json.RawMessage `json:"messages"`
			Content  []json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(data, &wrapped); err != nil {
			return nil, fmt.Errorf("maimbridge: 合并转发节点格式无效: %w", err)
		}
		switch {
		case len(wrapped.Data) > 0:
			raw = wrapped.Data
		case len(wrapped.Messages) > 0:
			raw = wrapped.Messages
		case len(wrapped.Content) > 0:
			raw = wrapped.Content
		default:
			return nil, fmt.Errorf("maimbridge: 合并转发缺少节点数据")
		}
	}
	nodes := make([]platformio.ForwardNode, 0, len(raw))
	for index, item := range raw {
		var record map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(item)))
		decoder.UseNumber()
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("maimbridge: 合并转发第 %d 个节点格式无效: %w", index+1, err)
		}
		info := record
		if nested, ok := record["message_info"].(map[string]any); ok {
			info = nested
		}
		userInfo := info
		if nested, ok := info["user_info"].(map[string]any); ok {
			userInfo = nested
		}
		node := platformio.ForwardNode{
			UserID:    forwardInfoString(userInfo, "user_id", "userid", "qq"),
			Nickname:  forwardInfoString(userInfo, "user_nickname", "nickname", "name"),
			Cardname:  forwardInfoString(userInfo, "user_cardname", "cardname", "card"),
			MessageID: firstTextValue(forwardInfoString(info, "message_id"), forwardInfoString(record, "message_id", "id")),
			Time:      forwardInfoInt64(info["time"]),
		}
		rawSegment, ok := record["message_segment"]
		if !ok {
			rawSegment = record["content"]
		}
		seg, err := maimSegFromAny(rawSegment)
		if err != nil {
			return nil, fmt.Errorf("maimbridge: 合并转发第 %d 个节点消息段无效: %w", index+1, err)
		}
		segments, _ := segmentsFromMaimSeg(seg)
		node.Segments = segments
		if len(node.Segments) == 0 {
			return nil, fmt.Errorf("maimbridge: 合并转发第 %d 个节点缺少消息段", index+1)
		}
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("maimbridge: 合并转发缺少节点数据")
	}
	return nodes, nil
}

func maimSegFromAny(raw any) (maimmsg.Seg, error) {
	if raw == nil {
		return maimmsg.Seg{}, fmt.Errorf("消息段为空")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return maimmsg.Seg{}, err
	}
	var seg maimmsg.Seg
	if err := json.Unmarshal(encoded, &seg); err != nil {
		return maimmsg.Seg{}, err
	}
	if strings.TrimSpace(seg.Type) == "" {
		return maimmsg.Seg{}, fmt.Errorf("消息段缺少 type")
	}
	return seg, nil
}

func forwardInfoString(info map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := info[key]
		if !ok || value == nil {
			continue
		}
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			return text
		}
	}
	return ""
}

func forwardInfoInt64(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed
		}
	case string:
		var parsed int64
		if _, err := fmt.Sscan(strings.TrimSpace(typed), &parsed); err == nil {
			return parsed
		}
	}
	return 0
}
