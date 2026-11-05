package my_sites

import (
	"errors"
	"regexp"
	"strings"
)

// 分组的模型厂商并不等于账号协议；能力响应与创建校验共用这一份定义。
// 这里只列手动可选、并有专门建账号参数的协议；Sub2API 上游分组自带的其他平台另由 upstreamGroupPlatform 接受。
type GroupTypeOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type accountProtocol struct {
	GroupTypeOption
	concurrency int
	poolMode    bool
	passthrough string
	tier        string
}

var accountProtocols = []accountProtocol{
	{GroupTypeOption{"openai", "OpenAI"}, 1000, true, "openai_passthrough", ""},
	{GroupTypeOption{"anthropic", "Anthropic"}, 1000, true, "anthropic_passthrough", ""},
	{GroupTypeOption{"gemini", "Gemini"}, 1000, true, "", "aistudio_free"},
	{GroupTypeOption{"antigravity", "Antigravity"}, 10, false, "", ""},
	// Sub2API 的 Grok API Key 账号同样走 base_url/api_key，并支持池模式。
	{GroupTypeOption{"grok", "Grok"}, 1000, true, "", ""},
}

// accountPlatformPattern 是 Sub2API 平台标识的格式（如 grok、opencode_go）；逗号分隔的厂商列表不匹配。
var accountPlatformPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// upstreamGroupPlatform 返回 Sub2API 上游分组自带的平台，任何合法平台标识都直接作为账号协议；
// New API 上游的分组类型是模型厂商列表，不是平台，始终返回空。
func upstreamGroupPlatform(cached string, newAPIUpstream bool) string {
	cached = normalizeProtocolValue(cached)
	if newAPIUpstream || !accountPlatformPattern.MatchString(cached) {
		return ""
	}
	return cached
}

func findAccountProtocol(value string) (accountProtocol, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, protocol := range accountProtocols {
		if protocol.ID == value {
			return protocol, true
		}
	}
	return accountProtocol{}, false
}

func accountGroupTypeOptions() []GroupTypeOption {
	options := make([]GroupTypeOption, 0, len(accountProtocols))
	for _, protocol := range accountProtocols {
		options = append(options, protocol.GroupTypeOption)
	}
	return options
}

func normalizeProtocolValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// validateRequestedGroupType 校验显式选择：必须是已发布协议，或正是 Sub2API 上游分组自带的平台
// （前端会把自动识别出的平台原样提交）；空值表示未选择。
func validateRequestedGroupType(requested, upstreamPlatform string) error {
	requested = normalizeProtocolValue(requested)
	if requested == "" || (upstreamPlatform != "" && requested == upstreamPlatform) {
		return nil
	}
	if _, ok := findAccountProtocol(requested); !ok {
		return requestError(ErrorGroupTypeInvalid)
	}
	return nil
}

func resolveAccountGroupType(cached, requested string, newAPIUpstream bool) (string, error) {
	platform := upstreamGroupPlatform(cached, newAPIUpstream)
	requested = normalizeProtocolValue(requested)
	if err := validateRequestedGroupType(requested, platform); err != nil {
		return "", err
	}
	// Sub2API 上游分组自带平台时由它决定协议（含 grok 等）；New API 的厂商列表不等于协议，只认显式选择。
	if platform != "" {
		return platform, nil
	}
	if requested != "" {
		return requested, nil
	}
	return "", requestError(ErrorGroupTypeRequired)
}

// protocolFromOwnGroupPlatforms 取所选自有分组的唯一平台作为账号协议；
// 混用不同平台时拒绝，平台不是已发布协议时要求用户显式选择。
func protocolFromOwnGroupPlatforms(platforms []string) (string, error) {
	protocol := ""
	for _, platform := range platforms {
		platform = normalizeProtocolValue(platform)
		if protocol == "" {
			protocol = platform
			continue
		}
		if platform != protocol {
			return "", requestError(ErrorOwnGroupPlatformMismatch)
		}
	}
	if _, ok := findAccountProtocol(protocol); !ok {
		return "", requestError(ErrorGroupTypeRequired)
	}
	return protocol, nil
}

// resolveManagedAccountGroupType 决定 Sub2API 管理端新账号的协议：
// 先按上游缓存/显式选择解析，New API 上游未选择时跟随所选自有分组的唯一平台；
// 最终协议必须与每个所选自有分组的平台一致（Sub2API 账号只能绑定同平台分组）。
func resolveManagedAccountGroupType(cached, requested string, newAPIUpstream bool, ownGroupPlatforms []string) (string, error) {
	groupType, err := resolveAccountGroupType(cached, requested, newAPIUpstream)
	if err != nil {
		if !errors.Is(err, requestError(ErrorGroupTypeRequired)) {
			return "", err
		}
		if groupType, err = protocolFromOwnGroupPlatforms(ownGroupPlatforms); err != nil {
			return "", err
		}
	}
	if len(ownGroupPlatforms) == 0 {
		return "", requestError(ErrorRequest)
	}
	for _, platform := range ownGroupPlatforms {
		if normalizeProtocolValue(platform) != groupType {
			return "", requestError(ErrorOwnGroupPlatformMismatch)
		}
	}
	return groupType, nil
}
