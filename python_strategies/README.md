# Python CCXT 量化交易策略

这是一个使用 `ccxt` 和 `pandas` 编写的简易双均线（SMA）交叉跟随趋势策略框架。

## 前置环境准备

1. 请确保系统中安装了 Python 3.8 或以上版本。
2. 安装相关依赖包:
   ```bash
   pip install -r requirements.txt
   ```

## 策略简介

该策略默认使用 `BTC/USDT` 为交易对。通过不断拉取最新的 K 线数据 (OHLCV) 判断趋势。
规则定义：
- **金叉 (BUY信号)**： 当短期均线由下向上穿越长期均线，执行买入（做多）。
- **死叉 (SELL信号)**：当短期均线由上向下穿越长期均线，执行卖出（做空或平仓）。

## 如何使用

您可以打开 `sma_strategy.py` 文件：
1. 更新主函数部分的 `API_KEY` 和 `SECRET_KEY` （强烈建议申请测试网API Key先进行测试）。
2. 在 `execute_trade` 函数中，解开 `self.exchange.create_order` 代码的注释以执行真实订单。
3. 解除文件最末部 `strategy.run(sleep_seconds=30)` 的注释。

您可以传入参数执行：
```bash
python sma_strategy.py --exchange binance --symbol ETH/USDT --tf 1h
```

## 免责声明

本脚本仅用作为编程展示和学习参考，包含简单的仓位处理与模拟。实际进行量化交易需注意：滑点、手续费、API断线重连、并发性以及资金管理问题。由于该脚本引发的任何资金损失与开发者无关。
