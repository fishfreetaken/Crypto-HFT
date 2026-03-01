package tradelib

import (
	"fmt"
	"time"
)

// onPriceEMA EMA均值回归策略（默认策略）：
//
// 原理：EMA 定短期动量方向，BB 回调定入场时机，ER 过滤震荡市。
//   - emaBullish + 价格触达BB下轨 + ER>阈值 → 做多（均值回归）
//   - emaBearish + 价格触达BB上轨 + ER>阈值 → 做空（均值回归）
//   - 逆势方向（价格在trendEMA反侧）ER阈值×1.5，更严格过滤
func (s *Strategy) onPriceEMA(tick Tick, endTime time.Time) {
	price := tick.Prc
	s.feedPrice(price)

	// Kalman 滤波（预热期间也运行，积累状态估计）
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
			fmt.Printf("[%s] [%-4s] 数据采集中 $%.2f (%d/%d)\n",
				time.Now().Format("15:04:05"), s.Cfg.Name, price, s.priceBuf.count, s.warmupNeed)
		}
		return
	}

	// 使用状态化算子 (O(1))，消除滞后和性能瓶颈
	shortEMA := s.emaShort.Value()
	longEMA := s.emaLong.Value()
	trendEMA := s.emaTrend.Value()
	rsi := calcRSI(s.priceBuf, s.Cfg.RSIPeriod)
	signal := ""

	// ─── 每 tick 计算全量滑动窗口指标 ───
	er := 0.0
	if s.Cfg.ERPeriod > 0 {
		er = calcER(s.priceBuf, s.Cfg.ERPeriod)
	}
	bbUpper, bbLower := 0.0, 0.0
	if s.Cfg.BBPeriod > 0 {
		bbUpper, _, bbLower = calcBollingerBands(s.priceBuf, s.Cfg.BBPeriod, s.Cfg.BBStdDev)
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
	if s.Cfg.ERPeriod > 0 {
		bbTag = fmt.Sprintf(" ER:%.2f", er)
	}
	if s.Cfg.BBPeriod > 0 {
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
	equity := s.p.totalEquity(s.Cfg, tick)
	if equity > s.p.peakEquity {
		s.p.peakEquity = equity
	}
	drawdown := (s.p.peakEquity - equity) / s.p.peakEquity

	// 波动熔断检测
	inSafety := time.Now().Before(s.safetyUntil)
	if !inSafety && s.priceBuf.count >= s.Cfg.VolatilityPeriod {
		vol := calcVolatility(s.priceBuf, s.Cfg.VolatilityPeriod)
		if vol >= s.Cfg.VolatilityThreshold && drawdown >= s.Cfg.SafetyDrawdown {
			if s.p.inPosition() {
				s.p.closePos(s.Cfg, tick, "波动熔断", &s.trades)
			}
			s.safetyUntil = time.Now().Add(s.Cfg.safetyCooldown())
			inSafety = true
			fmt.Printf("[%s] [%-4s] \033[35m⚠ 波动熔断！\033[0m 波动:%.3f%% 回撤:%.2f%% → 冷静%v\n",
				time.Now().Format("15:04:05"), s.Cfg.Name, vol*100, drawdown*100, s.Cfg.safetyCooldown())
		}
	}

	// 安全冷静期：只更新 EMA 状态，不开仓
	if inSafety {
		remaining := time.Until(s.safetyUntil).Round(time.Second)
		s.prevShortEMA = shortEMA
		s.prevLongEMA = longEMA
		s.printStatus(tick, endTime, indicators,
			fmt.Sprintf("\033[35m安全冷静(%v) 回撤:%.2f%%\033[0m", remaining, drawdown*100))
		return
	}

	inCooldown := time.Since(s.p.lastTradeTime) < s.Cfg.tradeCooldown()

	// 优先级 1：止损 / 止盈 / RSI 极值平仓（以及吊灯止损）
	if s.p.inPosition() {
		triggered, stopSignal := s.p.checkStops(s.Cfg, tick, &s.trades)
		if triggered {
			signal = stopSignal
		} else {
			pct := s.p.positionPct(tick)
			switch {
			case s.p.direction == dirLong && rsi > s.Cfg.RSIExitLong && pct > 0:
				s.p.closePos(s.Cfg, tick, "RSI超买", &s.trades)
				signal = fmt.Sprintf("RSI超买(%.1f)平多", rsi)
			case s.p.direction == dirShort && rsi < s.Cfg.RSIExitShort && pct > 0:
				s.p.closePos(s.Cfg, tick, "RSI超卖", &s.trades)
				signal = fmt.Sprintf("RSI超卖(%.1f)平空", rsi)
			}
		}
	}

	// 优先级 2：开仓信号
	if !s.p.inPosition() && !inCooldown {
		kVelLong := s.Cfg.KalmanR <= 0 || s.Cfg.KalmanVelThresh <= 0 || kVelPct > s.Cfg.KalmanVelThresh
		kVelShort := s.Cfg.KalmanR <= 0 || s.Cfg.KalmanVelThresh <= 0 || kVelPct < -s.Cfg.KalmanVelThresh

		vol := 0.0
		if s.priceBuf.count >= s.Cfg.VolatilityPeriod {
			vol = calcVolatility(s.priceBuf, s.Cfg.VolatilityPeriod)
		}

		if s.Cfg.BBPeriod > 0 {
			emaBullish := shortEMA > longEMA
			emaBearish := shortEMA < longEMA
			erLongThresh := s.Cfg.ERThreshold
			erShortThresh := s.Cfg.ERThreshold
			if price < trendEMA { erLongThresh *= 1.5 }
			if price > trendEMA { erShortThresh *= 1.5 }
			erLongOK := s.Cfg.ERThreshold <= 0 || er > erLongThresh
			erShortOK := s.Cfg.ERThreshold <= 0 || er > erShortThresh
			trendTag := func(withTrend bool) string {
				if withTrend { return "顺势" }
				return "逆势"
			}
			switch {
			case emaBullish && price <= bbLower && rsi < s.Cfg.RSILongMax && erLongOK && kVelLong:
				s.p.openPosVolAdjusted(s.Cfg, dirLong, tick, vol, &s.trades)
				signal = fmt.Sprintf("\033[32mBB回调做多(%s)\033[0m (BB下轨:%.0f ER:%.2f/%.2f K:%.4f%%)",
					trendTag(price > trendEMA), bbLower, er, erLongThresh, kVelPct*100)
			case emaBearish && price >= bbUpper && rsi > s.Cfg.RSIShortMin && erShortOK && kVelShort:
				s.p.openPosVolAdjusted(s.Cfg, dirShort, tick, vol, &s.trades)
				signal = fmt.Sprintf("\033[31mBB反弹做空(%s)\033[0m (BB上轨:%.0f ER:%.2f/%.2f K:%.4f%%)",
					trendTag(price < trendEMA), bbUpper, er, erShortThresh, kVelPct*100)
			}
		} else if s.prevShortEMA > 0 && s.prevLongEMA > 0 {
			momentum := calcMomentum(s.priceBuf, s.Cfg.MomentumPeriod)
			momOK := s.Cfg.MomentumPeriod == 0
			erOK := s.Cfg.ERThreshold <= 0 || er > s.Cfg.ERThreshold
			switch {
			case s.prevShortEMA <= s.prevLongEMA && shortEMA > longEMA &&
				rsi < s.Cfg.RSILongMax && erOK &&
				(momOK || (price > trendEMA && momentum > s.Cfg.MomentumThreshold) ||
					(price <= trendEMA && momentum > s.Cfg.MomentumThreshold*1.5)) && kVelLong:
				s.p.openPosVolAdjusted(s.Cfg, dirLong, tick, vol, &s.trades)
				signal = fmt.Sprintf("\033[32m金叉做多\033[0m (ER:%.2f 动量%+.3f%% K:%.4f%%)", er, momentum*100, kVelPct*100)
			case s.prevShortEMA >= s.prevLongEMA && shortEMA < longEMA &&
				rsi > s.Cfg.RSIShortMin && erOK &&
				(momOK || (price < trendEMA && momentum < -s.Cfg.MomentumThreshold) ||
					(price >= trendEMA && momentum < -s.Cfg.MomentumThreshold*1.5)) && kVelShort:
				s.p.openPosVolAdjusted(s.Cfg, dirShort, tick, vol, &s.trades)
				signal = fmt.Sprintf("\033[31m死叉做空\033[0m (ER:%.2f 动量%+.3f%% K:%.4f%%)", er, momentum*100, kVelPct*100)
			}
		}
	}
	if inCooldown && !s.p.inPosition() && signal == "" {
		remaining := s.Cfg.tradeCooldown() - time.Since(s.p.lastTradeTime)
		signal = fmt.Sprintf("冷却中(%v)", remaining.Round(time.Second))
	}

	// ─── 无信号时显示：距入场的缺口（空仓）或平仓触发价（持仓）───
	ck := func(ok bool) string {
		if ok { return "\033[32m✓\033[0m" }
		return "\033[31m✗\033[0m"
	}
	if signal == "" {
		if !s.p.inPosition() && s.Cfg.BBPeriod > 0 {
			kVelLong := s.Cfg.KalmanR <= 0 || s.Cfg.KalmanVelThresh <= 0 || kVelPct > s.Cfg.KalmanVelThresh
			kVelShort := s.Cfg.KalmanR <= 0 || s.Cfg.KalmanVelThresh <= 0 || kVelPct < -s.Cfg.KalmanVelThresh
			emaBullish := shortEMA > longEMA
			emaBearish := shortEMA < longEMA
			if emaBullish {
				erThresh := s.Cfg.ERThreshold
				if price < trendEMA { erThresh *= 1.5 }
				gap := price - bbLower
				gapStr := fmt.Sprintf("\033[33m↓$%.0f\033[0m", gap)
				if gap <= 0 { gapStr = fmt.Sprintf("\033[32m✓$%.0f\033[0m", bbLower) }
				signal = fmt.Sprintf("等多 价%s RSI:%.1f<%g%s ER:%.2f>%.2f%s K%s",
					gapStr, rsi, s.Cfg.RSILongMax, ck(rsi < s.Cfg.RSILongMax),
					er, erThresh, ck(er > erThresh), ck(kVelLong))
			} else if emaBearish {
				erThresh := s.Cfg.ERThreshold
				if price > trendEMA { erThresh *= 1.5 }
				gap := bbUpper - price
				gapStr := fmt.Sprintf("\033[33m↑$%.0f\033[0m", gap)
				if gap <= 0 { gapStr = fmt.Sprintf("\033[32m✓$%.0f\033[0m", bbUpper) }
				signal = fmt.Sprintf("等空 价%s RSI:%.1f>%g%s ER:%.2f>%.2f%s K%s",
					gapStr, rsi, s.Cfg.RSIShortMin, ck(rsi > s.Cfg.RSIShortMin),
					er, erThresh, ck(er > erThresh), ck(kVelShort))
			}
		} else if !s.p.inPosition() && s.Cfg.BBPeriod == 0 {
			kDir := "↑多↓空"
			if kVelPct > s.Cfg.KalmanVelThresh {
				kDir = "\033[32m↑充能\033[0m"
			} else if kVelPct < -s.Cfg.KalmanVelThresh {
				kDir = "\033[31m↓泄能\033[0m"
			}
			erThresh := s.Cfg.ERThreshold
			signal = fmt.Sprintf("等金叉/死叉 ER:%.2f>%.2f%s K方向:%s",
				er, erThresh, ck(er > erThresh), kDir)
		} else if s.p.inPosition() {
			pct := s.p.positionPct(tick)
			var slPrice, tpPrice float64
			if s.p.direction == dirLong {
				slPrice = s.p.entryPrice * (1 - s.Cfg.StopLoss)
				tpPrice = s.p.entryPrice * (1 + s.Cfg.TakeProfit)
				signal = fmt.Sprintf("持多 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) RSI出:%.1f>%.0f%s 当前%+.3f%%",
					slPrice, price-slPrice,
					tpPrice, tpPrice-price,
					rsi, s.Cfg.RSIExitLong, ck(rsi > s.Cfg.RSIExitLong && pct > 0),
					pct*100)
			} else {
				slPrice = s.p.entryPrice * (1 + s.Cfg.StopLoss)
				tpPrice = s.p.entryPrice * (1 - s.Cfg.TakeProfit)
				signal = fmt.Sprintf("持空 止损$%.0f(差$%.0f) 止盈$%.0f(差$%.0f) RSI出:%.1f<%.0f%s 当前%+.3f%%",
					slPrice, slPrice-price,
					tpPrice, price-tpPrice,
					rsi, s.Cfg.RSIExitShort, ck(rsi < s.Cfg.RSIExitShort && pct > 0),
					pct*100)
			}
		}
	}

	s.prevShortEMA = shortEMA
	s.prevLongEMA = longEMA
	s.printStatus(tick, endTime, indicators, signal)
}
