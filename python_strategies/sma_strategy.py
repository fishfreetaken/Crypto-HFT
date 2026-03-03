import ccxt
import time
import pandas as pd
import logging
import argparse

# 设置日志格式
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')

class SMAStrategy:
    """
    一个简单的双均线（SMA）交叉趋势跟踪加密货币量化策略。
    使用CCXT统一接口，支持币安、OKX等主流交易所。
    """
    def __init__(self, exchange_id, symbol, api_key=None, secret=None, short_window=10, long_window=30, timeframe='1h', trade_size=0.01, **kwargs):
        self.symbol = symbol
        self.short_window = short_window
        self.long_window = long_window
        self.timeframe = timeframe
        self.trade_size = trade_size
        self.position = 0 # 简单的仓位管理：0代表空仓，1代表持有多头，-1代表持有空头

        # 动态实例化交易所
        exchange_class = getattr(ccxt, exchange_id)
        
        # 构建配置字典
        exchange_config = {
            'apiKey': api_key,
            'secret': secret,
            'enableRateLimit': True, # 必须开启，防止被交易所API限制
        }
        
        # OKX 特殊要求：需要传递 API 密码 (passphrase)
        if exchange_id.lower() == 'okx':
            import os
            # 我们通过类的属性或者这里直接写死个变量来传入，为了快速演示我在下面做了环境或者直接获取
            exchange_config['password'] = os.environ.get('OKX_PASSPHRASE', kwargs.get('passphrase', '')) 
            
        self.exchange = exchange_class(exchange_config)
        
        # 默认使用沙盒环境(Testnet)以保护资金安全
        # 注意：并非所有交易所的沙盒环境都可用，请根据实际情况调整
        try:
            self.exchange.set_sandbox_mode(True)
            logging.info(f"已开启 {exchange_id} 沙盒模式 (Testnet)")
        except Exception as e:
            logging.warning(f"{exchange_id} 不支持沙盒模式或开启失败，将连接到真实网络！请小心！异常: {e}")
        
    def fetch_ohlcv(self):
        """
        获取K线数据
        """
        try:
            # 获取足够计算长周期均线的数据（加一点冗余）
            limit = self.long_window + 5
            ohlcv = self.exchange.fetch_ohlcv(self.symbol, timeframe=self.timeframe, limit=limit)
            
            # 使用pandas转换为DataFrame以便进行数据处理
            df = pd.DataFrame(ohlcv, columns=['timestamp', 'open', 'high', 'low', 'close', 'volume'])
            df['timestamp'] = pd.to_datetime(df['timestamp'], unit='ms')
            return df
        except ccxt.NetworkError as e:
            logging.error(f"网络异常: {e}")
            return None
        except ccxt.ExchangeError as e:
            logging.error(f"交易所异常: {e}")
            return None
        except Exception as e:
            logging.error(f"发生未知错误: {e}")
            return None

    def calculate_signals(self, df):
        """
        计算交易信号：金叉买入，死叉卖出
        """
        # 计算短期和长期简单移动平均线 (SMA)
        df['sma_short'] = df['close'].rolling(window=self.short_window).mean()
        df['sma_long'] = df['close'].rolling(window=self.long_window).mean()
        
        # 为了避免未来函数，我们使用上一根已经闭合的K线(previous)做对比
        # latest 可能是未闭合的当前K线
        current = df.iloc[-2]
        previous = df.iloc[-3]
        
        # 判断是否发生金叉 (短期均线向上穿过长期均线)
        if previous['sma_short'] <= previous['sma_long'] and current['sma_short'] > current['sma_long']:
            return 'BUY'
        # 判断是否发生死叉 (短期均线向下穿过长期均线)
        elif previous['sma_short'] >= previous['sma_long'] and current['sma_short'] < current['sma_long']:
            return 'SELL'
        
        return 'HOLD'

    def execute_trade(self, signal, order_type='market'):
        """
        执行订单操作
        """
        try:
            if signal == 'BUY' and self.position <= 0:
                logging.info(f"执行 买入 订单, 数量: {self.trade_size} {self.symbol}")
                # ========== 真实交易代码（请在有足够信心时解除注释）==========
                order = self.exchange.create_order(self.symbol, order_type, 'buy', self.trade_size)
                # logging.info(order)
                self.position = 1
                
            elif signal == 'SELL' and self.position >= 0:
                logging.info(f"执行 卖出 订单, 数量: {self.trade_size} {self.symbol}")
                # ========== 真实交易代码（请在有足够信心时解除注释）==========
                order = self.exchange.create_order(self.symbol, order_type, 'sell', self.trade_size)
                # logging.info(order)
                self.position = -1
            else:
                current_price = self.fetch_ohlcv().iloc[-1]['close'] if self.fetch_ohlcv() is not None else 'N/A'
                logging.info(f"无交易执行。当前信号: {signal}, 当前状态: {self.position}, 最新价格: {current_price}")
        except Exception as e:
             logging.error(f"执行交易时出错: {e}")

    def run(self, sleep_seconds=60):
        """
        主循环：持续拉取数据、计算信号和执行交易
        """
        logging.info(f"启动双均线(SMA)策略...")
        logging.info(f"交易所: {self.exchange.id}, 交易对: {self.symbol}, 时间组件: {self.timeframe}")
        logging.info(f"短期均线窗口: {self.short_window}, 长期均线窗口: {self.long_window}, 面值(数量): {self.trade_size}")
        
        while True:
            df = self.fetch_ohlcv()
            if df is not None and not df.empty:
                signal = self.calculate_signals(df)
                self.execute_trade(signal)
            
            # 等待一段时间后进行下一次轮询，以防过度消耗API限额
            time.sleep(sleep_seconds)

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="CCXT 简易双均线量化交易机器人")
    parser.add_argument('--exchange', type=str, default='binance', help='交易所ID, 如 binance, okx, bybit 等 (参考ccxt文档)')
    parser.add_argument('--symbol', type=str, default='BTC/USDT', help='交易对, 如 BTC/USDT, ETH/USDT')
    parser.add_argument('--tf', type=str, default='15m', help='K线时间框架, 如 1m, 15m, 1h, 1d')
    parser.add_argument('--passphrase', type=str, default='', help='OKX所需的API密码(Passphrase)')
    args = parser.parse_args()

    # 在此处填入你的 API 配置 (建议使用测试网以避免意外损失)
    # 如果只测试拉取数据，不实际下单，可以放空
    API_KEY = "YOUR_API_KEY_HERE"
    SECRET_KEY = "YOUR_SECRET_KEY_HERE"
    
    strategy = SMAStrategy(
        exchange_id=args.exchange, 
        symbol=args.symbol, 
        api_key=API_KEY, 
        secret=SECRET_KEY,
        passphrase=args.passphrase, # 添加密码传递
        short_window=10,   # 短期均线周期
        long_window=30,    # 长期均线周期
        timeframe=args.tf, # 时间框架
        trade_size=0.001   # 每次交易笔数（请根据交易所的最小交易粒度调整）
    )
    
    # 取消注释下方代码即可实际运行策略:
    logging.info("策略初始化成功。请填入API Keys并取消注释 strategy.run() 开始运行。")
    strategy.run(sleep_seconds=30)
