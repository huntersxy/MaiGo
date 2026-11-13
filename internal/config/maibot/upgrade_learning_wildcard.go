package maibot

import "strings"

// upgradeMergeWildcardLearningRulesIntoDefaults 对齐 8.14.60
func upgradeMergeWildcardLearningRulesIntoDefaults(bot *BotConfigFile) []string {
	var reasons []string
	if mergeWildcardLearningRules(&bot.Expression.LearningList) {
		reasons = append(reasons, "expression.learning_list")
	}
	if mergeWildcardLearningRules(&bot.Jargon.LearningList) {
		reasons = append(reasons, "jargon.learning_list")
	}
	return reasons
}

func mergeWildcardLearningRules(list *[]LearningItem) bool {
	if list == nil || len(*list) == 0 {
		return false
	}
	items := make([]LearningItem, len(*list))
	copy(items, *list)

	targets := make([]learningTarget, len(items))
	wasWildcard := make([]bool, len(items))
	wasDead := make([]bool, len(items))
	for i := range items {
		rawPlatform := strings.TrimSpace(items[i].Platform)
		rawItemID := strings.TrimSpace(items[i].ItemId)
		platform := rawPlatform
		itemID := rawItemID
		if platform == "*" {
			platform = ""
		}
		if itemID == "*" {
			itemID = ""
		}
		ruleType := strings.TrimSpace(items[i].Type)
		if ruleType == "" {
			ruleType = "group"
		}
		targets[i] = learningTarget{platform, itemID, ruleType}
		wasWildcard[i] = bool(rawPlatform != "" && rawItemID != "") &&
			(rawPlatform == "*" || rawItemID == "*")
		wasDead[i] = (rawPlatform == "*") != (rawItemID == "*") && platform == "" && itemID == ""
	}

	winners := map[learningTarget]int{}
	for i, target := range targets {
		if target.platform == "" && target.itemID == "" {
			continue
		}
		current, ok := winners[target]
		if !ok || (wasWildcard[i] && !wasWildcard[current]) {
			winners[target] = i
		}
	}

	var globalIndexes []int
	for i, target := range targets {
		if target.platform == "" && target.itemID == "" && !wasDead[i] {
			globalIndexes = append(globalIndexes, i)
		}
	}

	wildcardByType := map[string]LearningItem{}
	var firstDefault *LearningItem
	for _, index := range globalIndexes {
		if wasWildcard[index] {
			rt := targets[index].ruleType
			if _, ok := wildcardByType[rt]; !ok {
				wildcardByType[rt] = items[index]
			}
		} else if firstDefault == nil {
			copyItem := items[index]
			firstDefault = &copyItem
		}
	}

	var globalItems []LearningItem
	if len(wildcardByType) > 0 {
		for _, ruleType := range []string{"group", "private"} {
			src, ok := wildcardByType[ruleType]
			if !ok && firstDefault != nil {
				src = *firstDefault
			}
			globalItems = append(globalItems, learningDefaultItem(src, ruleType))
		}
	} else if firstDefault != nil {
		rt := strings.TrimSpace(firstDefault.Type)
		if rt == "" {
			rt = "group"
		}
		globalItems = append(globalItems, learningDefaultItem(*firstDefault, rt))
	}

	merged := make([]LearningItem, 0, len(items))
	globalInserted := false
	for i, target := range targets {
		if target.platform == "" && target.itemID == "" {
			if !globalInserted && containsInt(globalIndexes, i) {
				merged = append(merged, globalItems...)
				globalInserted = true
			}
			continue
		}
		if winners[target] != i {
			continue
		}
		item := items[i]
		item.Platform = target.platform
		item.ItemId = target.itemID
		merged = append(merged, item)
	}

	if learningListEqual(*list, merged) {
		return false
	}
	*list = merged
	return true
}

type learningTarget struct {
	platform, itemID, ruleType string
}

func learningDefaultItem(source LearningItem, ruleType string) LearningItem {
	item := source
	item.Platform = ""
	item.ItemId = ""
	item.Type = ruleType
	return item
}

func learningListEqual(a, b []LearningItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
