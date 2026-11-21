package identity

import (
	"slices"

	"MaiGo/internal/common/data_models/messagecomponent"
)

func NormalizeComponents(components []messagecomponent.Component, state State, platform string) []messagecomponent.Component {
	result := slices.Clone(components)
	rewrite := func(value *string) *string {
		if value == nil || *value == "" {
			return value
		}
		canonical := state.ResolveUserID(platform, *value)
		return &canonical
	}
	for index, component := range result {
		switch item := component.(type) {
		case messagecomponent.AtComponent:
			item.TargetUserID = state.ResolveUserID(platform, item.TargetUserID)
			result[index] = item
		case messagecomponent.ReplyComponent:
			item.SenderID = rewrite(item.SenderID)
			result[index] = item
		case messagecomponent.ForwardNodeComponent:
			item.ForwardComponents = slices.Clone(item.ForwardComponents)
			for nodeIndex := range item.ForwardComponents {
				node := &item.ForwardComponents[nodeIndex]
				node.UserID = rewrite(node.UserID)
				node.Content = NormalizeComponents(node.Content, state, platform)
			}
			result[index] = item
		}
	}
	return result
}
