package scripts

// LPMM GraphML 图转换，对齐 convert_graph
// 差异说明：Go 版不执行 Python pickle 反序列化，graph_structure.pkl 会显式报错
// 或在使用 graphml 时被跳过；GraphML 使用 encoding/xml 读取

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
)

// lpmmGraph 转换期间的中间图
type lpmmGraph struct {
	nodes []lpmmGraphNode
	edges []lpmmGraphEdge
}

// lpmmGraphNode 图节点与属性
type lpmmGraphNode struct {
	id    string
	attrs map[string]any
}

// lpmmGraphEdge 图边与权重
type lpmmGraphEdge struct {
	source string
	target string
	weight float64
}

// graphMLDocument GraphML 根节点
type graphMLDocument struct {
	XMLName xml.Name     `xml:"graphml"`
	Keys    []graphMLKey `xml:"key"`
	Graph   graphMLGraph `xml:"graph"`
}

// graphMLKey GraphML key 定义
type graphMLKey struct {
	ID       string `xml:"id,attr"`
	For      string `xml:"for,attr"`
	AttrName string `xml:"attr.name,attr"`
	AttrType string `xml:"attr.type,attr"`
	Default  string `xml:"default"`
}

// graphMLGraph GraphML graph 元素
type graphMLGraph struct {
	Nodes []graphMLNode `xml:"node"`
	Edges []graphMLEdge `xml:"edge"`
}

// graphMLNode GraphML node 元素
type graphMLNode struct {
	ID   string        `xml:"id,attr"`
	Data []graphMLData `xml:"data"`
}

// graphMLEdge GraphML edge 元素
type graphMLEdge struct {
	Source string        `xml:"source,attr"`
	Target string        `xml:"target,attr"`
	Data   []graphMLData `xml:"data"`
}

// graphMLData GraphML data 元素
type graphMLData struct {
	Key  string `xml:"key,attr"`
	Text string `xml:",chardata"`
}

// convertGraph 对齐 convert_graph
func (c *LPMMParquetConverter) convertGraph() error {
	graphFiles := []string{
		filepath.Join(c.inputDir, "rag-graph.graphml"),
		filepath.Join(c.inputDir, "graph.graphml"),
		filepath.Join(c.inputDir, "graph_structure.pkl"),
	}
	existing := make([]string, 0, len(graphFiles))
	for _, path := range graphFiles {
		if !pathExists(path) {
			continue
		}
		if strings.EqualFold(filepath.Ext(path), ".pkl") && !c.allowUnsafePickle {
			continue
		}
		existing = append(existing, path)
	}
	if len(existing) == 0 {
		c.logger.Warning("未找到图文件。关系 Parquet 生成的图仍会保留。")
		return nil
	}

	var graph *lpmmGraph
	var picklePath string
	for _, path := range existing {
		c.logger.Infof("发现图文件: %s", path)
		switch strings.ToLower(filepath.Ext(path)) {
		case ".graphml":
			loaded, err := loadLPMMGraphML(path)
			if err != nil {
				return err
			}
			graph = loaded
		case ".pkl":
			if !c.allowUnsafePickle {
				c.logger.Warningf("跳过不安全的 pickle 图文件: %s。 如确认来源可信，可添加 --allow-unsafe-pickle。", path)
				continue
			}
			picklePath = path
		}
		if graph != nil {
			break
		}
	}
	if graph == nil {
		if picklePath != "" {
			return fmt.Errorf("Go 版不执行 Python pickle 反序列化，无法转换图文件: %s；请提供 GraphML", picklePath)
		}
		c.logger.Warning("未找到有效的图文件。跳过图转换。")
		return nil
	}

	c.logger.Infof("已加载图，包含 %d 个节点和 %d 条边。", len(graph.nodes), len(graph.edges))
	nodes := make([]string, 0, len(graph.nodes))
	nodeAttrs := map[string]map[string]any{}
	for _, node := range graph.nodes {
		mapped := c.mapNodeID(node.id)
		nodes = append(nodes, mapped)
		if len(node.attrs) > 0 {
			nodeAttrs[mapped] = node.attrs
		}
	}
	c.graphStore.AddNodes(nodes, nodeAttrs)

	edges := make([]storage.Edge, 0, len(graph.edges))
	weights := make([]float64, 0, len(graph.edges))
	for _, edge := range graph.edges {
		edges = append(edges, storage.Edge{Source: c.mapNodeID(edge.source), Target: c.mapNodeID(edge.target)})
		weights = append(weights, edge.weight)
	}
	if len(edges) > 0 {
		if _, err := c.graphStore.AddEdges(edges, weights, nil); err != nil {
			return err
		}
	}
	c.logger.Info("图转换完成。")
	return nil
}

// loadLPMMGraphML 使用 encoding/xml 读取 GraphML
func loadLPMMGraphML(path string) (*lpmmGraph, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	document := graphMLDocument{}
	if err := xml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	keys := make(map[string]graphMLKey, len(document.Keys))
	nodeDefaults := map[string]any{}
	edgeWeightKeys := map[string]struct{}{}
	edgeWeightDefault := 0.0
	hasEdgeWeightDefault := false
	for _, key := range document.Keys {
		keys[key.ID] = key
		name := graphMLKeyName(key)
		switch strings.ToLower(strings.TrimSpace(key.For)) {
		case "node", "all":
			if key.Default != "" {
				value, err := graphMLValue(key, key.Default)
				if err != nil {
					return nil, err
				}
				nodeDefaults[name] = value
			}
		}
		if name == "weight" && (strings.EqualFold(key.For, "edge") || strings.EqualFold(key.For, "all")) {
			edgeWeightKeys[key.ID] = struct{}{}
			if key.Default != "" {
				value, err := strconv.ParseFloat(strings.TrimSpace(key.Default), 64)
				if err != nil {
					return nil, fmt.Errorf("GraphML 边权重默认值非法: %w", err)
				}
				edgeWeightDefault = value
				hasEdgeWeightDefault = true
			}
		}
	}

	graph := &lpmmGraph{}
	for _, node := range document.Graph.Nodes {
		if strings.TrimSpace(node.ID) == "" {
			return nil, fmt.Errorf("GraphML 节点缺少 id")
		}
		attrs := map[string]any{}
		for _, data := range node.Data {
			key, ok := keys[data.Key]
			if !ok {
				continue
			}
			value, err := graphMLValue(key, data.Text)
			if err != nil {
				return nil, err
			}
			attrs[graphMLKeyName(key)] = value
		}
		for name, value := range nodeDefaults {
			if _, ok := attrs[name]; !ok {
				attrs[name] = value
			}
		}
		graph.nodes = append(graph.nodes, lpmmGraphNode{id: node.ID, attrs: attrs})
	}

	for _, edge := range document.Graph.Edges {
		if strings.TrimSpace(edge.Source) == "" || strings.TrimSpace(edge.Target) == "" {
			return nil, fmt.Errorf("GraphML 边缺少 source/target")
		}
		weight := 1.0
		if hasEdgeWeightDefault {
			weight = edgeWeightDefault
		}
		for _, data := range edge.Data {
			if _, ok := edgeWeightKeys[data.Key]; !ok {
				continue
			}
			value, err := strconv.ParseFloat(strings.TrimSpace(data.Text), 64)
			if err != nil {
				return nil, fmt.Errorf("GraphML 边权重非法: %w", err)
			}
			weight = value
		}
		graph.edges = append(graph.edges, lpmmGraphEdge{source: edge.Source, target: edge.Target, weight: weight})
	}
	return graph, nil
}

// graphMLKeyName 返回 networkx 使用的属性名
func graphMLKeyName(key graphMLKey) string {
	if name := strings.TrimSpace(key.AttrName); name != "" {
		return name
	}
	return key.ID
}

// graphMLValue 按 attr.type 转换 GraphML data 值
func graphMLValue(key graphMLKey, text string) (any, error) {
	raw := strings.TrimSpace(text)
	switch strings.ToLower(strings.TrimSpace(key.AttrType)) {
	case "int", "long":
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("GraphML 属性 %s 不是整数: %w", graphMLKeyName(key), err)
		}
		return value, nil
	case "float", "double":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("GraphML 属性 %s 不是浮点数: %w", graphMLKeyName(key), err)
		}
		return value, nil
	case "boolean":
		switch strings.ToLower(raw) {
		case "true", "1":
			return true, nil
		case "false", "0":
			return false, nil
		default:
			return nil, fmt.Errorf("GraphML 属性 %s 不是布尔值: %s", graphMLKeyName(key), raw)
		}
	default:
		return text, nil
	}
}
