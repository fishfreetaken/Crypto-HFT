package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// ===== 全局配置（所有策略共享）=====

type AppConfig struct {
	Symbol            string   `json:"symbol"`
	SampleIntervalSec int      `json:"sample_interval_sec"`
	TradeDurationMin  int      `json:"trade_duration_min"`
	DataDir           string   `json:"data_dir"`
	LookbackHours     int      `json:"lookback_hours"`
	Strategies        []Config `json:"strategies"`
}

func (a *AppConfig) sampleInterval() time.Duration {
	return time.Duration(a.SampleIntervalSec) * time.Second
}

func (a *AppConfig) tradeDuration() time.Duration {
	return time.Duration(a.TradeDurationMin) * time.Minute
}

// ===== 策略配置（每个策略独立）=====

type Config struct {
	Name           string  `json:"name"`
	Disabled       bool    `json:"disabled"`        // true = 不执行且不输出；false（默认）= 正常运行
	InitialCapital float64 `json:"initial_capital"`

	Leverage   float64 `json:"leverage"`
	TradeFee   float64 `json:"trade_fee"`
	StopLoss   float64 `json:"stop_loss"`
	TakeProfit float64 `json:"take_profit"`

	CooldownSec int `json:"cooldown_sec"`
	EMAShort    int `json:"ema_short"`
	EMALong     int `json:"ema_long"`
	TrendPeriod int `json:"trend_period"`
	RSIPeriod   int `json:"rsi_period"`

	RSILongMax   float64 `json:"rsi_long_max"`
	RSIShortMin  float64 `json:"rsi_short_min"`
	RSIExitLong  float64 `json:"rsi_exit_long"`
	RSIExitShort float64 `json:"rsi_exit_short"`

	VolatilityPeriod    int     `json:"volatility_period"`
	VolatilityThreshold float64 `json:"volatility_threshold"`
	SafetyDrawdown      float64 `json:"safety_drawdown"`
	SafetyCooldownSec   int     `json:"safety_cooldown_sec"`

	// BB+ER 入场优化：EMA 定趋势方向，BB 定回调入场时机，ER 过滤无效率震荡行情
	// BBPeriod=0 时退回旧版 EMA 金叉逻辑（向后兼容）
	BBPeriod    int     `json:"bb_period"`
	BBStdDev    float64 `json:"bb_std_dev"`
	ERPeriod    int     `json:"er_period"`
	ERThreshold float64 `json:"er_threshold"`

	// EMA 动量过滤（旧方案，BBPeriod=0 时生效）
	MomentumPeriod    int     `json:"momentum_period"`
	MomentumThreshold float64 `json:"momentum_threshold"`

	// 卡尔曼滤波参数（0 = 禁用 Kalman，各策略独立配置）
	KalmanQPos      float64 `json:"kalman_q_pos"`      // 价格过程噪声（占价格比率）
	KalmanQVel      float64 `json:"kalman_q_vel"`      // 速度过程噪声（占价格比率）
	KalmanR         float64 `json:"kalman_r"`          // 测量噪声（占价格比率）；0=禁用
	KalmanVelThresh float64 `json:"kalman_vel_thresh"` // 最小速度（EMA策略用）；0=不限速

	// trend_prob 策略专用参数（strategy_type = "trend_prob" 时生效）
	StrategyType      string  `json:"strategy_type"`
	TrendLookback     int     `json:"trend_lookback"`
	NoiseWeight       float64 `json:"noise_weight"`
	TargetMin         float64 `json:"target_min"`
	TargetMax         float64 `json:"target_max"`
	DecaySec          int     `json:"decay_sec"`
	DecayExp          float64 `json:"decay_exp"`
	PerfDecayWeight   float64 `json:"perf_decay_weight"`
	ProfitBoostWeight float64 `json:"profit_boost_weight"`

	// 挤压突破策略（strategy_type = "squeeze_breakout"）
	// 检测 BB 被 Keltner 通道包裹（盘整蓄力），突破时高杠杆跟入
	SqueezeBBPeriod   int     `json:"squeeze_bb_period"`   // BB 计算窗口
	SqueezeBBStdDev   float64 `json:"squeeze_bb_std_dev"`  // BB σ 倍数
	SqueezeATRPeriod  int     `json:"squeeze_atr_period"`  // ATR / KC 计算窗口
	SqueezeKCMult     float64 `json:"squeeze_kc_mult"`     // KC = EMA ± mult×ATR
	SqueezeConfirmER  float64 `json:"squeeze_confirm_er"`  // 突破确认 ER 门槛（防假突破）
	SqueezeMaxHoldSec int     `json:"squeeze_max_hold_sec"` // 最长持仓秒数（超时强平）
	SqueezeBBWidthPct float64 `json:"squeeze_bb_width_pct"` // >0 时：用 BB 宽度/价格 < 阈值 判挤压（适合 5s 采样）；=0 时：用 BB⊂KC 经典判法

	// 死猫反弹策略（strategy_type = "dead_cat_bounce"）
	// 急跌后弱反弹（≤DCBBounceMaxPct 回撤）结束时做空，等待续跌
	DCBDropPeriod   int     `json:"dcb_drop_period"`    // 下跌回溯窗口(tick)
	DCBDropMinPct   float64 `json:"dcb_drop_min_pct"`   // 有效急跌最小幅度
	DCBBounceMinPct float64 `json:"dcb_bounce_min_pct"` // 反弹需达到的最小幅度（相对低点）
	DCBBounceMaxPct float64 `json:"dcb_bounce_max_pct"` // 反弹超过总跌幅此比例则非死猫
	DCBConfirmTicks int     `json:"dcb_confirm_ticks"`  // 确认反弹结束所需连续下跌tick数
	DCBMaxHoldSec   int     `json:"dcb_max_hold_sec"`   // 超时强平秒数

	// 瀑布加速策略（strategy_type = "waterfall"）
	// 连续N个tick均匀大幅下跌（止损连锁触发信号），高杠杆追入做空
	WFConsecutiveTicks int     `json:"wf_consecutive_ticks"` // 需要连续下跌的tick数
	WFMinVelPct        float64 `json:"wf_min_vel_pct"`       // 单tick平均最小下跌%
	WFMaxHoldSec       int     `json:"wf_max_hold_sec"`      // 超时强平秒数
}

func (c *Config) tradeCooldown() time.Duration {
	return time.Duration(c.CooldownSec) * time.Second
}

func (c *Config) safetyCooldown() time.Duration {
	return time.Duration(c.SafetyCooldownSec) * time.Second
}

func defaultAppConfig() AppConfig {
	return AppConfig{
		Symbol:            "BTCUSDT",
		SampleIntervalSec: 30,
		TradeDurationMin:  480,
		DataDir:           "data",
		LookbackHours:     30,
		Strategies:        defaultStrategies(),
	}
}

// defaultStrategies 内置三种预设策略：保守 / 稳定 / 激进
func defaultStrategies() []Config {
	return []Config{
		{
			// 保守：BB(1.2σ/12期)均值回归 + ER(12)>0.18。KalmanVelThresh=0：BB回归信号本身逆短期动量，
			// 若要求做空时速度向下会在价格反弹到上轨时被阻断（速度此时为正）
			Name:                "保守",
			InitialCapital:      300.0,
			Leverage:            1.5,
			TradeFee:            0.0005,
			StopLoss:            0.004,
			TakeProfit:          0.010,
			CooldownSec:         120,
			EMAShort:            8,
			EMALong:             20,
			TrendPeriod:         35,
			RSIPeriod:           14,
			RSILongMax:          62.0,
			RSIShortMin:         38.0,
			RSIExitLong:         75.0,
			RSIExitShort:        25.0,
			VolatilityPeriod:    5,
			VolatilityThreshold: 0.006,
			SafetyDrawdown:      0.03,
			SafetyCooldownSec:   600,
			NoiseWeight:         0.05,
			BBPeriod:            12,
			BBStdDev:            1.2,
			ERPeriod:            12,
			ERThreshold:         0.18,
			KalmanQPos:          0.0002,
			KalmanQVel:          0.00005,
			KalmanR:             0.00030,
			KalmanVelThresh:     0,
		},
		{
			// 稳定：BB(1.5σ/10期) + ER(12)>0.20，冷却90s，KalmanVelThresh=0（同保守理由）
			Name:                "稳定",
			InitialCapital:      400.0,
			Leverage:            3.0,
			TradeFee:            0.0005,
			StopLoss:            0.005,
			TakeProfit:          0.012,
			CooldownSec:         90,
			EMAShort:            5,
			EMALong:             15,
			TrendPeriod:         25,
			RSIPeriod:           9,
			RSILongMax:          65.0,
			RSIShortMin:         35.0,
			RSIExitLong:         78.0,
			RSIExitShort:        22.0,
			VolatilityPeriod:    5,
			VolatilityThreshold: 0.012,
			SafetyDrawdown:      0.06,
			SafetyCooldownSec:   300,
			NoiseWeight:         0.08,
			BBPeriod:            10,
			BBStdDev:            1.5,
			ERPeriod:            12,
			ERThreshold:         0.20,
			KalmanQPos:          0.0002,
			KalmanQVel:          0.00005,
			KalmanR:             0.00020,
			KalmanVelThresh:     0,
		},
		{
			// 激进：BB(2.0σ/10期) + ER(12)>0.25，冷却60s，KalmanVelThresh=0（同保守理由）
			Name:                "激进",
			InitialCapital:      300.0,
			Leverage:            5.0,
			TradeFee:            0.0005,
			StopLoss:            0.006,
			TakeProfit:          0.018,
			CooldownSec:         60,
			EMAShort:            3,
			EMALong:             10,
			TrendPeriod:         20,
			RSIPeriod:           7,
			RSILongMax:          70.0,
			RSIShortMin:         30.0,
			RSIExitLong:         85.0,
			RSIExitShort:        15.0,
			VolatilityPeriod:    5,
			VolatilityThreshold: 0.015,
			SafetyDrawdown:      0.08,
			SafetyCooldownSec:   180,
			NoiseWeight:         0.12,
			BBPeriod:            10,
			BBStdDev:            2.0,
			ERPeriod:            12,
			ERThreshold:         0.25,
			KalmanQPos:          0.0003,
			KalmanQVel:          0.00008,
			KalmanR:             0.00015,
			KalmanVelThresh:     0,
		},
		{
			// 挤压突破(30x)：双重确认（ER>0.42 + Kalman速度方向），极端压缩才入场
			// 止损0.15%价格(=4.5%权益)，止盈1.0%(=30%权益)，盈亏比6:1，保本胜率14%
			// 5s采样：SqueezeBBPeriod=6(30s窗口) + SqueezeBBWidthPct=0.02%替代BB⊂KC判法
			// 原因：5s ATR≈$3，KC宽≈$8，BB(20期)≈$20，BB永远无法进入KC → 改用BB绝对宽度阈值
			// ER(8)替代ER(14)：14期ER被13个震荡tick稀释，实测0.15-0.20<0.42；8期窗口突破tick ER≈0.4-0.8
			// KalmanVelThresh=0：挤压期速度趋近0，第一个突破tick Kalman未来得及更新，用ER过滤即可
			Name:              "挤压突破",
			StrategyType:      "squeeze_breakout",
			InitialCapital:    300.0,
			Leverage:          30.0,
			TradeFee:          0.0005,
			StopLoss:          0.0015,
			TakeProfit:        0.010,
			CooldownSec:       20,
			SqueezeBBPeriod:   6,
			SqueezeBBStdDev:   2.0,
			SqueezeATRPeriod:  8,     // ER窗口8tick=40s，避免前期震荡稀释突破信号
			SqueezeKCMult:     1.4,
			SqueezeConfirmER:  0.25,  // 8tick窗口下突破ER ≈ drop/(7×flat+drop)，阈值0.25过滤微弱突破
			SqueezeMaxHoldSec: 240,
			SqueezeBBWidthPct: 0.0002,
			KalmanQPos:        0.0003,
			KalmanQVel:        0.0001,
			KalmanR:           0.00005,
			KalmanVelThresh:   0, // 不检测速度方向，ER确认即可
		},
		{
			// 死猫做空(15x)：急跌后弱反弹(≤35%回撤)结束时做空，等待续跌
			// 止损0.2%价格(=3%权益)；止盈0.8%(=12%权益)；费用3%；净+9%/-6%；打平胜率40%
			// 窗口360tick(=30分钟@5s)：5s采样10分钟仅0.04%跌幅不够，30分钟可捕获0.4%+大跌
			Name:            "死猫做空",
			StrategyType:    "dead_cat_bounce",
			InitialCapital:  1000.0,
			Leverage:        15.0,
			TradeFee:        0.0005,
			StopLoss:        0.0020,
			TakeProfit:      0.0080,
			CooldownSec:     30,
			DCBDropPeriod:   360,    // 30min窗口，捕获跨越多个震荡的大趋势下跌
			DCBDropMinPct:   0.0015, // 0.15%=约$100@$66k，适配当前5s波动率
			DCBBounceMinPct: 0.001,
			DCBBounceMaxPct: 0.35,
			DCBConfirmTicks: 2, // 减少至2，加快确认速度
			DCBMaxHoldSec:   180,
			KalmanQPos:      0.0003,
			KalmanQVel:      0.00010,
			KalmanR:         0.00005,
			KalmanVelThresh: 0.00002,
		},
		{
			// 趋势押注：8x 杠杆（降自29x），紧止损(0.3%价格=2.4%权益) + 宽目标(3-8%权益)
			// 期望值数学：费率0.8%往返，胜率50%时EV=+0.5%/笔，优于原29x必亏格局
			// Kalman：用速度替代趋势概率作为方向信号，SNR决定目标高低，VelThresh=0不限速
			Name:              "趋势押注",
			StrategyType:      "trend_prob",
			InitialCapital:    200.0,
			Leverage:          8.0,
			TradeFee:          0.0005,
			StopLoss:          0.003,
			CooldownSec:       30,
			TrendLookback:     20,
			NoiseWeight:       0.25,
			TargetMin:         0.03,
			TargetMax:         0.08,
			DecaySec:          600,
			DecayExp:          1.5,
			PerfDecayWeight:   3.0,
			ProfitBoostWeight: 1.5,
			KalmanQPos:        0.0003,
			KalmanQVel:        0.00010,
			KalmanR:           0.00005,
			KalmanVelThresh:   0,
		},
	}
}

func loadAppConfig(path string) (AppConfig, error) {
	cfg := defaultAppConfig()
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return cfg, err
	}
	if len(cfg.Strategies) == 0 {
		cfg.Strategies = defaultStrategies()
	}
	return cfg, nil
}

var appCfg AppConfig

// watchConfig 每 2 分钟检查配置文件修改时间，有变化时通过 reloadCh 发送新配置。
func watchConfig(ctx context.Context, path string, reloadCh chan<- AppConfig) {
	const interval = 2 * time.Minute
	var lastMod time.Time
	if info, err := os.Stat(path); err == nil {
		lastMod = info.ModTime()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if !info.ModTime().After(lastMod) {
				continue
			}
			newCfg, err := loadAppConfig(path)
			if err != nil {
				log.Printf("[配置监听] 重载失败: %v\n", err)
				continue
			}
			lastMod = info.ModTime()
			select {
			case reloadCh <- newCfg:
				log.Printf("[配置监听] 检测到变化，已排队重载\n")
			default:
			}
		}
	}
}

func readPriceFile(path string) []float64 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	type record struct {
		Price float64 `json:"price"`
	}
	var prices []float64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec record
		if json.Unmarshal(scanner.Bytes(), &rec) == nil && rec.Price > 0 {
			prices = append(prices, rec.Price)
		}
	}
	return prices
}

func loadHistoricalPricesFromDir(dataDir string, lookbackHours int) []float64 {
	now := time.Now()
	start := now.Add(-time.Duration(lookbackHours) * time.Hour).Truncate(time.Hour)
	var all []float64
	for t := start; !t.After(now); t = t.Add(time.Hour) {
		path := filepath.Join(dataDir, t.Format("2006-01-02"), t.Format("15")+".jsonl")
		all = append(all, readPriceFile(path)...)
	}
	return all
}
