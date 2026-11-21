package bootstrap

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"MaiGo/internal/common/data_models/messagecomponent"
	"MaiGo/internal/common/database"
	"MaiGo/internal/person_info/identity"
	"MaiGo/internal/platform_io/drivers/onebot"
)

func normalizeInboundIdentity(message *onebot.ConversationMessage, state identity.State) *onebot.ConversationMessage {
	platform := strings.TrimSpace(message.Platform)
	if platform == "" {
		platform = "qq"
	}
	if len(state.Aliases[platform]) == 0 {
		return message
	}
	result := *message
	lookup := func(physical string, numeric int64, role string) (string, int64, bool) {
		if physical == "" && numeric > 0 {
			physical = strconv.FormatInt(numeric, 10)
		}
		canonical := state.ResolveUserID(platform, physical)
		if canonical == physical {
			return physical, numeric, false
		}
		value, err := strconv.ParseInt(canonical, 10, 64)
		if err != nil || value <= 0 {
			value = onebot.SyntheticIdentityID(role, platform, message.AccountID, message.Scope, canonical)
		}
		return canonical, value, true
	}
	if canonical, numeric, changed := lookup(message.ExternalUserID, message.UserID, "user"); changed {
		original := message.ExternalUserID
		if original == "" {
			original = strconv.FormatInt(message.UserID, 10)
		}
		result.UserID, result.ExternalUserID = numeric, canonical
		if message.ConversationKind == memory.ConversationKindPrivate {
			result.TargetID = numeric
			if value, err := strconv.ParseInt(canonical, 10, 64); err != nil || value <= 0 {
				result.TargetID = onebot.SyntheticIdentityID("target", platform, message.AccountID, message.Scope, canonical)
			}
			result.ExternalTargetID = canonical
			result.SessionID = ""
			result.ArrivalSeq = 0
		}
		result.AdditionalConfig = maps.Clone(message.AdditionalConfig)
		if result.AdditionalConfig == nil {
			result.AdditionalConfig = map[string]any{}
		}
		result.AdditionalConfig["identity_original_user_id"] = original
		result.AdditionalConfig["identity_canonical_user_id"] = canonical
	}
	result.MessageParts = slices.Clone(message.MessageParts)
	result.AtList = slices.Clone(message.AtList)
	result.ExternalAtList = slices.Clone(message.ExternalAtList)
	result.AtNames = maps.Clone(message.AtNames)
	for index, part := range result.MessageParts {
		if part.Forward != nil {
			forward := identity.NormalizeComponents([]messagecomponent.Component{*part.Forward}, state, platform)[0].(messagecomponent.ForwardNodeComponent)
			result.MessageParts[index].Forward = &forward
		}
		if part.Kind != "at" {
			continue
		}
		if canonical, numeric, changed := lookup(part.ExternalAtUserID, part.AtUserID, "at"); changed {
			result.MessageParts[index].AtUserID = numeric
			result.MessageParts[index].ExternalAtUserID = canonical
		}
	}
	for index, numeric := range result.AtList {
		external := ""
		if index < len(result.ExternalAtList) {
			external = result.ExternalAtList[index]
		}
		if canonical, value, changed := lookup(external, numeric, "at"); changed {
			result.AtList[index] = value
			if index >= len(result.ExternalAtList) {
				result.ExternalAtList = append(result.ExternalAtList, make([]string, len(result.AtList)-len(result.ExternalAtList))...)
			}
			result.ExternalAtList[index] = canonical
			if name := message.AtNames[numeric]; name != "" {
				result.AtNames[value] = name
			}
		}
	}
	if message.Reply != nil {
		if canonical, numeric, changed := lookup(message.Reply.ExternalSenderID, message.Reply.SenderID, "reply-sender"); changed {
			reply := *message.Reply
			reply.ExternalSenderID, reply.SenderID = canonical, numeric
			result.Reply = &reply
		}
	}
	return &result
}
