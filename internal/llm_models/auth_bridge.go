package llm

// 鉴权与传输基础能力的转发。
//
// 实现位于 internal/llm_models/contract（对齐 Python
// src/llm_models/model_client/base_client.py 里各客户端共用的鉴权构造）：
// 该能力被「客户端实现」与「路由/嵌入服务」两侧同时需要，放在任一侧都会
// 造成循环引用。这里保留父包的内部小写名，调用点无需改动。

import (
	"maibot-go/internal/llm_models/contract"
)

const (
	// authTypeNone 无需鉴权的 client_type 取值
	authTypeNone = contract.AuthTypeNone
	// authorizationHeader 标准鉴权头名
	authorizationHeader = contract.AuthorizationHeader
	// authTypeBearer Bearer 鉴权
	authTypeBearer = contract.AuthTypeBearer
	// authTypeHeader 自定义头鉴权
	authTypeHeader = contract.AuthTypeHeader
	// authTypeQuery 查询参数鉴权
	authTypeQuery = contract.AuthTypeQuery
)

var (
	// buildOpenAICompatAuth 构造 OpenAI 兼容协议族的鉴权参数
	buildOpenAICompatAuth = contract.BuildOpenAICompatAuth
	// newProviderTransport 把鉴权注入到 HTTP 传输层
	newProviderTransport = contract.NewProviderTransport
	// normalizeOpenAIBaseURL 规范化 OpenAI 系 BaseURL
	normalizeOpenAIBaseURL = contract.NormalizeOpenAIBaseURL
	// canonicalizeAuthorizationHeader 规范化鉴权头名
	canonicalizeAuthorizationHeader = contract.CanonicalizeAuthorizationHeader
	// buildAuthHeaderValue 拼接鉴权头的值
	buildAuthHeaderValue = contract.BuildAuthHeaderValue
)
