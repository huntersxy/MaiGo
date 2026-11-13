package messagecomponent

// 组件深拷贝，对齐 Python copy.deepcopy 语义
//
// 组件多为值类型，但内部仍持有 map/slice/指针字段；Clone 只复制切片头会让
// Hook 改写穿透回原消息，因此这里逐类型复制可变成员。

// CloneComponents 深拷贝组件切片
func CloneComponents(components []Component) []Component {
	if components == nil {
		return nil
	}
	out := make([]Component, len(components))
	for index, component := range components {
		out[index] = CloneComponent(component)
	}
	return out
}

// CloneComponent 深拷贝单个组件
func CloneComponent(component Component) Component {
	switch typed := component.(type) {
	case ImageComponent:
		return ImageComponent{ByteComponent: cloneByteComponent(typed.ByteComponent)}
	case *ImageComponent:
		if typed == nil {
			return typed
		}
		cloned := ImageComponent{ByteComponent: cloneByteComponent(typed.ByteComponent)}
		return &cloned
	case EmojiComponent:
		return EmojiComponent{ByteComponent: cloneByteComponent(typed.ByteComponent)}
	case *EmojiComponent:
		if typed == nil {
			return typed
		}
		cloned := EmojiComponent{ByteComponent: cloneByteComponent(typed.ByteComponent)}
		return &cloned
	case VoiceComponent:
		cloned := typed
		cloned.ByteComponent = cloneByteComponent(typed.ByteComponent)
		return cloned
	case *VoiceComponent:
		if typed == nil {
			return typed
		}
		cloned := *typed
		cloned.ByteComponent = cloneByteComponent(typed.ByteComponent)
		return &cloned
	case AtComponent:
		return AtComponent{
			TargetUserID: typed.TargetUserID,
			Nickname:     cloneStringPtr(typed.Nickname),
			Cardname:     cloneStringPtr(typed.Cardname),
			UsesBotNick:  typed.UsesBotNick,
		}
	case *AtComponent:
		if typed == nil {
			return typed
		}
		cloned := AtComponent{
			TargetUserID: typed.TargetUserID,
			Nickname:     cloneStringPtr(typed.Nickname),
			Cardname:     cloneStringPtr(typed.Cardname),
			UsesBotNick:  typed.UsesBotNick,
		}
		return &cloned
	case ReplyComponent:
		return ReplyComponent{
			TargetMessageID: typed.TargetMessageID,
			TargetContent:   cloneStringPtr(typed.TargetContent),
			SenderID:        cloneStringPtr(typed.SenderID),
			SenderNickname:  cloneStringPtr(typed.SenderNickname),
			SenderCardname:  cloneStringPtr(typed.SenderCardname),
		}
	case *ReplyComponent:
		if typed == nil {
			return typed
		}
		cloned := ReplyComponent{
			TargetMessageID: typed.TargetMessageID,
			TargetContent:   cloneStringPtr(typed.TargetContent),
			SenderID:        cloneStringPtr(typed.SenderID),
			SenderNickname:  cloneStringPtr(typed.SenderNickname),
			SenderCardname:  cloneStringPtr(typed.SenderCardname),
		}
		return &cloned
	case ForwardNodeComponent:
		return cloneForwardNodeComponent(typed)
	case *ForwardNodeComponent:
		if typed == nil {
			return typed
		}
		cloned := cloneForwardNodeComponent(*typed)
		return &cloned
	case DictComponent:
		return DictComponent{Type: typed.Type, Data: cloneAnyMap(typed.Data)}
	case *DictComponent:
		if typed == nil {
			return typed
		}
		cloned := DictComponent{Type: typed.Type, Data: cloneAnyMap(typed.Data)}
		return &cloned
	default:
		// TextComponent/FileComponent 等纯值类型直接返回
		return component
	}
}

func cloneByteComponent(source ByteComponent) ByteComponent {
	cloned := source
	if source.Binary != nil {
		cloned.Binary = append([]byte(nil), source.Binary...)
	}
	return cloned
}

func cloneStringPtr(source *string) *string {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneForwardComponent(source ForwardComponent) ForwardComponent {
	return ForwardComponent{
		UserNickname: source.UserNickname,
		MessageID:    source.MessageID,
		Content:      CloneComponents(source.Content),
		UserID:       cloneStringPtr(source.UserID),
		UserCardname: cloneStringPtr(source.UserCardname),
	}
}

func cloneForwardNodeComponent(source ForwardNodeComponent) ForwardNodeComponent {
	if source.ForwardComponents == nil {
		return ForwardNodeComponent{}
	}
	nodes := make([]ForwardComponent, len(source.ForwardComponents))
	for index, node := range source.ForwardComponents {
		nodes[index] = cloneForwardComponent(node)
	}
	return ForwardNodeComponent{ForwardComponents: nodes}
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = cloneAnyValue(value)
	}
	return out
}

func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneAnyValue(item)
		}
		return out
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}
