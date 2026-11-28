package chatwiring

import (
	"fmt"

	"MaiGo/internal/common/data_models/messagecomponent"
	voicesystem "MaiGo/internal/chat/voice_system"

	"gorm.io/gorm"
)

// PersistMediaComponents 保存消息及转发节点中的语音原件，对齐 MessageUtils._persist_media_components 的语音分支
func PersistMediaComponents(tx *gorm.DB, instanceRoot string, components []messagecomponent.Component) error {
	if tx == nil || len(components) == 0 {
		return nil
	}
	for _, component := range components {
		switch item := component.(type) {
		case messagecomponent.VoiceComponent:
			if err := persistVoiceComponent(tx, instanceRoot, &item); err != nil {
				return err
			}
		case *messagecomponent.VoiceComponent:
			if item != nil {
				if err := persistVoiceComponent(tx, instanceRoot, item); err != nil {
					return err
				}
			}
		case messagecomponent.ForwardNodeComponent:
			for _, node := range item.ForwardComponents {
				if err := PersistMediaComponents(tx, instanceRoot, node.Content); err != nil {
					return err
				}
			}
		case *messagecomponent.ForwardNodeComponent:
			if item != nil {
				for _, node := range item.ForwardComponents {
					if err := PersistMediaComponents(tx, instanceRoot, node.Content); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func persistVoiceComponent(tx *gorm.DB, instanceRoot string, item *messagecomponent.VoiceComponent) error {
	if item == nil || len(item.Binary) == 0 {
		return nil
	}
	digest, err := voicesystem.StoreVoiceTx(tx, instanceRoot, item.Binary)
	if err != nil {
		return fmt.Errorf("保存语音原件失败: %w", err)
	}
	item.Hash = digest
	return nil
}