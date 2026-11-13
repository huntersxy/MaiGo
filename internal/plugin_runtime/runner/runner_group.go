package runner

import "strings"

func runnerGroupOrDefault(group Group) Group {
	if strings.TrimSpace(string(group)) == "" {
		return GroupThirdParty
	}
	return group
}
