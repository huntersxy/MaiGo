package snowluma

import "strings"

const (
	MsgActionStyleTargeted = "targeted"
	MsgActionStyleGeneric  = "generic"
	ForwardParamMessage    = "message"
	ForwardParamMessages   = "messages"
)

// ClientProfile 客户端能力画像
type ClientProfile struct {
	ClientType         string
	ExpectedAppName    string
	MsgActionStyle     string
	SplitFileUpload    bool
	ForwardParamKey    string
	RecordOutFormat    string
	TranscodeSilkVoice bool
	SupportsQZone      bool
}

var (
	NapCatProfile = ClientProfile{
		ClientType:         "napcat",
		ExpectedAppName:    "napcat",
		MsgActionStyle:     MsgActionStyleTargeted,
		SplitFileUpload:    false,
		ForwardParamKey:    ForwardParamMessage,
		RecordOutFormat:    "wav",
		TranscodeSilkVoice: false,
		SupportsQZone:      false,
	}
	SnowLumaProfile = ClientProfile{
		ClientType:         "snowluma",
		ExpectedAppName:    "snowluma",
		MsgActionStyle:     MsgActionStyleGeneric,
		SplitFileUpload:    true,
		ForwardParamKey:    ForwardParamMessages,
		RecordOutFormat:    "mp3",
		TranscodeSilkVoice: true,
		SupportsQZone:      true,
	}
	OneBotV11Profile = ClientProfile{
		ClientType:         "onebot_v11",
		ExpectedAppName:    "",
		MsgActionStyle:     MsgActionStyleTargeted,
		SplitFileUpload:    false,
		ForwardParamKey:    ForwardParamMessage,
		RecordOutFormat:    "wav",
		TranscodeSilkVoice: false,
		SupportsQZone:      false,
	}
)

// ProfileByType 按 client_type 查画像
func ProfileByType(clientType string) (ClientProfile, bool) {
	switch strings.ToLower(strings.TrimSpace(clientType)) {
	case "napcat":
		return NapCatProfile, true
	case "snowluma":
		return SnowLumaProfile, true
	case "onebot_v11":
		return OneBotV11Profile, true
	default:
		return ClientProfile{}, false
	}
}

// ProfileState 当前生效画像容器
type ProfileState struct {
	profile ClientProfile
}

func NewProfileState(initial ClientProfile) *ProfileState {
	return &ProfileState{profile: initial}
}

func (s *ProfileState) Profile() ClientProfile {
	if s == nil {
		return OneBotV11Profile
	}
	return s.profile
}

func (s *ProfileState) Update(profile ClientProfile) ClientProfile {
	if s == nil {
		return profile
	}
	s.profile = profile
	return s.profile
}

// DetectClientTypeFromAppName 从 get_version_info.app_name 判定类型
func DetectClientTypeFromAppName(appName string) string {
	normalized := strings.ToLower(strings.TrimSpace(appName))
	if normalized == "" {
		return ""
	}
	if strings.Contains(normalized, "snowluma") {
		return SnowLumaProfile.ClientType
	}
	if strings.Contains(normalized, "napcat") {
		return NapCatProfile.ClientType
	}
	return ""
}

// InitialProfileForConfig 连接前临时画像
func InitialProfileForConfig(clientType string) ClientProfile {
	if profile, ok := ProfileByType(clientType); ok {
		return profile
	}
	// auto：探测前用 onebot_v11 作为中性占位
	return OneBotV11Profile
}
