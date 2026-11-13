package quote

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"unicode/utf8"
)

// Frequency 引用频率档位
type Frequency string

const (
	FrequencyNever        Frequency = "never"
	FrequencyOccasionally Frequency = "occasionally"
	FrequencyOften        Frequency = "often"
)

// QuoteProbability 群聊固定公式
func QuoteProbability(textLength int, previousSenderSelf bool, previousSeconds float64, frequency Frequency, targetDistance int) (float64, error) {
	if targetDistance < 0 {
		return 0, fmt.Errorf("引用目标距离不能为负数")
	}
	if frequency == FrequencyNever {
		return 0, nil
	}
	if frequency != FrequencyOccasionally && frequency != FrequencyOften {
		return 0, fmt.Errorf("未知的群聊引用频率：%s", frequency)
	}
	var probability float64
	if textLength == 0 {
		if previousSenderSelf {
			if previousSeconds <= 3 {
				probability = 0.005913471588878362
			} else {
				probability = 0.013812173277286514
			}
		} else if previousSeconds <= 60 {
			probability = 0.025472825783009516
		} else {
			probability = 0.0108627046866078
		}
	} else if previousSenderSelf {
		if previousSeconds <= 3 {
			probability = 0.011765162067321502
		} else {
			probability = 0.07760369512696548
		}
	} else if textLength <= 50 {
		probability = 0.20934408508581265
	} else {
		probability = 0.08614950202149084
	}
	var distanceMultiplier float64
	switch {
	case targetDistance == 0:
		distanceMultiplier = 0.5
	case targetDistance <= 2:
		distanceMultiplier = 1.0
	case targetDistance <= 5:
		distanceMultiplier = 1.1
	default:
		distanceMultiplier = 1.5
	}
	mult := 1.0
	if frequency == FrequencyOften {
		mult = 2
	}
	out := probability * mult * distanceMultiplier
	if out > 1 {
		return 1, nil
	}
	return out, nil
}

// PrivateQuoteProbability 私聊 logit 公式
func PrivateQuoteProbability(textLength int, previousSenderSelf bool, previousSeconds float64, previousBurst, targetDistance int, frequency Frequency) (float64, error) {
	if targetDistance < 0 {
		return 0, fmt.Errorf("引用目标距离不能为负数")
	}
	if frequency == FrequencyNever {
		return 0, nil
	}
	if frequency != FrequencyOccasionally && frequency != FrequencyOften {
		return 0, fmt.Errorf("未知的私聊引用频率：%s", frequency)
	}
	z := -4.176576611204103
	if previousSenderSelf {
		if previousSeconds <= 3 {
			z += -2.0390866101745555
		} else {
			z += 0.407259409803155
		}
	} else {
		burst := previousBurst
		if burst < 1 {
			burst = 1
		}
		if burst > 16 {
			burst = 16
		}
		z += 0.6811570734750046 * math.Log2(float64(burst))
	}
	if textLength == 0 {
		z -= 3.4527659205203576
	}
	dist := targetDistance + 1
	if dist > 32 {
		dist = 32
	}
	z += 0.521800209819322 * math.Log2(float64(dist))
	probability := 1 / (1 + math.Exp(-z))
	if frequency == FrequencyOften {
		probability *= 2
	}
	if probability > 1 {
		return 1, nil
	}
	return probability, nil
}

// Decide 窗口内按公式抽签；窗口外或无序号时直接引用
func Decide(ctx *Context, botUserID, text string, frequency Frequency, isGroup bool, rng *rand.Rand) (bool, float64, error) {
	if frequency == FrequencyNever {
		return false, 0, nil
	}
	if frequency != FrequencyOccasionally && frequency != FrequencyOften {
		return false, 0, fmt.Errorf("未知的引用频率：%s", frequency)
	}
	if ctx == nil || ctx.TargetDistance == nil {
		return true, 1, nil
	}
	textLen := utf8.RuneCountInString(strings.TrimSpace(text))
	prevSelf := ctx.PreviousSenderID == botUserID
	var probability float64
	var err error
	if isGroup {
		probability, err = QuoteProbability(textLen, prevSelf, ctx.PreviousSeconds, frequency, *ctx.TargetDistance)
	} else {
		probability, err = PrivateQuoteProbability(textLen, prevSelf, ctx.PreviousSeconds, ctx.PreviousBurst, *ctx.TargetDistance, frequency)
	}
	if err != nil {
		return false, 0, err
	}
	if rng == nil {
		rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	return rng.Float64() < probability, probability, nil
}

// ParseFrequency 解析配置字符串
func ParseFrequency(raw string) Frequency {
	switch strings.TrimSpace(raw) {
	case string(FrequencyNever):
		return FrequencyNever
	case string(FrequencyOften):
		return FrequencyOften
	default:
		return FrequencyOccasionally
	}
}
