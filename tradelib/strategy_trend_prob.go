package tradelib

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

// onPriceTrendProb 趋势概率策略：无止损，达目标收益率平仓，方向由趋势概率+随机扰动决定
func (s *Strategy) onPriceTrendProb(tick Tick, endTime time.Time) {
	price := tick.Prc
	s.feedPrice(price)

	// Kalman 滤波（预热期间也运行以积累状态估计）
	kVelPct := 0.0
	if s.Cfg.KalmanR > 0 {
		p2 := price * price
		_, vel := s.kf.step(price,
			p2*s.Cfg.KalmanQPos*s.Cfg.KalmanQPos,
			p2*s.Cfg.KalmanQVel*s.Cfg.KalmanQVel,
			p2*s.Cfg.KalmanR*s.Cfg.KalmanR)
		kVelPct = vel / price
	}

	if s.priceBuf.count < s.warmupNeed {
		if !s.Cfg.Quiet {
			fmt.Printf("[%s] [%-5s] 数据采集中 $%.2f (%d/%d)\n",
				time.Now().Format("15:04:05"), s.Cfg.Name, price, s.priceBuf.count, s.warmupNeed)
		}
		return
	}

	upProb := calcTrendProb(s.priceBuf, s.Cfg.TrendLookback)
	signal := ""

	// 时间 + 亏损双重衰减目标；positionPct/equityGain 提前计算供后续平仓逻辑复用
	var decayedTarget, decayElapsed, equityGain float64
	var positionPct float64
	if s.p.inPosition() {
		positionPct = s.p.positionPct(tick)
		equityGain = positionPct * s.Cfg.Leverage

		// 时间衰减因子：(1 - t/T)^decay_exp
		timeFactor := 1.0
		if s.Cfg.DecaySec > 0 {
			decayElapsed = time.Since(s.openTime).Seconds()
			ratio := 1.0 - decayElapsed/float64(s.Cfg.DecaySec)
			if ratio < 0 {
				ratio = 0
			}
			exp := s.Cfg.DecayExp
			if exp <= 0 {
				exp = 1.0
			}
			timeFactor = math.Pow(ratio, exp)
		}

		// 亏损衰减因子：亏损越大目标降得越快，叠加 noise_weight 随机扰动
		perfFactor := 1.0
		if s.Cfg.PerfDecayWeight > 0 && equityGain < 0 {
			pf := 1.0 + equityGain*s.Cfg.PerfDecayWeight
			randCoeff := 1.0 + (rand.Float64()*2-1)*s.Cfg.NoiseWeight*0.3
			perfFactor = math.Max(0, math.Min(1, pf*randCoeff))
		}

		// 盈利提升因子：收益持续扩大时，目标相应调高，延长持仓以获取更多利润
		gainFactor := 1.0
		if s.Cfg.ProfitBoostWeight > 0 && equityGain > 0 {
			gf := 1.0 + equityGain*s.Cfg.ProfitBoostWeight
			randCoeff := 1.0 + (rand.Float64()*2-1)*s.Cfg.NoiseWeight*0.2
			gainFactor = math.Max(1.0, gf*randCoeff)
		}

		decayedTarget = s.currentTarget * timeFactor * perfFactor * gainFactor
	}

	// 爆仓保护
	if s.p.inPosition() && s.p.totalEquity(s.Cfg, tick) <= 0 {
		s.p.closePos(s.Cfg, tick, "爆仓", &s.trades)
		signal = "\033[31m⚠ 爆仓\033[0m"
	}

	if s.p.inPosition() {
		triggered, stopMsg := s.p.checkStops(s.Cfg, tick, &s.trades)
		if triggered {
			signal = stopMsg
		} else {
			switch {
			case decayedTarget <= 0:
				s.p.closePos(s.Cfg, tick, "衰减止损", &s.trades)
				signal = fmt.Sprintf("\033[33m衰减止损\033[0m(持仓%.0fs 盈亏%+.1f%%)",
					decayElapsed, equityGain*100)
			case equityGain >= decayedTarget:
				s.p.closePos(s.Cfg, tick, "达标", &s.trades)
				signal = fmt.Sprintf("\033[32m达标平仓\033[0m(目标%.1f%%→%.1f%% 实%+.1f%%)",
					s.currentTarget*100, decayedTarget*100, equityGain*100)
			}
		}
	}

	// 开仓：不允许空仓，平仓后立即重新入场（无冷却期）
	if !s.p.inPosition() {
		w := s.Cfg.NoiseWeight
		var d posDir
		var strength float64
		var entryTag string

		if s.Cfg.KalmanR > 0 && s.kf.ready {
			snr := math.Abs(kVelPct) / math.Max(s.Cfg.KalmanR, 1e-10)
			strength = math.Min(1.0, snr/3.0)
			strength = math.Max(0, strength+(rand.Float64()*2-1)*w*0.2)
			if kVelPct >= 0 {
				d = dirLong
				entryTag = fmt.Sprintf("K做多(SNR:%.1f 速%+.4f%%)", snr, kVelPct*100)
			} else {
				d = dirShort
				entryTag = fmt.Sprintf("K做空(SNR:%.1f 速%+.4f%%)", snr, kVelPct*100)
			}
		} else {
			longScore := (1-w)*upProb + w*rand.Float64()
			shortScore := (1-w)*(1-upProb) + w*rand.Float64()
			if longScore >= shortScore {
				d, strength = dirLong, upProb
				entryTag = fmt.Sprintf("趋势做多(概率%.0f%%)", upProb*100)
			} else {
				d, strength = dirShort, 1-upProb
				entryTag = fmt.Sprintf("趋势做空(概率%.0f%%)", (1-upProb)*100)
			}
		}

		baseTarget := s.Cfg.TargetMin + strength*(s.Cfg.TargetMax-s.Cfg.TargetMin)
		noise := w * (rand.Float64()*2 - 1) * (s.Cfg.TargetMax - s.Cfg.TargetMin) * 0.5
		target := math.Min(s.Cfg.TargetMax, math.Max(s.Cfg.TargetMin, baseTarget+noise))
		s.currentTarget = target
		s.openTime = time.Now()
		s.p.openPosVolAdjusted(s.Cfg, d, tick, 0.015, &s.trades)
		signal = fmt.Sprintf("%s 目标%.1f%%", entryTag, target*100)
	}

	// 状态输出
	equity := s.p.totalEquity(s.Cfg, tick)
	pnl := equity - s.Cfg.InitialCapital
	pnlPct := pnl / s.Cfg.InitialCapital * 100
	timeLeft := time.Until(endTime).Round(time.Second)
	position := "空仓"
	if s.p.inPosition() {
		pct := s.p.positionPct(tick)
		decayRemain := time.Duration(float64(s.Cfg.DecaySec)-decayElapsed) * time.Second
		position = fmt.Sprintf("%s仓 %+.3f%% 目标%.1f%%→%.1f%%(剩%v)",
			s.p.direction, pct*100, s.currentTarget*100, decayedTarget*100, decayRemain.Round(time.Second))
	}
	var trendInfo string
	if s.Cfg.KalmanR > 0 && s.kf.ready {
		snr := math.Abs(kVelPct) / math.Max(s.Cfg.KalmanR, 1e-10)
		snrLabel := "弱"
		if snr >= 2 { snrLabel = "中" }
		if snr >= 3 { snrLabel = "强" }
		kColor := "\033[33m"
		if kVelPct > 0 { kColor = "\033[32m" } else if kVelPct < 0 { kColor = "\033[31m" }
		trendInfo = fmt.Sprintf("$%.2f K:%s%+.5f%%\033[0m SNR:%.1f(%s) 概率:%.0f%%",
			price, kColor, kVelPct*100, snr, snrLabel, upProb*100)
	} else {
		upColor := "\033[33m"
		if upProb > 0.55 { upColor = "\033[32m" } else if upProb < 0.45 { upColor = "\033[31m" }
		trendInfo = fmt.Sprintf("$%.2f 趋↑%s%.0f%%\033[0m", price, upColor, upProb*100)
	}
	if signal != "" {
		fmt.Printf("[%s] [%-5s] %s | 权益:$%.2f(%+.2f%%) | %-42s | 剩余:%v | %s\n",
			time.Now().Format("15:04:05"), s.Cfg.Name, trendInfo, equity, pnlPct, position, timeLeft, signal)
	} else if !s.Cfg.Quiet {
		fmt.Printf("[%s] [%-5s] %s | 权益:$%.2f(%+.2f%%) | %-42s | 剩余:%v\n",
			time.Now().Format("15:04:05"), s.Cfg.Name, trendInfo, equity, pnlPct, position, timeLeft)
	}
}
