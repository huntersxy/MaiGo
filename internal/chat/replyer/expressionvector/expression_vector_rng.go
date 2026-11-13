package expressionvector

import "math/bits"

// numpy 2.5.4 PCG64 与 SeedSequence 的最小复刻
// 只服务表达向量聚类初始化，seed 固定为 Python 侧 20260621
// 参考 numpy/random/_pcg64.pyx、numpy/random/bit_generator.pyx

const (
	expressionVectorPCGMultiplierHi = uint64(2549297995355413924)
	expressionVectorPCGMultiplierLo = uint64(4865540595714422341)

	expressionVectorSeedHashInitA = uint32(0x43b0d7e5)
	expressionVectorSeedHashMultA = uint32(0x931e8875)
	expressionVectorSeedHashInitB = uint32(0x8b51f9dd)
	expressionVectorSeedHashMultB = uint32(0x58f38ded)
	expressionVectorSeedMixLeft   = uint32(0xca01f9dd)
	expressionVectorSeedMixRight  = uint32(0x4973f715)
	expressionVectorSeedXShift    = 16
)

type expressionVectorSeedSequence struct {
	pool      [4]uint32
	hashConst uint32
}

// newExpressionVectorSeedSequence 对齐 SeedSequence(seed) 的单 uint32 entropy 路径
func newExpressionVectorSeedSequence(seed uint32) *expressionVectorSeedSequence {
	sequence := &expressionVectorSeedSequence{hashConst: expressionVectorSeedHashInitA}
	sequence.pool[0] = sequence.hashMix(seed)
	for index := 1; index < len(sequence.pool); index++ {
		sequence.pool[index] = sequence.hashMix(0)
	}
	for source := range sequence.pool {
		for destination := range sequence.pool {
			if source == destination {
				continue
			}
			sequence.pool[destination] = expressionVectorSeedMix(
				sequence.pool[destination],
				sequence.hashMix(sequence.pool[source]),
			)
		}
	}
	return sequence
}

func (s *expressionVectorSeedSequence) hashMix(value uint32) uint32 {
	value ^= s.hashConst
	s.hashConst *= expressionVectorSeedHashMultA
	value *= s.hashConst
	value ^= value >> expressionVectorSeedXShift
	return value
}

func expressionVectorSeedMix(left, right uint32) uint32 {
	value := expressionVectorSeedMixLeft*left - expressionVectorSeedMixRight*right
	value ^= value >> expressionVectorSeedXShift
	return value
}

// generateState 生成 count 个 uint32 状态词
func (s *expressionVectorSeedSequence) generateState(count int) []uint32 {
	state := make([]uint32, count)
	hashConst := uint32(expressionVectorSeedHashInitB)
	for index := range state {
		value := s.pool[index%len(s.pool)] ^ hashConst
		hashConst *= expressionVectorSeedHashMultB
		value *= hashConst
		value ^= value >> expressionVectorSeedXShift
		state[index] = value
	}
	return state
}

// expressionVectorPCG64 复刻 numpy PCG64 的 64/32 位输出与 bounded 整数
type expressionVectorPCG64 struct {
	stateHi uint64
	stateLo uint64
	incHi   uint64
	incLo   uint64

	hasCached bool
	cached    uint32
}

func newExpressionVectorPCG64(seed uint32) *expressionVectorPCG64 {
	stateWords := newExpressionVectorSeedSequence(seed).generateState(8)
	var seedWords [4]uint64
	for index := range seedWords {
		seedWords[index] = uint64(stateWords[index*2]) | uint64(stateWords[index*2+1])<<32
	}
	rng := &expressionVectorPCG64{
		incHi: seedWords[2]<<1 | seedWords[3]>>63,
		incLo: seedWords[3]<<1 | 1,
	}
	rng.step()
	var carry uint64
	rng.stateLo, carry = bits.Add64(rng.stateLo, seedWords[1], 0)
	rng.stateHi, _ = bits.Add64(rng.stateHi, seedWords[0], carry)
	rng.step()
	return rng
}

func (r *expressionVectorPCG64) step() {
	hi, lo := expressionVectorMultiply128(
		r.stateHi,
		r.stateLo,
		expressionVectorPCGMultiplierHi,
		expressionVectorPCGMultiplierLo,
	)
	var carry uint64
	lo, carry = bits.Add64(lo, r.incLo, 0)
	hi, _ = bits.Add64(hi, r.incHi, carry)
	r.stateHi, r.stateLo = hi, lo
}

func expressionVectorMultiply128(aHi, aLo, bHi, bLo uint64) (uint64, uint64) {
	hi, lo := bits.Mul64(aLo, bLo)
	_, firstHigh := bits.Mul64(aHi, bLo)
	_, secondHigh := bits.Mul64(aLo, bHi)
	hi, _ = bits.Add64(hi, firstHigh, 0)
	hi, _ = bits.Add64(hi, secondHigh, 0)
	return hi, lo
}

func (r *expressionVectorPCG64) next64() uint64 {
	r.step()
	value := r.stateHi ^ r.stateLo
	rotation := r.stateHi >> 58
	return bits.RotateLeft64(value, -int(rotation))
}

func (r *expressionVectorPCG64) next32() uint32 {
	if r.hasCached {
		r.hasCached = false
		return r.cached
	}
	value := r.next64()
	r.hasCached = true
	r.cached = uint32(value >> 32)
	return uint32(value)
}

// bounded32 对齐 numpy rng.integers(0, rng+1) 的 32 位无偏拒绝采样
func (r *expressionVectorPCG64) bounded32(rng uint32) uint32 {
	if rng == 0 {
		return 0
	}
	rngExclusive := rng + 1
	product := uint64(r.next32()) * uint64(rngExclusive)
	leftover := uint32(product)
	if leftover < rngExclusive {
		threshold := (^uint32(0) - rng) % rngExclusive
		for leftover < threshold {
			product = uint64(r.next32()) * uint64(rngExclusive)
			leftover = uint32(product)
		}
	}
	return uint32(product >> 32)
}

func (r *expressionVectorPCG64) random() float64 {
	return float64(r.next64()>>11) * (1.0 / 9007199254740992.0)
}
