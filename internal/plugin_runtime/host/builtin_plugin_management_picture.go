package host

import (
	"fmt"
	"html"
	"strings"
)

type pluginCatalogGroup struct {
	Name     string
	Commands []*ComponentEntry
}

type pluginCatalogDetail struct {
	Label string
	Value string
}

func pluginCommandDetails(command *ComponentEntry) []pluginCatalogDetail {
	if command == nil {
		return nil
	}
	details := []pluginCatalogDetail{{
		Label: "说明",
		Value: firstNonEmptyText(strings.TrimSpace(command.Description), "未提供说明"),
	}}
	if len(command.Aliases) > 0 {
		details = append(details, pluginCatalogDetail{Label: "别名", Value: strings.Join(command.Aliases, "、")})
	}
	if permission := strings.TrimSpace(command.Permission); permission != "" {
		details = append(details, pluginCatalogDetail{Label: "权限", Value: permission})
	}
	return details
}

func estimatePluginCommandHeight(command *ComponentEntry, textCapacity int) int {
	if textCapacity <= 0 {
		textCapacity = 82
	}
	height := 72
	for _, detail := range pluginCommandDetails(command) {
		lines := 0
		for _, line := range strings.Split(detail.Value, "\n") {
			width := 0
			for _, char := range line {
				if char > 127 {
					width += 2
				} else {
					width++
				}
			}
			lineCount := 0
			if width > 0 {
				lineCount = (width + textCapacity - 1) / textCapacity
			}
			if lineCount < 1 {
				lineCount = 1
			}
			lines += lineCount
		}
		height += lines*26 + 5
	}
	return height
}

func balancePluginCatalogColumns(groups []pluginCatalogGroup, textCapacity int) [][]pluginCatalogGroup {
	total := 0
	for _, group := range groups {
		total += 90
		for _, command := range group.Commands {
			total += estimatePluginCommandHeight(command, textCapacity)
		}
	}
	target := float64(total) / 2
	columns := make([][]pluginCatalogGroup, 2)
	column := 0
	height := 0
	for _, group := range groups {
		newGroup := true
		for _, command := range group.Commands {
			commandHeight := estimatePluginCommandHeight(command, textCapacity)
			if column == 0 && len(columns[0]) > 0 && float64(height)+float64(commandHeight)/2 >= target {
				column = 1
				newGroup = true
			}
			if newGroup {
				columns[column] = append(columns[column], pluginCatalogGroup{Name: group.Name})
				height += 90
				newGroup = false
			}
			last := len(columns[column]) - 1
			columns[column][last].Commands = append(columns[column][last].Commands, command)
			height += commandHeight
		}
	}
	out := make([][]pluginCatalogGroup, 0, 2)
	for _, columnGroups := range columns {
		if len(columnGroups) > 0 {
			out = append(out, columnGroups)
		}
	}
	return out
}

func renderPluginCatalogGroup(group pluginCatalogGroup) string {
	var builder strings.Builder
	builder.WriteString("<section><h2>")
	builder.WriteString(html.EscapeString(group.Name))
	builder.WriteString("</h2>")
	for _, command := range group.Commands {
		badge := ""
		if !command.Enabled {
			badge = "<span class='badge disabled'>已禁用</span>"
		}
		builder.WriteString("<article><h3>")
		builder.WriteString(html.EscapeString(command.Name))
		builder.WriteString(badge)
		builder.WriteString("</h3><dl>")
		for _, detail := range pluginCommandDetails(command) {
			builder.WriteString("<dt>")
			builder.WriteString(html.EscapeString(detail.Label))
			builder.WriteString("</dt><dd><code>")
			builder.WriteString(html.EscapeString(detail.Value))
			builder.WriteString("</code></dd>")
		}
		builder.WriteString("</dl></article>")
	}
	builder.WriteString("</section>")
	return builder.String()
}

// buildManagementCommandPages 生成指令列表 HTML，对齐 command_catalog.build_command_pages
func buildManagementCommandPages(groups []pluginManagementCommandGroup, webuiStyle int) ([]string, error) {
	styleName, ok := pluginCommandCatalogStyleNames[webuiStyle]
	if !ok {
		return nil, fmt.Errorf("无法读取有效的 WebUI 界面风格: %d", webuiStyle)
	}
	catalogGroups := make([]pluginCatalogGroup, 0, len(groups))
	for _, group := range groups {
		commands := make([]*ComponentEntry, 0, len(group.Commands))
		commands = append(commands, group.Commands...)
		catalogGroups = append(catalogGroups, pluginCatalogGroup{Name: group.DisplayName, Commands: commands})
	}

	estimatedHeight := 300
	for _, group := range catalogGroups {
		estimatedHeight += 90
		for _, command := range group.Commands {
			estimatedHeight += estimatePluginCommandHeight(command, 82)
		}
	}
	columns := 1
	if estimatedHeight > 1800 {
		columns = 2
	}
	textCapacity := 82
	if columns == 2 {
		textCapacity = 32
	}
	contentHeightLimit := (4800 - 300) * columns

	pages := make([][]pluginCatalogGroup, 0)
	current := make([]pluginCatalogGroup, 0)
	height := 0
	for _, group := range catalogGroups {
		for index, command := range group.Commands {
			newGroup := index == 0 || len(current) == 0
			commandHeight := estimatePluginCommandHeight(command, textCapacity)
			if newGroup {
				commandHeight += 90
			}
			if len(current) > 0 && height+commandHeight > contentHeightLimit {
				pages = append(pages, current)
				current = make([]pluginCatalogGroup, 0)
				height = 0
				commandHeight = estimatePluginCommandHeight(command, textCapacity) + 90
			}
			if newGroup || len(current) == 0 {
				current = append(current, pluginCatalogGroup{Name: group.Name})
			}
			last := len(current) - 1
			current[last].Commands = append(current[last].Commands, command)
			height += commandHeight
		}
	}
	if len(current) > 0 {
		pages = append(pages, current)
	}

	totalCommands := 0
	for _, group := range catalogGroups {
		totalCommands += len(group.Commands)
	}
	documents := make([]string, 0, len(pages))
	for number, page := range pages {
		pageColumns := [][]pluginCatalogGroup{page}
		if columns == 2 {
			pageColumns = balancePluginCatalogColumns(page, textCapacity)
		}
		var content strings.Builder
		for _, columnGroups := range pageColumns {
			content.WriteString("<div class='column'>")
			for _, group := range columnGroups {
				content.WriteString(renderPluginCatalogGroup(group))
			}
			content.WriteString("</div>")
		}
		pageLabel := ""
		if len(pages) > 1 {
			pageLabel = fmt.Sprintf(" · 第 %d / %d 页", number+1, len(pages))
		}
		layout := "catalog"
		if columns == 2 {
			layout = "catalog two-columns"
		}
		documents = append(documents, fmt.Sprintf(
			"<!doctype html><html lang=\"zh-CN\" data-dashboard-style=\"%s\"><meta charset=\"utf-8\">"+
				"<style>%s%s</style>"+
				"<body><main><header><div class='brand'>MAIBOT · PLUGINS</div>"+
				"<h1>插件指令列表</h1><div class='muted'>%d 个插件 · %d 条指令%s</div></header>"+
				"<div class='%s'>%s</div><footer>包含已禁用指令</footer></main></body></html>",
			styleName,
			pluginCommandCatalogStyle,
			pluginCommandCatalogStyleVariant(webuiStyle),
			len(catalogGroups),
			totalCommands,
			pageLabel,
			layout,
			content.String(),
		))
	}
	return documents, nil
}
