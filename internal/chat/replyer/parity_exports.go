package replyer

// 差分对拍用的导出包装：Python 侧 protect_kaomoji / recover_kaomoji /
// get_western_ratio / strip_chinese_parenthetical_content 均为公开函数，
// Go 实现是包内小写，这里只做转发，不改变语义。

// StripChineseParentheticalContent 对齐 utils.py 的同名清理逻辑
func StripChineseParentheticalContent(text string) string {
	return stripChineseParentheticalContent(text)
}

// WesternRatio 对齐 utils.py 的 get_western_ratio
func WesternRatio(paragraph string) float64 {
	return westernRatio(paragraph)
}

// KaomojiPair 占位符与颜文字原文的对应关系
type KaomojiPair struct {
	Placeholder string
	Text        string
}

// ProtectKaomoji 对齐 utils.py 的 protect_kaomoji
func ProtectKaomoji(text string) (string, []KaomojiPair) {
	protected, mapping := protectKaomoji(text, true)
	pairs := make([]KaomojiPair, 0, len(mapping))
	for _, entry := range mapping {
		pairs = append(pairs, KaomojiPair{Placeholder: entry.placeholder, Text: entry.text})
	}
	return protected, pairs
}

// RecoverKaomoji 对齐 utils.py 的 recover_kaomoji
func RecoverKaomoji(texts []string, pairs []KaomojiPair) []string {
	mapping := make(kaomojiMapping, 0, len(pairs))
	for _, pair := range pairs {
		mapping = append(mapping, kaomojiEntry{placeholder: pair.Placeholder, text: pair.Text})
	}
	return recoverKaomoji(texts, mapping, true)
}
