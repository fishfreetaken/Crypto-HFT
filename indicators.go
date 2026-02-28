package main

import "math"

// ===== 基础数据结构：环形缓冲区 =====
type RingBuffer struct {
	data  []float64
	size  int
	count int
	pos   int
}

func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		data: make([]float64, size),
		size: size,
	}
}

func (r *RingBuffer) Add(val float64) {
	if r.size == 0 {
		return
	}
	r.data[r.pos] = val
	r.pos = (r.pos + 1) % r.size
	if r.count < r.size {
		r.count++
	}
}

// Get 返回 ago 个 tick 前的值 (0 = 当前最新值)
func (r *RingBuffer) Get(ago int) float64 {
	if ago >= r.count || r.count == 0 {
		return 0
	}
	idx := (r.pos - 1 - ago) % r.size
	if idx < 0 {
		idx += r.size
	}
	return r.data[idx]
}

// ===== 状态化技术指标 (O(1) 计算) =====

type StatefulEMA struct {
	period int
	alpha  float64
	val    float64
	count  int
	sum    float64
}

func NewStatefulEMA(period int) *StatefulEMA {
	return &StatefulEMA{
		period: period,
		alpha:  2.0 / float64(period+1),
	}
}

func (e *StatefulEMA) Update(price float64) float64 {
	if e.count < e.period {
		e.sum += price
		e.count++
		if e.count == e.period {
			e.val = e.sum / float64(e.period)
		}
		if e.count > 0 {
			return e.sum / float64(e.count)
		}
		return 0
	}
	e.val = price*e.alpha + e.val*(1-e.alpha)
	return e.val
}

func (e *StatefulEMA) Value() float64 { return e.val }

type StatefulZLEMA struct {
	period int
	lag    int
	ema    *StatefulEMA
	buf    *RingBuffer
}

func NewStatefulZLEMA(period int) *StatefulZLEMA {
	lag := (period - 1) / 2
	return &StatefulZLEMA{
		period: period,
		lag:    lag,
		ema:    NewStatefulEMA(period),
		buf:    NewRingBuffer(lag + 1),
	}
}

func (z *StatefulZLEMA) Update(price float64) float64 {
	z.buf.Add(price)
	var adjusted float64
	if z.buf.count <= z.lag {
		adjusted = price
	} else {
		oldPrice := z.buf.Get(z.lag)
		adjusted = 2*price - oldPrice
	}
	return z.ema.Update(adjusted)
}

func (z *StatefulZLEMA) Value() float64 { return z.ema.Value() }

// ===== 基于 RingBuffer 的滑动窗口技术指标 (O(Period) 计算) =====

func calcRSI(buf *RingBuffer, period int) float64 {
	if buf.count < period+1 || period == 0 {
		return 50.0
	}
	var gains, losses float64
	for i := 0; i < period; i++ {
		diff := buf.Get(i) - buf.Get(i+1)
		if diff > 0 {
			gains += diff
		} else {
			losses -= diff
		}
	}
	if losses == 0 {
		return 100.0
	}
	rs := (gains / float64(period)) / (losses / float64(period))
	return 100.0 - 100.0/(1+rs)
}

func calcBollingerBands(buf *RingBuffer, period int, numStd float64) (upper, middle, lower float64) {
	if buf.count < period || period == 0 {
		return 0, 0, 0
	}
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += buf.Get(i)
	}
	middle = sum / float64(period)
	variance := 0.0
	for i := 0; i < period; i++ {
		d := buf.Get(i) - middle
		variance += d * d
	}
	std := math.Sqrt(variance / float64(period))
	upper = middle + numStd*std
	lower = middle - numStd*std
	return
}

func calcER(buf *RingBuffer, period int) float64 {
	if buf.count < period+1 || period == 0 {
		return 0
	}
	netMove := math.Abs(buf.Get(0) - buf.Get(period))
	pathLen := 0.0
	for i := 0; i < period; i++ {
		pathLen += math.Abs(buf.Get(i) - buf.Get(i+1))
	}
	if pathLen == 0 {
		return 0
	}
	return netMove / pathLen
}

func calcATR(buf *RingBuffer, period int) float64 {
	if buf.count < period+1 || period == 0 {
		return 0
	}
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += math.Abs(buf.Get(i) - buf.Get(i+1))
	}
	return sum / float64(period)
}

func calcMomentum(buf *RingBuffer, period int) float64 {
	if buf.count < period+1 || period == 0 {
		return 0
	}
	base := buf.Get(period)
	if base == 0 {
		return 0
	}
	return (buf.Get(0) - base) / base
}

func calcVolatility(buf *RingBuffer, period int) float64 {
	if buf.count < period || period == 0 {
		return 0
	}
	lo := buf.Get(0)
	hi := lo
	for i := 1; i < period; i++ {
		p := buf.Get(i)
		if p < lo {
			lo = p
		}
		if p > hi {
			hi = p
		}
	}
	if lo == 0 {
		return 0
	}
	return (hi - lo) / lo
}

func calcTrendProb(buf *RingBuffer, lookback int) float64 {
	if buf.count < 2 || lookback < 1 {
		return 0.5
	}
	period := lookback
	if period >= buf.count {
		period = buf.count - 1
	}
	ups := 0
	for i := 0; i < period; i++ {
		if buf.Get(i) > buf.Get(i+1) {
			ups++
		}
	}
	return float64(ups) / float64(period)
}

// ===== 卡尔曼滤波器 (匀速控制论模型, 完全状态化 O(1)) =====
type kalmanFilter struct {
	x0, x1             float64
	p00, p01, p10, p11 float64
	ready              bool
}

func (kf *kalmanFilter) step(z, Q0, Q1, R float64) (px, vx float64) {
	if !kf.ready {
		kf.x0, kf.x1 = z, 0
		kf.p00, kf.p11 = R, Q1
		kf.ready = true
		return z, 0
	}
	x0p := kf.x0 + kf.x1
	x1p := kf.x1
	p00p := kf.p00 + kf.p01 + kf.p10 + kf.p11 + Q0
	p01p := kf.p01 + kf.p11
	p10p := kf.p10 + kf.p11
	p11p := kf.p11 + Q1
	innov := z - x0p
	S := p00p + R
	K0 := p00p / S
	K1 := p10p / S
	kf.x0 = x0p + K0*innov
	kf.x1 = x1p + K1*innov
	kf.p00 = (1 - K0) * p00p
	kf.p01 = (1 - K0) * p01p
	kf.p10 = p10p - K1*p00p
	kf.p11 = p11p - K1*p01p
	return kf.x0, kf.x1
}
