package tradelib

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type EventSeverity int

const (
	SeverityInfo EventSeverity = iota
	SeverityHigh
)

type Event struct {
	Title    string
	Time     time.Time
	Severity EventSeverity
	Source   string
}

type NewsRadar struct {
	mu     sync.RWMutex
	events []Event
}

var GlobalRadar = &NewsRadar{
	events: make([]Event, 0),
}

// StartRadar 启动资讯雷达探针，定时抓取重大事件
func (r *NewsRadar) StartRadar(interval time.Duration) {
	go func() {
		// 初始抓取（给网络一点启动时间）
		time.Sleep(1 * time.Second)
		r.fetchBinanceAnnouncements()
		r.fetchMacroEvents()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			r.fetchBinanceAnnouncements()
			r.fetchMacroEvents()
		}
	}()
}

func (r *NewsRadar) GetRecentEvents(d time.Duration) []Event {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var recent []Event
	for _, e := range r.events {
		diff := time.Since(e.Time)
		if diff < 0 {
			diff = -diff // 未来的事件
		}
		if diff <= d {
			recent = append(recent, e)
		}
	}
	return recent
}

// IsHighRiskMode 判断当前是否处于核弹级事件影响窗口中
func (r *NewsRadar) IsHighRiskMode() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := time.Now()
	for _, e := range r.events {
		if e.Severity == SeverityHigh {
			// 过去 1 小时内的高危事件，或未来 2 小时内的宏观发表
			timeDiff := now.Sub(e.Time)
			if timeDiff > -2*time.Hour && timeDiff < 1*time.Hour {
				return true
			}
		}
	}
	return false
}

func (r *NewsRadar) addEvent(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 去重逻辑
	for _, existing := range r.events {
		if existing.Title == e.Title && existing.Source == e.Source {
			return
		}
	}
	r.events = append(r.events, e)
}

// fetchBinanceAnnouncements 抓取币安新币上线、下架等重大公告
func (r *NewsRadar) fetchBinanceAnnouncements() {
	client := http.Client{Timeout: 10 * time.Second}
	url := "https://www.binance.com/bapi/composite/v1/public/cms/article/catalog/list/query?catalogId=48&pageNo=1&pageSize=15"
	resp, err := client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var res struct {
		Data struct {
			Articles []struct {
				Title       string `json:"title"`
				ReleaseDate int64  `json:"releaseDate"`
			} `json:"articles"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err == nil {
		for _, art := range res.Data.Articles {
			t := time.UnixMilli(art.ReleaseDate)
			// 提取过去 24 小时的公告
			if time.Since(t) < 24*time.Hour {
				severity := SeverityInfo
				lowTitle := strings.ToLower(art.Title)
				if strings.Contains(lowTitle, "delist") || strings.Contains(lowTitle, "maintenance") || strings.Contains(lowTitle, "binance will list") || strings.Contains(lowTitle, "launchpool") {
					severity = SeverityHigh
				}
				r.addEvent(Event{
					Title:    art.Title,
					Time:     t,
					Severity: severity,
					Source:   "Binance公告",
				})
			}
		}
	}
}

// fetchMacroEvents 抓取美国核心宏观经济数据（如 CPI / 非农决议）
func (r *NewsRadar) fetchMacroEvents() {
	client := http.Client{Timeout: 10 * time.Second}
	url := "https://nfs.faireconomy.media/ff_calendar_thisweek.json"
	req, _ := http.NewRequest("GET", url, nil)
	// ForexFactory 拦截了默认 Go User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var events []struct {
		Title   string `json:"title"`
		Country string `json:"country"`
		Date    string `json:"date"`   // e.g. 2026-03-01T10:00:00-05:00
		Impact  string `json:"impact"` // "High", "Medium", "Low"
	}
	if err := json.NewDecoder(resp.Body).Decode(&events); err == nil {
		for _, ev := range events {
			// 美元（影响整个加密圈）并且是核弹级影响
			if ev.Country == "USD" && ev.Impact == "High" {
				t, err := time.Parse(time.RFC3339, ev.Date)
				if err == nil {
					// 仅关注过去24小时或未来48小时的宏观数据发布
					if time.Since(t) < 24*time.Hour && time.Until(t) < 48*time.Hour {
						r.addEvent(Event{
							Title:    fmt.Sprintf("USD %s", ev.Title),
							Time:     t,
							Severity: SeverityHigh,
							Source:   "宏观日历",
						})
					}
				}
			}
		}
	}
}

// PrintReport 打印近期大事件雷达快报
func (r *NewsRadar) PrintReport() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	fmt.Println("\n========== 🌐 重大突发事件与宏观雷达 ==========")
	if len(r.events) == 0 {
		fmt.Println(" [无] 过去24小时内未侦测到重大突发与宏观数据")
	} else {
		for _, e := range r.events {
			timeDesc := ""
			diff := time.Since(e.Time)
			if diff > 0 {
				timeDesc = fmt.Sprintf("已公布 %.1f 小时", diff.Hours())
			} else {
				timeDesc = fmt.Sprintf("\033[33m将于 %.1f 小时后公布\033[0m", (-diff).Hours())
			}

			sevDesc := "[一般]"
			if e.Severity == SeverityHigh {
				sevDesc = "\033[31m[高危/全盘剧震]\033[0m"
			}

			fmt.Printf(" [%s] %s | %s | %s\n", e.Source, sevDesc, e.Title, timeDesc)
		}
	}

	if r.IsHighRiskMode() {
		fmt.Println(" ⚠️ 当前风控: \033[31m系统处于高危影响期拦截窗口 (静默新仓，仅管平仓)\033[0m")
	} else {
		fmt.Println(" ✅ 当前风控: 市场资讯面平稳 (多空双向通行)")
	}
	fmt.Println("===============================================")
}
