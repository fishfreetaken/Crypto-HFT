package main

import "math"

// ===== 技术指标 =====

func calcEMA(prices []float64, period int) float64 {
	if len(prices) < period {
		return 0
	}
	sum := 0.0
	for i := range period {
		sum += prices[i]
	}
	ema := sum / float64(period)
	k := 2.0 / float64(period+1)
	for i := period; i < len(prices); i++ {
		ema = prices[i]*k + ema*(1-k)
	}
	return ema
}

func calcRSI(prices []float64, period int) float64 {
	if len(prices) < period+1 {
		return 50
	}
	var gains, losses float64
	start := len(prices) - period - 1
	for i := start + 1; i <= start+period; i++ {
		diff := prices[i] - prices[i-1]
		if diff > 0 {
			gains += diff
		} else {
			losses -= diff
		}
	}
	if losses == 0 {
		return 100
	}
	rs := (gains / float64(period)) / (losses / float64(period))
	return 100 - 100/(1+rs)
}

// calcZLEMA 零延迟EMA：对输入价格做 2*p[t]-p[t-lag] 的超前调整后再算EMA，
// 将信号滞后压缩约 50%。lag = (period-1)/2
func calcZLEMA(prices []float64, period int) float64 {
	if len(prices) < period {
		return 0
	}
	lag := (period - 1) / 2
	adjusted := make([]float64, len(prices))
	for i := range prices {
		if i < lag {
			adjusted[i] = prices[i]
		} else {
			adjusted[i] = 2*prices[i] - prices[i-lag]
		}
	}
	return calcEMA(adjusted, period)
}

// calcTrendProb 统计最近 lookback 个价格变动中上涨占比（0~1），0.5 表示无明显趋势
func calcTrendProb(prices []float64, lookback int) float64 {
	n := len(prices)
	if n < 2 || lookback < 1 {
		return 0.5
	}
	if lookback >= n {
		lookback = n - 1
	}
	window := prices[n-lookback-1:]
	ups := 0
	for i := 1; i < len(window); i++ {
		if window[i] > window[i-1] {
			ups++
		}
	}
	return float64(ups) / float64(lookback)
}

// calcBollingerBands 计算布林带（Bollinger Bands）：均线 ± N 倍标准差。
// 在趋势中，价格回调至下轨是多头低风险入场点，反弹至上轨是空头低风险入场点。
func calcBollingerBands(prices []float64, period int, numStd float64) (upper, middle, lower float64) {
	if len(prices) < period {
		return 0, 0, 0
	}
	window := prices[len(prices)-period:]
	sum := 0.0
	for _, p := range window {
		sum += p
	}
	middle = sum / float64(period)
	variance := 0.0
	for _, p := range window {
		d := p - middle
		variance += d * d
	}
	std := math.Sqrt(variance / float64(period))
	upper = middle + numStd*std
	lower = middle - numStd*std
	return
}

// calcER 效率比率（Efficiency Ratio，Perry Kaufman）：
// ER = |净位移| / 路径总长，范围 0~1。
// 0 = 价格完全随机游走；1 = 价格方向一致完美趋势。
// 用于区分趋势市（ER 高，EMA 信号有效）与震荡市（ER 低，信号频繁失真）。
func calcER(prices []float64, period int) float64 {
	n := len(prices)
	if n < period+1 {
		return 0
	}
	window := prices[n-period-1:]
	netMove := math.Abs(window[period] - window[0])
	pathLen := 0.0
	for i := 1; i <= period; i++ {
		pathLen += math.Abs(window[i] - window[i-1])
	}
	if pathLen == 0 {
		return 0
	}
	return netMove / pathLen
}

// calcATR 平均真实波幅：最近 period 个采样点价格变化绝对值的均值。
// 结合 EMA 构成 Keltner 通道（KC），用于判断 BB 是否被 KC 包裹（挤压状态）。
func calcATR(prices []float64, period int) float64 {
	n := len(prices)
	if n < period+1 {
		return 0
	}
	sum := 0.0
	for i := n - period; i < n; i++ {
		sum += math.Abs(prices[i] - prices[i-1])
	}
	return sum / float64(period)
}

// ===== 卡尔曼滤波器（控制论噪声抑制）=====
//
// 卡尔曼滤波是控制论中的最优线性估计器（源自航天轨迹跟踪），将含噪观测分解为：
//   - 滤波价格（x0）：去除随机波动后的"真实价格"估计
//   - 速度估计（x1）：价格变化率，即当前趋势的方向与强度
//
// 状态模型（匀速运动 CV Model）：
//
//	x0[t+1] = x0[t] + x1[t] + w0  （价格 = 上次价格 + 速度 + 过程噪声）
//	x1[t+1] = x1[t] + w1          （速度缓慢随机游走 + 过程噪声）
//
// 观测模型：
//
//	z[t] = x0[t] + v               （观测价格 = 真实价格 + 测量噪声）
//
// 在 BTC 随机游走期：速度估计 x1 ≈ 0（无趋势）
// 在 BTC 趋势期：    速度估计 x1 ≠ 0（有方向），SNR = |x1| / (price × KalmanR) 反映趋势质量
//
// 参数说明（均为占价格的比率，如 0.0002 = 0.02%）：
//
//	KalmanQPos = 价格过程噪声标准差，越大→对新价格越敏感（响应快但抗噪弱）
//	KalmanQVel = 速度过程噪声标准差，越大→允许趋势变化越快
//	KalmanR    = 测量噪声标准差（反映 bid-ask 价差、随机扰动）
//	KalmanVelThresh = 最小速度阈值；低于此值认为市场无趋势，禁止 EMA 策略开仓
type kalmanFilter struct {
	x0, x1             float64 // 估计价格、估计速度
	p00, p01, p10, p11 float64 // 误差协方差矩阵
	ready              bool
}

// step 运行一步卡尔曼滤波预测+更新，返回滤波价格和速度估计值。
// Q0, Q1, R 为方差（= (price × sigma)^2），需在调用处计算。
func (kf *kalmanFilter) step(z, Q0, Q1, R float64) (px, vx float64) {
	if !kf.ready {
		kf.x0, kf.x1 = z, 0
		kf.p00, kf.p11 = R, Q1 // 初始不确定性
		kf.ready = true
		return z, 0
	}
	// 预测步：F = [[1,1],[0,1]]（匀速模型）
	x0p := kf.x0 + kf.x1
	x1p := kf.x1
	p00p := kf.p00 + kf.p01 + kf.p10 + kf.p11 + Q0
	p01p := kf.p01 + kf.p11
	p10p := kf.p10 + kf.p11
	p11p := kf.p11 + Q1
	// 更新步：H = [1, 0]（只观测价格）
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

// calcMomentum 计算价格动量（变化率 ROC）：最近 period 个采样点内的价格涨跌幅。
// 正值表示价格上涨，负值表示下跌，用于过滤 EMA 在无趋势市场中产生的虚假信号。
func calcMomentum(prices []float64, period int) float64 {
	n := len(prices)
	if n < period+1 || period < 1 {
		return 0
	}
	base := prices[n-1-period]
	if base == 0 {
		return 0
	}
	return (prices[n-1] - base) / base
}

func calcVolatility(prices []float64, period int) float64 {
	if len(prices) < period {
		return 0
	}
	window := prices[len(prices)-period:]
	lo, hi := window[0], window[0]
	for _, p := range window[1:] {
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
