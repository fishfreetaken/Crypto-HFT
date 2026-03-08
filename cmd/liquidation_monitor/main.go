package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"biance/services/liquidation"
)

// 加载外部 JSON 配置文件
func loadConfig(path string) (*liquidation.MonitorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg liquidation.MonitorConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func main() {
	configPath := "targets.json"

	// 初始加载配置
	initialCfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("启动失败，无法读取初始配置文件 %s: %v", configPath, err)
	}

	monitor := liquidation.NewMonitor(initialCfg)

	if err := monitor.Start(); err != nil {
		log.Fatal("尝试连接清算流失败:", err)
	}

	// 监听中断以优雅退出
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("============ [清算地图引擎已启动] ============")
	fmt.Printf("已成功加载 %d 个自定义监控币种。\n", len(initialCfg.Symbols))
	if initialCfg.DefaultMinAlertUSD > 0 {
		fmt.Printf("其余全局币种监听阈值：$%.2f\n", initialCfg.DefaultMinAlertUSD)
	} else {
		fmt.Println("全局监听已关闭，仅监听白名单配置币种。")
	}
	fmt.Println("配置文件热加载服务正在运行，每10秒检查一次... 等待极端行情触发...")

	// 热更新配置文件循环协程
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		var lastModTime time.Time
		if stat, err := os.Stat(configPath); err == nil {
			lastModTime = stat.ModTime()
		}

		for range ticker.C {
			stat, err := os.Stat(configPath)
			if err != nil {
				continue
			}

			// 如果文件产生新修改
			if stat.ModTime().After(lastModTime) {
				newCfg, err := loadConfig(configPath)
				if err == nil {
					monitor.UpdateConfig(newCfg)
					lastModTime = stat.ModTime()
					log.Printf("[配置热重载] 成功更新！当前监听币种数: %d\n", len(newCfg.Symbols))
				} else {
					log.Printf("[配置热重载 - ERROR] JSON 解析失败，保持原有配置: %v\n", err)
				}
			}
		}
	}()

	// 【此处对接大盘策略】：通过一个守护协程持续消费独立产生的事件流
	go func() {
		for alert := range monitor.AlertChan {
			var colorStr string
			if alert.Side == "SELL" { // 多头被爆 (系统被迫卖出)，红色向下砸盘
				colorStr = "\033[31m"
			} else { // 空头被爆，系统被迫买入，绿色向上轧空
				colorStr = "\033[32m"
			}
			resetCStr := "\033[0m"

			// 控制台终端彩墨打印
			fmt.Printf("%s[清算告警]%s %s\n", colorStr, resetCStr, alert.Message)

			// 可以在这里调用策略接口，例如：
			// exampleStrategy.OnLiquidation(alert)
		}
	}()

	<-sigs
	fmt.Println("收到停机信号，正在停止清算流监视...")
	monitor.Stop()
	fmt.Println("监视核心已关闭，系统安全退出。")
}
