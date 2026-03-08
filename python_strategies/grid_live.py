import ccxt
import time
import argparse
import logging
import os

logging.basicConfig(level=logging.INFO, format="%(asctime)s - %(message)s", handlers=[
    logging.FileHandler("grid_live.log", encoding="utf-8"),
    logging.StreamHandler()
])

class GridLiveDeployer:
    """
    网格交易（Grid Trading）实盘部署引擎
    """
    def __init__(self, exchange_id, symbol, api_key, secret, password, lower_price, upper_price, num_grids, capital, trail_up=False, trail_down=False):
        self.symbol = symbol
        self.lower_price = lower_price
        self.upper_price = upper_price
        self.num_grids = num_grids
        self.capital = capital
        self.trail_up = trail_up
        self.trail_down = trail_down

        exchange_class = getattr(ccxt, exchange_id)
        config = {
            'apiKey': api_key,
            'secret': secret,
            'password': password,
            'enableRateLimit': True,
        }
        self.exchange = exchange_class(config)
        
        # 尝试开启沙盒进行测试
        try:
             self.exchange.set_sandbox_mode(True)
             logging.info(f"开启 {exchange_id} 测试网 (Sandbox)")
        except Exception:
             logging.warning(f"无法开启测试网，将工作在实盘环境！请谨慎操作。")

        self.grid_spacing = (self.upper_price - self.lower_price) / self.num_grids
        self.capital_per_grid = self.capital / self.num_grids
        
        self.grids = []
        self.initialized = False

    def init_grids(self):
        try:
            ticker = self.exchange.fetch_ticker(self.symbol)
            start_price = ticker['last']
        except Exception as e:
            logging.error(f"获取最新价格失败: {e}")
            return
            
        logging.info(f"========= 网格交易实盘初始化 =========")
        logging.info(f"标的: {self.symbol} | 最新价: {start_price}")
        logging.info(f"区间: {self.lower_price} - {self.upper_price} | 格数: {self.num_grids}")
        
        base_asset_needed = 0.0
        for i in range(self.num_grids):
            buy_p = self.lower_price + i * self.grid_spacing
            sell_p = buy_p + self.grid_spacing
            qty = self.capital_per_grid / buy_p
            
            if start_price > sell_p:
                status = "waiting_buy"
            elif start_price < buy_p:
                status = "waiting_sell"
                base_asset_needed += qty
            else:
                status = "waiting_sell"
                base_asset_needed += qty

            self.grids.append({
                "id": i,
                "buy_price": buy_p,
                "sell_price": sell_p,
                "qty": qty,
                "status": status,
                "order_id": None # 记录真实挂单ID
            })
            
        logging.info(f"初始化需提供法币: {self.capital - base_asset_needed*start_price:.2f} | 需预先买入现货: {base_asset_needed:.4f}")
        logging.info(f"注：实盘环境将在此处调用市价买入现货并挂开双边 Limit Orders。")
        self.initialized = True
        
    def loop(self):
        if not self.initialized:
            self.init_grids()
            
        logging.info("网格主控制循环已启动。(按 Ctrl+C 终止)")
        while True:
            try:
                ticker = self.exchange.fetch_ticker(self.symbol)
                current_price = ticker['last']
                
                # 此处可以做真实挂单的穿透检测和越迁(Trailing)逻辑
                # [由于是演示版本，此处仅打印监控]
                # print(f"[{time.strftime('%H:%M:%S')}] {self.symbol} 当前价: {current_price}")
                
            except Exception as e:
                logging.error(f"API请求异常: {e}")
                
            time.sleep(10)

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="网格交易实盘运行")
    parser.add_argument("--exchange", type=str, default="okx")
    parser.add_argument("--symbol", type=str, default="DOGE/USDT")
    parser.add_argument("--lower", type=float, default=0.10)
    parser.add_argument("--upper", type=float, default=0.45)
    parser.add_argument("--grids", type=int, default=100)
    parser.add_argument("--capital", type=float, default=10000)
    
    args = parser.parse_args()
    
    deployer = GridLiveDeployer(
         exchange_id=args.exchange,
         symbol=args.symbol,
         api_key=os.environ.get("API_KEY", ""),
         secret=os.environ.get("SECRET", ""),
         password=os.environ.get("PASSWORD", ""),
         lower_price=args.lower,
         upper_price=args.upper,
         num_grids=args.grids,
         capital=args.capital
    )
    deployer.loop()
