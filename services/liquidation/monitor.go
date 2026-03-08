package liquidation

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WsForceOrder 映射币安返回的强平事件 JSON 结构
type WsForceOrder struct {
	EventType string `json:"e"` // 事件类型 "forceOrder"
	EventTime int64  `json:"E"` // 事件时间
	Order     Order  `json:"o"` // 订单详情
}

type Order struct {
	Symbol       string `json:"s"`  // 交易对，如 BTCUSDT
	Side         string `json:"S"`  // 方向: SELL=多头被爆仓, BUY=空头被爆仓
	OrderType    string `json:"o"`  // 订单类型 LIMIT / MARKET
	TimeInForce  string `json:"f"`  // 有效方式 IOC
	Qty          string `json:"q"`  // 强平数量
	Price        string `json:"p"`  // 强平执行价格
	AveragePrice string `json:"ap"` // 平均执行价格
	OrderStatus  string `json:"X"`  // 状态 FILLED
	TradeTime    int64  `json:"T"`  // 交易时间
}

// LiquidationAlert 聚合后的清算爆仓警报，可供策略模块订阅
type LiquidationAlert struct {
	Symbol     string
	Side       string // "SELL" (多头爆) 或 "BUY" (空头爆)
	Price      float64
	TotalValue float64 // 美元价值
	Timestamp  time.Time
	Message    string
}

// MonitorConfig 控制独立币种的最小预警值
type MonitorConfig struct {
	DefaultMinAlertUSD float64            `json:"default_min_alert_usd"`
	Symbols            map[string]float64 `json:"symbols"` // 币种和针对该币种的特定报警阈值 (大写，如 BTCUSDT: 10000)
}

// Monitor 清算地图监控核心结构体
type Monitor struct {
	StreamName     string
	AlertChan      chan LiquidationAlert // 推送警报的管道
	stopChan       chan struct{}         // 停止信号管道
	mu             sync.Mutex            // 供并发安全的属性保护
	TotalProcessed int64                 // 已处理的爆仓条数
	wsConn         *websocket.Conn       // websocket实例

	configMu sync.RWMutex   // 配置锁，保证运行中热更新安全
	config   *MonitorConfig // 动态加载的配置对象
}

// NewMonitor 初始化一个清算流监视器
func NewMonitor(cfg *MonitorConfig) *Monitor {
	m := &Monitor{
		StreamName: "!forceOrder@arr", // 监听全市场全部强平，由我们在本地通过配置进行过滤
		AlertChan:  make(chan LiquidationAlert, 100),
		stopChan:   make(chan struct{}),
	}
	m.UpdateConfig(cfg)
	return m
}

// UpdateConfig 供系统或配置文件热加载更新参数
func (m *Monitor) UpdateConfig(cfg *MonitorConfig) {
	if cfg == nil {
		return
	}

	m.configMu.Lock()
	defer m.configMu.Unlock()

	// 标准化 symbols, 将其变为全大写并在尾部加入 USDT 以备后续判断
	normalizedSymbols := make(map[string]float64)
	if cfg.Symbols != nil {
		for k, v := range cfg.Symbols {
			upperSymbol := strings.ToUpper(k)
			if !strings.HasSuffix(upperSymbol, "USDT") {
				upperSymbol += "USDT"
			}
			normalizedSymbols[upperSymbol] = v
		}
	}
	cfg.Symbols = normalizedSymbols
	m.config = cfg
}

// getThreshold 根据当前热更新配置，寻找该币种的过滤限制
func (m *Monitor) getThreshold(symbol string) float64 {
	m.configMu.RLock()
	defer m.configMu.RUnlock()

	if m.config == nil {
		return 10000.0 // 默认保底
	}

	upperSymbol := strings.ToUpper(symbol)

	// 首先看看这个币种是否有特定的自定义数值
	if threshold, ok := m.config.Symbols[upperSymbol]; ok {
		return threshold
	}

	// 如果没有在指定名单内，则采用全局默认过滤线。如果是-1说明不想看全局币种，直接过滤
	return m.config.DefaultMinAlertUSD
}

// Start 启动监视器 (非阻塞)
func (m *Monitor) Start() error {
	u := url.URL{Scheme: "wss", Host: "fstream.binance.info", Path: "/ws/" + m.StreamName}
	log.Printf("[Liquidation Monitor] 正在连接币安强平数据流: %s\n", u.String())

	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("websocket dial error: %w", err)
	}

	m.mu.Lock()
	m.wsConn = c
	m.mu.Unlock()

	go m.listen()
	return nil
}

// Stop 关闭监视器，释放资源
func (m *Monitor) Stop() {
	close(m.stopChan)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wsConn != nil {
		m.wsConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		m.wsConn.Close()
	}
}

// listen 内部循环用于读取 websocket 数据
func (m *Monitor) listen() {
	defer log.Println("[Liquidation Monitor] 退出监听协程")

	for {
		select {
		case <-m.stopChan:
			return
		default:
		}

		m.mu.Lock()
		conn := m.wsConn
		m.mu.Unlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[Liquidation Monitor] 读取数据流出错: %v (准备断开重连...)\n", err)
			m.reconnect()
			return
		}

		m.processMessage(message)
	}
}

// reconnect 简单的重连逻辑
func (m *Monitor) reconnect() {
	m.mu.Lock()
	if m.wsConn != nil {
		m.wsConn.Close()
	}
	m.mu.Unlock()

	time.Sleep(5 * time.Second)
	log.Println("[Liquidation Monitor] 正在尝试重连...")
	if err := m.Start(); err != nil {
		log.Printf("[Liquidation Monitor] 重连失败: %v\n", err)
		go m.reconnect() // 继续轮询重连
	}
}

func (m *Monitor) processMessage(message []byte) {
	var event WsForceOrder
	if err := json.Unmarshal(message, &event); err != nil {
		log.Printf("[Liquidation Monitor] JSON 解析忽略: %v\n", err)
		return
	}

	price, _ := strconv.ParseFloat(event.Order.Price, 64)
	qty, _ := strconv.ParseFloat(event.Order.Qty, 64)
	usdValue := price * qty

	m.TotalProcessed++

	// 取出该币的门槛
	threshold := m.getThreshold(event.Order.Symbol)

	// 如果阈值小于0，表示我们不在乎这个币；或没达到爆发红线，直接忽略
	if threshold < 0 || usdValue < threshold {
		return
	}

	timestamp := time.UnixMilli(event.EventTime)

	// 格式化数据封装为告警
	var direction string
	if event.Order.Side == "SELL" {
		direction = "📉 多头被爆 (向下砸盘)"
	} else {
		direction = "📈 空头被爆 (向上买入)"
	}

	msgStr := fmt.Sprintf("[%s] %s | 预警币种: %s | 价格: $%.4f | 数量: %.4f | 爆仓价值: $%.2f",
		timestamp.Format("15:04:05.000"), direction, event.Order.Symbol, price, qty, usdValue)

	alert := LiquidationAlert{
		Symbol:     event.Order.Symbol,
		Side:       event.Order.Side,
		Price:      price,
		TotalValue: usdValue,
		Timestamp:  timestamp,
		Message:    msgStr,
	}

	// 尝试非阻塞写入管道推送警报
	select {
	case m.AlertChan <- alert:
	default:
		// 当消费者消费不过来（Channel 满）时，忽略当前告警或增加缓冲。
		log.Println("[Liquidation Monitor - WARNING] 告警推送管道已满，可能有积压。")
	}
}
