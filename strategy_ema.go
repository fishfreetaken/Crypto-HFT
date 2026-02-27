package main

import (
	"fmt"
	"math/rand"
	"time"
)

// onPriceEMA EMA均值回归策略（默认策略）：
//
// 原理：EMA 定短期动量方向，BB 回调定入场时机，ER 过滤震荡市。
//   - emaBullish + 价格触达BB下轨 + ER>阈值 → 做多（均值回归）
//   - emaBearish + 价格触达BB上轨 + ER>阈值 → 做空（均值回归）
//   - 逆势方向（价格在trendEMA反侧）ER阈值×1.5，更严格过滤
func (s *Strategy) onPriceEMA(price float64, endTime time.Time) {
	s.prices = append(s.prices, price)

	// Kalman 滤波（预热期间也运行，积累状态估计）
	kVelPct := 0.0
	if s.cfg.KalmanR > 0 {
		p2 := price * price
		_, vel := s.kf.step(price,
			p2*s.cfg.KalmanQPos*s.cfg.KalmanQPos,
			p2*s.cfg.KalmanQVel*s.cfg.KalmanQVel,
			p2*s.cfg.KalmanR*s.cfg.KalmanR)
		kVelPct = vel / price
	}

	if len(s.prices) < s.warmupNeed {
		fmt.Printf("[%s] [%-4s] 数据采集中 $%.2f (%d/%d)\n",
			time.Now().Format("15:04:05"), s.cfg.Name, price, len(s.prices), s.warmupNeed)
		return
	}

	// ZLEMA 减少约 50% 滞后；趋势线用普通 EMA 保持平滑
	shortEMA := calcZLEMA(s.prices, s.cfg.EMAShort)
	longEMA := calcZLEMA(s.prices, s.cfg.EMALong)
	trendEMA := calcEMA(s.prices, s.cfg.TrendPeriod)
	rsi := calcRSI(s.prices, s.cfg.RSIPeriod)
	signal := ""

	// 加入小幅随机扰动，避免多策略同步触发同一信号（NoiseWeight=0 时无扰动）
	noisyShortEMA := shortEMA
	if s.cfg.NoiseWeight > 0 {
		noisyShortEMA += price * s.cfg.NoiseWeight * 0.001 * (rand.Float64()*2 - 1)
	}

	// ─── 每 tick 都计算全量指标，用于状态显示 + 信号生成 ───
	er := 0.0
	bbUpper, bbLower := 0.0, 0.0
	if s.cfg.BBPeriod > 0 {
		er = calcER(s.prices, s.cfg.ERPeriod)
		bbUpper, _, bbLower = calcBollingerBands(s.prices, s.cfg.BBPeriod, s.cfg.BBStdDev)
	}

	// 构建指标状态字符串（让用户一眼看清当前各指标状态）
	emaArrow := "\033[31m▼\033[0m"
	if shortEMA > longEMA {
		emaArrow = "\033[32m▲\033[0m"
	}
	trendPos := "\033[32m↑趋\033[0m" // 价格在 trendEMA 上方（顺多/逆空）
	if price < trendEMA {
		trendPos = "\033[31m↓趋\033[0m" // 价格在 trendEMA 下方（逆多/顺空）
	}
	bbTag := ""
	if s.cfg.BBPeriod > 0 {
		pricePos := "  中  "
		if price <= bbLower {
			pricePos = "\033[32m←下轨\033[0m"
		} else if price >= bbUpper {
			pricePos = "\033[31m→上轨\033[0m"
		}
		bbTag = fmt.Sprintf(" BB:%.0f/%s/%.0f ER:%.2f", bbLower, pricePos, bbUpper, er)
	}
	kColor := "\033[33m"
	if kVelPct > 0 {
		kColor = "\033[32m"
	} else if kVelPct < 0 {
		kColor = "\033[31m"
	}
	indicators := fmt.Sprintf("$%.2f EMA%s %s RSI:%.1f%s K:%s%+.4f%%\033[0m",
		price, emaArrow, trendPos, rsi, bbTag, kColor, kVelPct*100)

	// 更新峰值权益
	equity := s.p.totalEquity(s.cfg, price)
	if equity > s.p.peakEquity {
		s.p.peakEquity = equity
	}
	drawdown := (s.p.peakEquity - equity) / s.p.peakEquity

	// 波动熔断检测
	inSafety := time.Now().Before(s.safetyUntil)
	if !inSafety && len(s.prices) >= s.cfg.VolatilityPeriod {
		vol := calcVolatility(s.prices, s.cfg.VolatilityPeriod)
		if vol >= s.cfg.VolatilityThreshold && drawdown >= s.cfg.SafetyDrawdown {
			if s.p.inPosition() {
				s.p.closePos(s.cfg, price, "波动熔断", &s.trades)
			}
			s.safetyUntil = time.Now().Add(s.cfg.safetyCooldown())
			inSafety = true
			fmt.Printf("[%s] [%-4s] \033[35m⚠ 波动熔断！\033[0m 波动:%.3f%% 回撤:%.2f%% → 冷静%v\n",
				time.Now().Format("15:04:05"), s.cfg.Name, vol*100, drawdown*100, s.cfg.safetyCooldown())
		}
	}

	// 安全冷静期：只更新 EMA 状态，不开仓
	if inSafety {
		remaining := time.Until(s.safetyUntil).Round(time.Second)
		s.prevShortEMA = shortEMA
		s.prevLongEMA = longEMA
		s.printStatus(price, endTime, indicators,
			fmt.Sprintf("\033[35m安全冷静(%v) 回撤:%.2f%%\033[0m", remaining, drawdown*100))
		return
	}

	inCooldown := time.Since(s.p.lastTradeTime) < s.cfg.tradeCooldown()

	// 优先级 1：止损 / 止盈 / RSI 极值平仓（仅盈利时触发 RSI 平仓）
	if s.p.inPosition() {
		pct := s.p.positionPct(price)
		switch {
		case pct <= -s.cfg.StopLoss:
			s.p.closePos(s.cfg, price, "止损", &s.trades)
			signal = fmt.Sprintf("\033[31m止损(%.3f%%)\033[0m", pct*100)
		case pct >= s.cfg.TakeProfit:
			s.p.closePos(s.cfg, price, "止盈", &s.trades)
			signal = fmt.Sprintf("\033[32m止盈(+%.3f%%)\033[0m", pct*100)
		case s.p.direction == dirLong && rsi > s.cfg.RSIExitLong && pct > 0:
			s.p.closePos(s.cfg, price, "RSI超买", &s.trades)
			signal = fmt.Sprintf("RSI超买(%.1f)平多", rsi)
		case s.p.direction == dirShort && rsi < s.cfg.RSIExitShort && pct > 0:
			s.p.closePos(s.cfg, price, "RSI超卖", &s.trades)
			signal = fmt.Sprintf("RSI超卖(%.1f)平空", rsi)
		}
	}

	// 优先级 2：开仓信号
	// 【新方案 BBPeriod>0】EMA 定短期动量方向 + BB 回调定入场时机 + ER 过滤震荡市
	//   - emaBullish/emaBearish 只看短/长 EMA 的相对位置，不强制要求价格在 trendEMA 哪侧
	//   - trendEMA 用于区分"顺势"与"逆势"：顺势用正常 ER 门槛，逆势门槛提高 50%（更严格过滤）
	//   - 这样在 BTC 牛市期间也能捕捉短期回撤的做空机会，实现多空双向均衡
	// 【旧方案 BBPeriod=0】EMA 金叉/死叉 + 动量 ROC 过滤（向后兼容）
	if !s.p.inPosition() && !inCooldown {
		// Kalman 速度过滤：速度 < 阈值时认为市场处于随机游走，禁止开仓
		kVelLong := s.cfg.KalmanR <= 0 || s.cfg.KalmanVelThresh <= 0 || kVelPct > s.cfg.KalmanVelThresh
		kVelShort := s.cfg.KalmanR <= 0 || s.cfg.KalmanVelThresh <= 0 || kVelPct < -s.cfg.KalmanVelThresh

		if s.cfg.BBPeriod > 0 {
			// ─── 新方案：BB 回调 + 自适应 ER 过滤 + Kalman 速度确认（复用已计算的 er/bbUpper/bbLower）───
			emaBullish := shortEMA > longEMA
			emaBearish := shortEMA < longEMA
			// 顺势入场：ER 门槛正常；逆势入场（价格在 trendEMA 反方向）：ER 门槛提高 50%
			erLongThresh := s.cfg.ERThreshold
			erShortThresh := s.cfg.ERThreshold
			if price < trendEMA { erLongThresh *= 1.5 }  // 逆势做多，需更强趋势效率
			if price > trendEMA { erShortThresh *= 1.5 } // 逆势做空，需更强趋势效率
			erLongOK := s.cfg.ERThreshold <= 0 || er > erLongThresh
			erShortOK := s.cfg.ERThreshold <= 0 || er > erShortThresh
			trendTag := func(withTrend bool) string {
				if withTrend { return "顺势" }
				return "逆势"
			}
			switch {
			case emaBullish && price <= bbLower && rsi < s.cfg.RSILongMax && erLongOK && kVelLong:
				s.p.openPos(s.cfg, dirLong, price, &s.trades)
				signal = fmt.Sprintf("\033[32mBB回调做多(%s)\033[0m (BB下轨:%.0f ER:%.2f/%.2f K:%.4f%%)",
					trendTag(price > trendEMA), bbLower, er, erLongThresh, kVelPct*100)
			case emaBearish && price >= bbUpper && rsi > s.cfg.RSIShortMin && erShortOK && kVelShort:
				s.p.openPos(s.cfg, dirShort, price, &s.trades)
				signal = fmt.Sprintf("\033[31mBB反弹做空(%s)\033[0m (BB上轨:%.0f ER:%.2f/%.2f K:%.4f%%)",
					trendTag(price < trendEMA), bbUpper, er, erShortThresh, kVelPct*100)
			}
		} else if s.prevShortEMA > 0 && s.prevLongEMA > 0 {
			// ─── 旧方案：EMA 金叉/死叉 + 动量过滤 + Kalman 速度确认 ───
			momentum := calcMomentum(s.prices, s.cfg.MomentumPeriod)
			momOK := s.cfg.MomentumPeriod == 0
			switch {
			case s.prevShortEMA <= s.prevLongEMA && noisyShortEMA > longEMA &&
				rsi < s.cfg.RSILongMax &&
				(momOK || (price > trendEMA && momentum > s.cfg.MomentumThreshold) ||
					(price <= trendEMA && momentum > s.cfg.MomentumThreshold*1.5)) && kVelLong:
				s.p.openPos(s.cfg, dirLong, price, &s.trades)
				signal = fmt.Sprintf("\033[32m金叉做多\033[0m (RSI:%.1f↑ 动量%+.3f%% K:%.4f%%)", rsi, momentum*100, kVelPct*100)
			case s.prevShortEMA >= s.prevLongEMA && noisyShortEMA < longEMA &&
				rsi > s.cfg.RSIShortMin &&
				(momOK || (price < trendEMA && momentum < -s.cfg.MomentumThreshold) ||
					(price >= trendEMA && momentum < -s.cfg.MomentumThreshold*1.5)) && kVelShort:
				s.p.openPos(s.cfg, dirShort, price, &s.trades)
				signal = fmt.Sprintf("\033[31m死叉做空\033[0m (RSI:%.1f↓ 动量%+.3f%% K:%.4f%%)", rsi, momentum*100, kVelPct*100)
			}
		}
	}
	if inCooldown && !s.p.inPosition() && signal == "" {
		remaining := s.cfg.tradeCooldown() - time.Since(s.p.lastTradeTime)
		signal = fmt.Sprintf("冷却中(%v)", remaining.Round(time.Second))
	}

	// ─── 无信号时显示：距入场的缺口（空仓）或平仓触发价（持仓）───
	ck := func(ok bool) string {
		if ok { return "\033[32m✓\033[0m" }
		return "\033[31m✗\033[0m"
	}
	if signal == "" {
		if !s.p.inPosition() && s.cfg.BBPeriod > 0 {
			// 空仓：显示下一个入场方向的缺口条件
			kVelLong := s.cfg.KalmanR <= 0 || s.cfg.KalmanVelThresh <= 0 || kVelPct > s.cfg.KalmanVelThresh
			kVelShort := s.cfg.KalmanR <= 0 || s.cfg.KalmanVelThresh <= 0 || kVelPct < -s.cfg.KalmanVelThresh
			emaBullish := shortEMA > longEMA
			emaBearish := shortEMA < longEMA
			if emaBullish {
				erThresh := s.cfg.ERThreshold
				if price < trendEMA { erThresh *= 1.5 }
				gap := price - bbLower
				gapStr := fmt.Sprintf("\033[33m↓$%.0f\033[0m", gap)
				if gap <= 0 { gapStr = fmt.Sprintf("\033[32m✓$%.0f\033[0m", bbLower) }
				signal = fmt.Sprintf("等多 价%s RSI:%.1f<%g%s ER:%.2f>%.2f%s K%s",
					gapStr, rsi, s.cfg.RSILongMax, ck(rsi < s.cfg.RSILongMax),
					er, erThresh, ck(er > erThresh), ck(kVelLong))
			} else if emaBearish {
				erThresh := s.cfg.ERThreshold
				if price > trendEMA { erThresh *= 1.5 }
				gap := bbUpper - price
				gapStr := fmt.Sprintf("\033[33m↑$%.0f\033[0m", gap)
				if gap <= 0 { gapStr = fmt.Sprintf("\033[32m✓$%.0f\033[0m", bbUpper) }
				signal = fmt.Sprintf("等空 价%s RSI:%.1f>%g%s ER:%.2f>%.2f%s K%s",
					gapStr, rsi, s.cfg.RSIShortMin, ck(rsi > s.cfg.RSIShortMin),
					er, erThresh, ck(er > erThresh), ck(kVelShort))
			}
		} else if s.p.inPosition() {
			// 持仓：显示止损/止盈/RSI平仓的触发价
			pct := s.p.positionPct(price)
			var slPrice, tpPrice float64
			if s.p.direction == dirLong {
				slPrice = s.p.entryPrice * (1 - s.cfg.StopLoss)
				tpPrice = s.p.entryPrice * (1 + s.cfg.TakeProfit)
				signal = fmt.Sprintf("持多 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) RSI出:%.1f>%.0f%s 当前%+.3f%%",
					slPrice, price-slPrice,
					tpPrice, tpPrice-price,
					rsi, s.cfg.RSIExitLong, ck(rsi > s.cfg.RSIExitLong && pct > 0),
					pct*100)
			} else {
				slPrice = s.p.entryPrice * (1 + s.cfg.StopLoss)
				tpPrice = s.p.entryPrice * (1 - s.cfg.TakeProfit)
				signal = fmt.Sprintf("持空 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) RSI出:%.1f<%.0f%s 当前%+.3f%%",
					slPrice, slPrice-price,
					tpPrice, price-tpPrice,
					rsi, s.cfg.RSIExitShort, ck(rsi < s.cfg.RSIExitShort && pct > 0),
					pct*100)
			}
		}
	}

	s.prevShortEMA = shortEMA
	s.prevLongEMA = longEMA
	s.printStatus(price, endTime, indicators, signal)
}
