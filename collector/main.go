package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// collectorCfg 采集器独立配置，与主策略 config.json 互不干扰。
// 运行中直接编辑 collector.json 并保存，3 秒内自动热重载。
type collectorCfg struct {
	SampleIntervalSec int    `json:"sample_interval_sec"`
	DataDir           string `json:"data_dir"`
}

func (c collectorCfg) sampleInterval() time.Duration {
	if c.SampleIntervalSec <= 0 {
		return 5 * time.Second
	}
	return time.Duration(c.SampleIntervalSec) * time.Second
}

// parseCfg 从文件解析配置，失败时返回 error（不含默认值兜底，由调用方决定）。
func parseCfg(path string) (collectorCfg, error) {
	f, err := os.Open(path)
	if err != nil {
		return collectorCfg{}, err
	}
	defer f.Close()
	var cfg collectorCfg
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return collectorCfg{}, err
	}
	return cfg, nil
}

// loadCfg 加载配置，文件不存在或解析失败时使用内置默认值。
func loadCfg(path string) collectorCfg {
	def := collectorCfg{SampleIntervalSec: 5, DataDir: "data"}
	cfg, err := parseCfg(path)
	if err != nil {
		log.Printf("未找到或无法解析 %s，使用默认配置 (interval=%ds dir=%s)\n",
			path, def.SampleIntervalSec, def.DataDir)
		return def
	}
	if cfg.SampleIntervalSec <= 0 {
		cfg.SampleIntervalSec = def.SampleIntervalSec
	}
	if cfg.DataDir == "" {
		cfg.DataDir = def.DataDir
	}
	log.Printf("已加载配置: %s  (interval=%ds dir=%s)\n",
		path, cfg.SampleIntervalSec, cfg.DataDir)
	return cfg
}

// watchConfig 每隔 checkEvery 检查配置文件修改时间，变化时重新解析并写入 ch。
// 仅在解析成功时才发送，解析失败时打印警告并保持旧配置不变。
func watchConfig(cfgPath string, checkEvery time.Duration, ch chan<- collectorCfg) {
	var lastMod time.Time
	for {
		time.Sleep(checkEvery)
		info, err := os.Stat(cfgPath)
		if err != nil {
			continue
		}
		if !info.ModTime().After(lastMod) {
			continue
		}
		lastMod = info.ModTime()
		cfg, err := parseCfg(cfgPath)
		if err != nil {
			log.Printf("[热重载] 解析失败，保持当前配置: %v\n", err)
			continue
		}
		if cfg.SampleIntervalSec <= 0 {
			log.Printf("[热重载] sample_interval_sec 非正数，忽略本次变更\n")
			continue
		}
		// 非阻塞写入：若上一次变更还没被消费，丢弃旧的
		select {
		case ch <- cfg:
		default:
			ch <- cfg // 清空后重写（channel 容量=1）
		}
	}
}

// ===== HTTP 价格源（OKX → Bybit → Binance）=====

var httpClient = &http.Client{Timeout: 5 * time.Second}

func fetchFromOKX() (PriceRecord, error) {
	resp, err := httpClient.Get("https://www.okx.com/api/v5/market/ticker?instId=BTC-USDT")
	if err != nil {
		return PriceRecord{}, err
	}
	defer resp.Body.Close()
	var result struct {
		Data []struct {
			Last  string `json:"last"`
			Bid   string `json:"bidPx"`
			Ask   string `json:"askPx"`
			BidSz string `json:"bidSz"`
			AskSz string `json:"askSz"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return PriceRecord{}, err
	}
	if len(result.Data) == 0 {
		return PriceRecord{}, fmt.Errorf("OKX 返回空数据")
	}
	d := result.Data[0]
	last, _ := strconv.ParseFloat(d.Last, 64)
	bid, _ := strconv.ParseFloat(d.Bid, 64)
	ask, _ := strconv.ParseFloat(d.Ask, 64)
	bv, _ := strconv.ParseFloat(d.BidSz, 64)
	av, _ := strconv.ParseFloat(d.AskSz, 64)
	return PriceRecord{Price: last, Bid: bid, Ask: ask, BidVol: bv, AskVol: av}, nil
}

func fetchFromBybit() (PriceRecord, error) {
	resp, err := httpClient.Get("https://api.bybit.com/v5/market/tickers?category=spot&symbol=BTCUSDT")
	if err != nil {
		return PriceRecord{}, err
	}
	defer resp.Body.Close()
	var result struct {
		Result struct {
			List []struct {
				LastPrice string `json:"lastPrice"`
				Bid1Price string `json:"bid1Price"`
				Ask1Price string `json:"ask1Price"`
				Bid1Size  string `json:"bid1Size"`
				Ask1Size  string `json:"ask1Size"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return PriceRecord{}, err
	}
	if len(result.Result.List) == 0 {
		return PriceRecord{}, fmt.Errorf("Bybit 返回空数据")
	}
	d := result.Result.List[0]
	last, _ := strconv.ParseFloat(d.LastPrice, 64)
	bid, _ := strconv.ParseFloat(d.Bid1Price, 64)
	ask, _ := strconv.ParseFloat(d.Ask1Price, 64)
	bv, _ := strconv.ParseFloat(d.Bid1Size, 64)
	av, _ := strconv.ParseFloat(d.Ask1Size, 64)
	return PriceRecord{Price: last, Bid: bid, Ask: ask, BidVol: bv, AskVol: av}, nil
}

func fetchFromBinance() (PriceRecord, error) {
	resp, err := httpClient.Get("https://api.binance.com/api/v3/ticker/24hr?symbol=BTCUSDT")
	if err != nil {
		return PriceRecord{}, err
	}
	defer resp.Body.Close()
	var d struct {
		LastPrice string `json:"lastPrice"`
		BidPrice  string `json:"bidPrice"`
		AskPrice  string `json:"askPrice"`
		BidQty    string `json:"bidQty"`
		AskQty    string `json:"askQty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return PriceRecord{}, err
	}
	last, _ := strconv.ParseFloat(d.LastPrice, 64)
	bid, _ := strconv.ParseFloat(d.BidPrice, 64)
	ask, _ := strconv.ParseFloat(d.AskPrice, 64)
	bv, _ := strconv.ParseFloat(d.BidQty, 64)
	av, _ := strconv.ParseFloat(d.AskQty, 64)
	return PriceRecord{Price: last, Bid: bid, Ask: ask, BidVol: bv, AskVol: av}, nil
}

func fetchPrice() (PriceRecord, error) {
	sources := []struct {
		name string
		fn   func() (PriceRecord, error)
	}{
		{"OKX", fetchFromOKX},
		{"Bybit", fetchFromBybit},
		{"Binance", fetchFromBinance},
	}
	for _, src := range sources {
		record, err := src.fn()
		if err == nil {
			return record, nil
		}
		log.Printf("【%s】不可用: %v，尝试下一个源...", src.name, err)
	}
	return PriceRecord{}, fmt.Errorf("所有价格源均不可用")
}

// ===== 按小时轮转的文件写入器 =====

// fileWriter 维护当前打开的文件句柄，按小时自动切换新文件。
// 目录结构：{DataDir}/{YYYY-MM-DD}/{HH}.jsonl
type fileWriter struct {
	dataDir string
	curPath string // 当前写入的文件路径
	file    *os.File
	enc     *json.Encoder
	total   int // 本次会话累计写入条数
}

// PriceRecord 写入文件的价格记录
type PriceRecord struct {
	Ts     time.Time `json:"ts"`
	Price  float64   `json:"price"`
	Bid    float64   `json:"bid,omitempty"`
	Ask    float64   `json:"ask,omitempty"`
	BidVol float64   `json:"bid_vol,omitempty"`
	AskVol float64   `json:"ask_vol,omitempty"`
}

func (w *fileWriter) targetPath(t time.Time) string {
	return filepath.Join(w.dataDir, t.Format("2006-01-02"), t.Format("15")+".jsonl")
}

// ensureFile 检查目标路径是否有变化（整点切换），按需轮转文件。
func (w *fileWriter) ensureFile(t time.Time) error {
	path := w.targetPath(t)
	if path == w.curPath {
		return nil
	}
	// 关闭旧文件
	if w.file != nil {
		w.file.Sync()
		w.file.Close()
		fmt.Printf("[%s] 小时轮转 → %s\n", t.Format("15:04:05"), path)
	}
	// 按天创建目录（不存在时自动建）
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	w.file = f
	w.enc = json.NewEncoder(f)
	w.curPath = path
	return nil
}

func (w *fileWriter) write(rec PriceRecord) error {
	if err := w.ensureFile(rec.Ts); err != nil {
		return err
	}
	if err := w.enc.Encode(rec); err != nil {
		return err
	}
	w.file.Sync()
	w.total++
	return nil
}

func (w *fileWriter) close() {
	if w.file != nil {
		w.file.Sync()
		w.file.Close()
		w.file = nil
	}
}

// ===== 主程序 =====

func main() {
	cfgPath := "collector.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg := loadCfg(cfgPath)

	fmt.Println("========== BTC 价格采集器 ==========")
	fmt.Printf("采样间隔: %v | 数据目录: %s/\n", cfg.sampleInterval(), cfg.DataDir)
	fmt.Printf("配置文件: %s（运行中修改并保存可实时生效）\n", cfgPath)
	fmt.Printf("文件策略: %s/{YYYY-MM-DD}/{HH}.jsonl\n", cfg.DataDir)
	fmt.Println("按 Ctrl+C 停止采集")
	fmt.Println("=====================================")

	w := &fileWriter{dataDir: cfg.DataDir}
	defer w.close()

	// 捕获 Ctrl+C / SIGTERM
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// 启动配置热重载监听（每 3 秒检查文件修改时间）
	cfgCh := make(chan collectorCfg, 1)
	go watchConfig(cfgPath, 3*time.Second, cfgCh)

	// 立即采集一次（不等第一个 tick）
	now := time.Now()
	if record, err := fetchPrice(); err == nil {
		record.Ts = now
		if werr := w.write(record); werr != nil {
			log.Printf("写入失败: %v\n", werr)
		} else {
			fmt.Printf("[%s] $%.2f → %s (本次第 %d 条)\n",
				now.Format("15:04:05"), record.Price, w.curPath, w.total)
		}
	} else {
		log.Printf("首次采集失败: %v\n", err)
	}

	ticker := time.NewTicker(cfg.sampleInterval())
	defer ticker.Stop()

	for {
		select {
		case <-sig:
			fmt.Printf("\n采集已停止，本次共写入 %d 条记录\n最后文件: %s\n", w.total, w.curPath)
			return

		case newCfg := <-cfgCh:
			oldInterval := cfg.sampleInterval()
			cfg = newCfg
			w.dataDir = cfg.DataDir // 数据目录也支持热更新
			if cfg.sampleInterval() != oldInterval {
				ticker.Reset(cfg.sampleInterval())
				fmt.Printf("[热重载] 采样间隔 %v → %v\n", oldInterval, cfg.sampleInterval())
			} else {
				fmt.Printf("[热重载] 配置已更新 (interval=%ds dir=%s)\n",
					cfg.SampleIntervalSec, cfg.DataDir)
			}

		case t := <-ticker.C:
			record, err := fetchPrice()
			if err != nil {
				log.Printf("采集失败: %v\n", err)
				continue
			}
			record.Ts = t
			if werr := w.write(record); werr != nil {
				log.Printf("写入失败: %v\n", werr)
				continue
			}
			fmt.Printf("[%s] $%.2f → %s (本次第 %d 条)\n",
				t.Format("15:04:05"), record.Price, w.curPath, w.total)
		}
	}
}
