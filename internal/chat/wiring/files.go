package chatwiring

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/chat/message_receive"
	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/files"
)

// FileIngestor 文件服务摄入端口，复用 filesvc 的远端附件校验
type FileIngestor struct {
	Service *filesvc.Service
}

// Ingest 实现 messagereceive.FileIngestor
func (i *FileIngestor) Ingest(ctx context.Context, message *messagereceive.SessionMessage) error {
	if i == nil || i.Service == nil {
		return errors.New("文件服务未接线")
	}
	if message == nil || message.RawMessage == nil {
		return nil
	}
	components := message.RawMessage.Components
	replacements := make([][2]string, 0, 1)
	for index, raw := range components {
		component, ok := raw.(messagecomponent.FileComponent)
		if !ok {
			continue
		}
		asset, handled, err := i.Service.IngestFile(ctx, filesvc.RemoteFileInput{
			SessionID: message.SessionID,
			MessageID: message.MessageID,
			GroupID:   groupIDForFile(message),
			UserID:    message.UserID(),
			FileRef:   component.FileRef,
			FileID:    component.FileID,
			Busid:     component.Busid,
			Name:      component.Name,
			MimeType:  component.MimeType,
			Size:      parseFileSize(component.Size),
			Source:    message.FileSource,
		})
		if err != nil {
			return fmt.Errorf("文件摄入失败: %w", err)
		}
		if !handled {
			continue
		}
		oldText := component.ToPlainText()
		component.FileRef = asset.FileRef
		if strings.TrimSpace(asset.Name) != "" {
			component.Name = asset.Name
		}
		if strings.TrimSpace(asset.MimeType) != "" {
			component.MimeType = asset.MimeType
		}
		if asset.Size > 0 {
			component.Size = strconv.FormatInt(asset.Size, 10)
		}
		component.URL = ""
		component.Base64Data = ""
		components[index] = component
		replacements = append(replacements, [2]string{oldText, component.ToPlainText()})
	}
	if len(replacements) == 0 {
		return nil
	}
	text := message.PlainText()
	for _, replacement := range replacements {
		if replacement[0] == "" {
			continue
		}
		text = strings.Replace(text, replacement[0], replacement[1], 1)
	}
	for _, replacement := range replacements {
		if replacement[1] != "" && !strings.Contains(text, replacement[1]) {
			text = strings.TrimSpace(text + " " + replacement[1])
		}
	}
	message.SetPlainText(text)
	return nil
}

func groupIDForFile(message *messagereceive.SessionMessage) string {
	if message == nil || message.MessageInfo.GroupInfo == nil {
		return ""
	}
	return strings.TrimSpace(message.MessageInfo.GroupInfo.GroupID)
}

func parseFileSize(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}
