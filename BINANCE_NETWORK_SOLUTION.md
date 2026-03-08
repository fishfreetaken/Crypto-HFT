# 币安 API 及 WebSocket 网络连通性解决方案 (防屏蔽指南)

## ❌ 常见问题现象
在本地机器（如云服务器或家庭宽带，特别是位于**马来西亚**等部分受监管地区，运营商为 TM 等）运行程序连接币安服务时，可能会遭遇以下报错：
- `websocket dial error: dial tcp 175.139.x.x:443: connectex: A connection attempt failed...`
- HTTP 接口请求 `Timeout` 或 `Connection Refused` 无法拉取价格数据。
- 日志中一直卡在 “正在连接币安数据流...” 并最终断开。

## 🔍 故障排查原因
发生此问题的原因在于**本地网络运营商 (ISP) 在骨干网层面上对币安的主域名 `*.binance.com` 进行了 DNS 污染或 SNI 阻断**。
这意味着，无论你是尝试获取当前价格明细还是连接 WebSocket 清算地图流，只要用的是 `.com` 域名，流量都会被拦截。

## 💡 终极解决方案（无需梯子/VPN）
这是最优雅、合规且能保证最低延迟的高频交易解决思路：**直接使用币安官方提供的抗污染备用域名 `.info`**。这些备用接口无需挂任何代理（Proxy）即可在国内/东南亚受限网络直连。

在你的量化爬虫、数据 Feeder 工具和所有 HFT 代码逻辑中，做以下全局替换：

### 1. U本位合约 WebSocket 行情数据流 (如爆仓流、K线流)
- ❌ **原始地址**: `wss://fstream.binance.com`
- ✅ **替换为**: `wss://fstream.binance.info`

### 2. 现货 / U本位常规 REST API 获取价格与下单
- ❌ **现货 HTTP**: `https://api.binance.com` 👉 ✅ **替换为**: `https://api.binance.info`
- ❌ **合约 HTTP**: `https://fapi.binance.com` 👉 ✅ **替换为**: `https://fapi.binance.info`

### 3. 现货 WebSocket 数据流
- ❌ **原始地址**: `wss://stream.binance.com:9443`
- ✅ **替换为**: `wss://stream.binance.info:9443`

---

## 🛠 代码审查清单建议

如果在未来的价格采集、回测系统数据下载工具或是 `Crypto-HFT/cmd/collector` 相关的组件中发现网络不通，请全局搜索你的 Go 代码及 Python 脚本：
1. `binance.com`
2. 找到所有 API 请求 baseURL
3. 批量将 `.com` 更改为 `.info` 即可畅通无阻。

**注意**：Go 语言程序在初始化网络连接时会自动读取系统环境变量中的代理配置。如果您曾为了测试而尝试设置了无效的环境变量（例如 `$env:http_proxy="127.0.0.1:1080"`），请确保在运行正式系统时通过 `set http_proxy=` （Cmd）或 `$env:http_proxy=""` (PowerShell) 将其清空，避免引起死循环的拨号失败！
