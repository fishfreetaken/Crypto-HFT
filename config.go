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
	Disabled       bool    `json:"disabled"` // true = 不执行且不输出；false（默认）= 正常运行
	InitialCapital float64 `json:"initial_capital"`

	Leverage     float64 `json:"leverage"`
	TradeFee     float64 `json:"trade_fee"`
	StopLoss     float64 `json:"stop_loss"`
	TakeProfit   float64 `json:"take_profit"`
	TrailingStop float64 `json:"trailing_stop"` // 动态追踪止损（如 0.015 表示价格从高点回落1.5%就平仓）

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
	SqueezeBBPeriod   int     `json:"squeeze_bb_period"`    // BB 计算窗口
	SqueezeBBStdDev   float64 `json:"squeeze_bb_std_dev"`   // BB σ 倍数
	SqueezeATRPeriod  int     `json:"squeeze_atr_period"`   // ATR / KC 计算窗口
	SqueezeKCMult     float64 `json:"squeeze_kc_mult"`      // KC = EMA ± mult×ATR
	SqueezeConfirmER  float64 `json:"squeeze_confirm_er"`   // 突破确认 ER 门槛（防假突破）
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

// defaultStrategies 内置五种预设优化策略，覆盖长短线与各种行情体制
func defaultStrategies() []Config {
	return []Config{
		{
			Name:                "K滤波趋势",
			InitialCapital:      1000.0,
			Leverage:            3.0,
			TradeFee:            0.0005,
			StopLoss:            0.015,
			TakeProfit:          0.045,
			CooldownSec:         300,
			EMAShort:            15,
			EMALong:             45,
			TrendPeriod:         100,
			RSIPeriod:           14,
			RSILongMax:          65.0,
			RSIShortMin:         35.0,
			RSIExitLong:         80.0,
			RSIExitShort:        20.0,
			VolatilityPeriod:    12,
			VolatilityThreshold: 0.020,
			SafetyDrawdown:      0.10,
			SafetyCooldownSec:   1800,
			BBPeriod:            0,
			BBStdDev:            2.0,
			ERPeriod:            14,
			ERThreshold:         0.25,
			KalmanQPos:          0.0001,
			KalmanQVel:          0.00002,
			KalmanR:             0.00050,
			KalmanVelThresh:     0.00005,
		},
		{
			Name:                "极值均值回归",
			InitialCapital:      1000.0,
			Leverage:            5.0,
			TradeFee:            0.0005,
			StopLoss:            0.012,
			TakeProfit:          0.025,
			CooldownSec:         180,
			EMAShort:            10,
			EMALong:             30,
			TrendPeriod:         50,
			RSIPeriod:           14,
			RSILongMax:          45.0,
			RSIShortMin:         55.0,
			RSIExitLong:         70.0,
			RSIExitShort:        30.0,
			VolatilityPeriod:    10,
			VolatilityThreshold: 0.015,
			SafetyDrawdown:      0.08,
			SafetyCooldownSec:   900,
			BBPeriod:            20,
			BBStdDev:            2.2,
			ERPeriod:            14,
			ERThreshold:         0.15,
			KalmanQPos:          0.0002,
			KalmanQVel:          0.00005,
			KalmanR:             0.00020,
			KalmanVelThresh:     0,
		},
		{
			Name:              "挤压突破",
			StrategyType:      "squeeze_breakout",
			InitialCapital:    1000.0,
			Leverage:          8.0,
			TradeFee:          0.0005,
			StopLoss:          0.008,
			TakeProfit:        0.024,
			CooldownSec:       60,
			SqueezeBBPeriod:   20,
			SqueezeBBStdDev:   2.0,
			SqueezeATRPeriod:  20,
			SqueezeKCMult:     1.5,
			SqueezeConfirmER:  0.35,
			SqueezeMaxHoldSec: 900,
			SqueezeBBWidthPct: 0.0005,
			KalmanQPos:        0.0003,
			KalmanQVel:        0.00010,
			KalmanR:           0.00005,
			KalmanVelThresh:   0.00003,
		},
		{
			Name:            "死猫做空",
			StrategyType:    "dead_cat_bounce",
			InitialCapital:  1000.0,
			Leverage:        5.0,
			TradeFee:        0.0005,
			StopLoss:        0.010,
			TakeProfit:      0.025,
			CooldownSec:     120,
			DCBDropPeriod:   480,
			DCBDropMinPct:   0.0060,
			DCBBounceMinPct: 0.0015,
			DCBBounceMaxPct: 0.382,
			DCBConfirmTicks: 3,
			DCBMaxHoldSec:   1200,
			KalmanQPos:      0.0003,
			KalmanQVel:      0.00010,
			KalmanR:         0.00010,
			KalmanVelThresh: 0.00002,
		},
		{
			Name:               "瀑布连环",
			StrategyType:       "waterfall",
			InitialCapital:     1000.0,
			Leverage:           10.0,
			TradeFee:           0.0005,
			StopLoss:           0.005,
			TakeProfit:         0.015,
			CooldownSec:        30,
			WFConsecutiveTicks: 5,
			WFMinVelPct:        0.00025,
			WFMaxHoldSec:       180,
			KalmanQPos:         0.0004,
			KalmanQVel:         0.00010,
			KalmanR:            0.00002,
			KalmanVelThresh:    0.00005,
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
