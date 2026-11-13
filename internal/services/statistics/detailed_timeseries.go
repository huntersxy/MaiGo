package stats

import (
	"strings"
	"time"
)

type detailedChartWindow struct {
	start    time.Time
	step     time.Duration
	size     int
	costs    []float64
	byModel  map[string][]float64
	byModule map[string][]float64
	byChat   map[string][]int64
}

type detailedMetricsWindow struct {
	start       time.Time
	step        time.Duration
	size        int
	costs       []float64
	tokens      []int64
	messages    []int64
	replies     []int64
	onlineHours []float64
}

func newDetailedChartWindow(start time.Time, hours int, intervalMinutes int) *detailedChartWindow {
	size := hours*60/intervalMinutes + 1
	return &detailedChartWindow{
		start:    start,
		step:     time.Duration(intervalMinutes) * time.Minute,
		size:     size,
		costs:    make([]float64, size),
		byModel:  map[string][]float64{},
		byModule: map[string][]float64{},
		byChat:   map[string][]int64{},
	}
}

func newDetailedMetricsWindow(start time.Time, hours int, intervalMinutes int) *detailedMetricsWindow {
	size := hours*60/intervalMinutes + 1
	return &detailedMetricsWindow{
		start:       start,
		step:        time.Duration(intervalMinutes) * time.Minute,
		size:        size,
		costs:       make([]float64, size),
		tokens:      make([]int64, size),
		messages:    make([]int64, size),
		replies:     make([]int64, size),
		onlineHours: make([]float64, size),
	}
}

func (w *detailedChartWindow) index(timestamp time.Time) int {
	return int(timestamp.Sub(w.start) / w.step)
}

func (w *detailedChartWindow) labels() []string {
	labels := make([]string, w.size)
	for i := range labels {
		labels[i] = w.start.Add(time.Duration(i) * w.step).In(time.Local).Format("15:04")
	}
	return labels
}

func (w *detailedChartWindow) result() DetailedStatisticsTrendData {
	return DetailedStatisticsTrendData{
		TimeLabels:    w.labels(),
		TotalCostData: w.costs,
		CostByModel:   w.byModel,
		CostByModule:  w.byModule,
		MessageByChat: w.byChat,
	}
}

func (w *detailedMetricsWindow) index(timestamp time.Time) int {
	return int(timestamp.Sub(w.start) / w.step)
}

func (w *detailedMetricsWindow) labels() []string {
	layout := "01-02"
	if w.step == time.Hour {
		layout = "15:04"
	}
	labels := make([]string, w.size)
	for i := range labels {
		labels[i] = w.start.Add(time.Duration(i) * w.step).In(time.Local).Format(layout)
	}
	return labels
}

func (w *detailedMetricsWindow) result() DetailedStatisticsMetricsData {
	out := DetailedStatisticsMetricsData{
		TimeLabels:         w.labels(),
		CostPer100Messages: make([]float64, w.size),
		CostPerHour:        make([]float64, w.size),
		TokensPerHour:      make([]float64, w.size),
		CostPer100Replies:  make([]float64, w.size),
	}
	for i := 0; i < w.size; i++ {
		if w.messages[i] > 0 {
			out.CostPer100Messages[i] = w.costs[i] / float64(w.messages[i]) * 100
		}
		if w.onlineHours[i] > 0 {
			out.CostPerHour[i] = w.costs[i] / w.onlineHours[i]
			out.TokensPerHour[i] = float64(w.tokens[i]) / w.onlineHours[i]
		}
		if w.replies[i] > 0 {
			out.CostPer100Replies[i] = w.costs[i] / float64(w.replies[i]) * 100
		}
	}
	return out
}

func buildDetailedTimeSeries(now time.Time, usage []DetailedUsage, messages []DetailedMessage, intervals []OnlineInterval, accounts []DetailedBotAccount) (map[string]DetailedStatisticsTrendData, map[string]DetailedStatisticsMetricsData) {
	now = now.In(time.Local)
	charts := map[string]*detailedChartWindow{
		"6h":  newDetailedChartWindow(now.Add(-6*time.Hour), 6, 10),
		"12h": newDetailedChartWindow(now.Add(-12*time.Hour), 12, 15),
		"24h": newDetailedChartWindow(now.Add(-24*time.Hour), 24, 15),
		"48h": newDetailedChartWindow(now.Add(-48*time.Hour), 48, 30),
	}
	metrics := map[string]*detailedMetricsWindow{
		"24h": newDetailedMetricsWindow(now.Add(-24*time.Hour), 24, 60),
		"7d":  newDetailedMetricsWindow(now.Add(-7*24*time.Hour), 168, 1440),
		"30d": newDetailedMetricsWindow(now.Add(-30*24*time.Hour), 720, 1440),
	}

	for _, record := range usage {
		timestamp := record.Timestamp.In(time.Local)
		model := firstNonEmpty(record.ModelAssignName, record.ModelName, "unknown")
		requestType := firstNonEmpty(record.RequestType, "unknown")
		module := requestType
		if index := strings.Index(requestType, "."); index >= 0 {
			module = requestType[:index]
		}
		tokens := record.PromptTokens + record.CompletionTokens
		for _, chart := range charts {
			index := chart.index(timestamp)
			if index < 0 || index >= chart.size {
				continue
			}
			chart.costs[index] += record.Cost
			addDetailedChartCost(chart.byModel, model, index, record.Cost, chart.size)
			addDetailedChartCost(chart.byModule, module, index, record.Cost, chart.size)
		}
		for _, series := range metrics {
			index := series.index(timestamp)
			if index < 0 || index >= series.size {
				continue
			}
			series.costs[index] += record.Cost
			series.tokens[index] += tokens
		}
	}

	botSelf := detailedBotSelfSet(accounts)
	for _, message := range messages {
		timestamp := message.StatisticsTime.In(time.Local)
		if message.ChatName != "" {
			for _, chart := range charts {
				index := chart.index(timestamp)
				if index < 0 || index >= chart.size {
					continue
				}
				values := chart.byChat[message.ChatName]
				if values == nil {
					values = make([]int64, chart.size)
					chart.byChat[message.ChatName] = values
				}
				values[index]++
			}
		}
		isReply := false
		if message.Platform != "" && message.UserID != "" {
			_, isReply = botSelf[message.Platform][message.UserID]
		}
		for _, series := range metrics {
			index := series.index(timestamp)
			if index < 0 || index >= series.size {
				continue
			}
			series.messages[index]++
			if isReply {
				series.replies[index]++
			}
		}
	}

	for _, series := range metrics {
		for _, interval := range intervals {
			for index := 0; index < series.size; index++ {
				bucketStart := series.start.Add(time.Duration(index) * series.step)
				overlapStart := interval.Start
				if overlapStart.Before(bucketStart) {
					overlapStart = bucketStart
				}
				overlapEnd := interval.End
				bucketEnd := bucketStart.Add(series.step)
				if overlapEnd.After(bucketEnd) {
					overlapEnd = bucketEnd
				}
				if overlapEnd.After(overlapStart) {
					series.onlineHours[index] += overlapEnd.Sub(overlapStart).Seconds() / 3600
				}
			}
		}
	}

	trends := make(map[string]DetailedStatisticsTrendData, len(charts))
	for key, chart := range charts {
		trends[key] = chart.result()
	}
	metricData := make(map[string]DetailedStatisticsMetricsData, len(metrics))
	for key, series := range metrics {
		metricData[key] = series.result()
	}
	return trends, metricData
}

func addDetailedChartCost(target map[string][]float64, name string, index int, cost float64, size int) {
	values := target[name]
	if values == nil {
		values = make([]float64, size)
		target[name] = values
	}
	values[index] += cost
}

func detailedBotSelfSet(accounts []DetailedBotAccount) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for _, account := range accounts {
		platform := strings.TrimSpace(account.Platform)
		accountID := strings.TrimSpace(account.AccountID)
		if platform == "" || accountID == "" {
			continue
		}
		if out[platform] == nil {
			out[platform] = map[string]struct{}{}
		}
		out[platform][accountID] = struct{}{}
	}
	return out
}
