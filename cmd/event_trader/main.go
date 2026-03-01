package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"biance/tradelib"
)

type EventConfig struct {
	InitialCapital  float64  `json:"initial_capital"`
	Leverage        float64  `json:"leverage"`
	TradeFee        float64  `json:"trade_fee"`
	StopLoss        float64  `json:"stop_loss"`
	TakeProfit      float64  `json:"take_profit"`
	TrailingStop    float64  `json:"trailing_stop"`
	MaxHoldMinutes  int      `json:"max_hold_minutes"`
	DataDir         string   `json:"data_dir"`
	ScanIntervalSec int      `json:"scan_interval_sec"`
	KeywordsBullish []string `json:"keywords_bullish"`
	KeywordsBearish []string `json:"keywords_bearish"`
}

type TradePosition struct {
	Active       bool
	Direction    int // 1 = Long, -1 = Short
	EntryPrice   float64
	ExtremePrice float64 // For trailing stop
	EntryTime    time.Time
	Reason       string
	Margin       float64
	Leverage     float64
	Cash         float64
}

var cfg EventConfig
var pos TradePosition
var seenNews = make(map[string]bool)

func loadConfig(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("无法读取配置: %v", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Fatalf("配置解析失败: %v", err)
	}
	pos.Cash = cfg.InitialCapital
}

func checkStops(tick tradelib.Tick) {
	if !pos.Active {
		return
	}

	currentPrice := tick.Prc
	if pos.Direction == 1 {
		currentPrice = tick.Bid1
		if currentPrice > pos.ExtremePrice {
			pos.ExtremePrice = currentPrice
		}
	} else {
		currentPrice = tick.Ask1
		if currentPrice < pos.ExtremePrice || pos.ExtremePrice == 0 {
			pos.ExtremePrice = currentPrice
		}
	}

	pnlPct := 0.0
	if pos.Direction == 1 {
		pnlPct = (currentPrice - pos.EntryPrice) / pos.EntryPrice
	} else {
		pnlPct = (pos.EntryPrice - currentPrice) / pos.EntryPrice
	}

	closeReason := ""

	// 止盈止损
	if pnlPct <= -cfg.StopLoss {
		closeReason = fmt.Sprintf("触及止损(%.2f%%)", pnlPct*100)
	} else if pnlPct >= cfg.TakeProfit {
		closeReason = fmt.Sprintf("强制止盈(%.2f%%)", pnlPct*100)
	}

	// 跟踪止损
	if closeReason == "" && cfg.TrailingStop > 0 {
		drawdown := 0.0
		if pos.Direction == 1 {
			drawdown = (pos.ExtremePrice - currentPrice) / pos.ExtremePrice
		} else {
			drawdown = (currentPrice - pos.ExtremePrice) / pos.ExtremePrice
		}
		if pnlPct > cfg.TrailingStop && drawdown >= cfg.TrailingStop {
			closeReason = fmt.Sprintf("跟踪回撤平仓(%.2f%%)", drawdown*100)
		}
	}

	// 超时平仓
	holdMins := time.Since(pos.EntryTime).Minutes()
	if closeReason == "" && cfg.MaxHoldMinutes > 0 && int(holdMins) >= cfg.MaxHoldMinutes {
		closeReason = fmt.Sprintf("事件发酵超时(%d分钟)", int(holdMins))
	}

	if closeReason != "" {
		gross := pos.Margin * pnlPct * pos.Leverage
		fee := pos.Margin * pos.Leverage * cfg.TradeFee
		net := gross - fee
		pos.Cash = pos.Cash + net

		dirStr := "做多"
		if pos.Direction == -1 {
			dirStr = "做空"
		}
		fmt.Printf("\n[%s] \033[33m[平仓:%s]\033[0m %s 平仓价:$%.2f 净盈亏:$%+.2f 剩余资金:$%.2f\n",
			time.Now().Format("15:04:05"), closeReason, dirStr, currentPrice, net, pos.Cash)

		pos.Active = false
		pos.Margin = 0
	}
}

func openPosition(dir int, price float64, reason string) {
	if pos.Active {
		return
	}
	pos.Active = true
	pos.Direction = dir
	pos.EntryPrice = price
	pos.ExtremePrice = price
	pos.EntryTime = time.Now()
	pos.Reason = reason
	pos.Leverage = cfg.Leverage

	openFee := pos.Cash * pos.Leverage * cfg.TradeFee
	pos.Margin = pos.Cash - openFee
	pos.Cash = 0

	dirStr := "\033[32m做多(Long)\033[0m"
	if dir == -1 {
		dirStr = "\033[31m做空(Short)\033[0m"
	}
	fmt.Printf("\n[%s] 🚨 \033[35m[事件触发入场]\033[0m\n", time.Now().Format("15:04:05"))
	fmt.Printf("方向: %s | 价格: $%.2f | 杠杆: %.1fx\n触发因素: %s\n", dirStr, price, pos.Leverage, reason)
}

func scanNews(tick tradelib.Tick, isFirstBoot bool) {
	client := http.Client{Timeout: 5 * time.Second}
	url := "https://www.binance.com/bapi/composite/v1/public/cms/article/catalog/list/query?catalogId=48&pageNo=1&pageSize=15"
	resp, err := client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var res struct {
		Data struct {
			Articles []struct {
				Title string `json:"title"`
				Code  string `json:"code"`
			} `json:"articles"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return
	}

	for _, art := range res.Data.Articles {
		if seenNews[art.Code] {
			continue
		}
		seenNews[art.Code] = true

		if isFirstBoot {
			continue // 初始化启动时仅记录索引，不作为开仓信号
		}

		titleLower := strings.ToLower(art.Title)
		isBearish := false
		for _, kw := range cfg.KeywordsBearish {
			if strings.Contains(titleLower, kw) {
				openPosition(-1, tick.Bid1, fmt.Sprintf("🚨 捕获看空词[%s]: %s", kw, art.Title))
				isBearish = true
				break
			}
		}

		if !isBearish {
			for _, kw := range cfg.KeywordsBullish {
				if strings.Contains(titleLower, kw) {
					openPosition(1, tick.Ask1, fmt.Sprintf("🚀 捕获看多词[%s]: %s", kw, art.Title))
					break
				}
			}
		}
	}
}

func main() {
	fmt.Println("========== 📰 事件驱动策略引擎 (Independent Event Trader) ==========")

	// 工作目录修正
	if _, err := os.Stat("config.json"); os.IsNotExist(err) {
		if _, err := os.Stat("cmd/event_trader/config.json"); err == nil {
			os.Chdir("cmd/event_trader")
		} else if _, err := os.Stat("../../cmd/event_trader/config.json"); err == nil {
			os.Chdir("../../cmd/event_trader")
		}
	}

	loadConfig("config.json")
	fmt.Printf("独立资金池: $%.2f | 杠杆: %.1fx | 持仓时效: %d 分钟\n", cfg.InitialCapital, cfg.Leverage, cfg.MaxHoldMinutes)
	fmt.Printf("扫描频率: %d 秒\n", cfg.ScanIntervalSec)
	fmt.Printf("看多监控词汇: %v\n", cfg.KeywordsBullish)
	fmt.Printf("看空监控词汇: %v\n", cfg.KeywordsBearish)
	fmt.Println("==================================================================")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// 高频刷价用于止损
	priceTicker := time.NewTicker(2 * time.Second)
	newsTicker := time.NewTicker(time.Duration(cfg.ScanIntervalSec) * time.Second)

	fmt.Print("正在初始化过往资讯防火墙 (防止启动时买入老事件)... ")
	scanNews(tradelib.Tick{}, true)
	fmt.Println("OK！进入侦听状态。")

	lastPrintTime := time.Time{}

	for {
		select {
		case <-sig:
			fmt.Println("\n收到退出信号，终止事件交易终端。")
			return

		case <-newsTicker.C:
			tick, err := tradelib.FetchPrice(cfg.DataDir)
			if err == nil {
				scanNews(tick, false)
			}

		case <-priceTicker.C:
			tick, err := tradelib.FetchPrice(cfg.DataDir)
			if err != nil {
				continue
			}
			checkStops(tick)

			now := time.Now()
			if pos.Active {
				pnlPct := 0.0
				curPrc := tick.Prc
				if pos.Direction == 1 {
					curPrc = tick.Bid1
					pnlPct = (curPrc - pos.EntryPrice) / pos.EntryPrice
				} else {
					curPrc = tick.Ask1
					pnlPct = (pos.EntryPrice - curPrc) / pos.EntryPrice
				}
				netPnl := pos.Margin * pnlPct * pos.Leverage

				dirStr := "多头"
				if pos.Direction == -1 {
					dirStr = "空头"
				}

				fmt.Printf("\r[%s] 现价:$%.2f | 等待%s发酵 | 偏离:%+.2f%% | 净浮盈:$%+.2f (资金池:$%.2f)  ",
					now.Format("15:04:05"), curPrc, dirStr, pnlPct*100, netPnl, pos.Margin+netPnl)
			} else {
				// 为避免刷屏，空仓时仅每 10 秒跳动一次
				if now.Sub(lastPrintTime) > 10*time.Second {
					fmt.Printf("\r[%s] 现价:$%.2f | 资金池:$%.2f | 侦听突发黑天鹅/利多信号中...",
						now.Format("15:04:05"), tick.Prc, pos.Cash)
					lastPrintTime = now
				}
			}
		}
	}
}
