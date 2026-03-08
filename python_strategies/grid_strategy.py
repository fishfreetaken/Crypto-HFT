import ccxt
import time
import argparse
import pandas as pd
import numpy as np
import logging

logging.basicConfig(level=logging.INFO, format="%(asctime)s - %(message)s", handlers=[
    logging.FileHandler("grid_backtest.log", encoding="utf-8"),
    logging.StreamHandler()
])

class GridStrategyBacktester:
    """
    网格交易（Grid Trading）回测引擎 (支持 Trailing 网格跟随)
    """
    def __init__(self, symbol="BTC/USDT", lower_price=50000, upper_price=100000, num_grids=100, 
                 total_capital=10000.0, fee_rate=0.0005, trailing_up=False, trailing_down=False):
        self.symbol = symbol
        self.initial_lower_price = lower_price
        self.initial_upper_price = upper_price
        self.num_grids = num_grids
        self.total_capital = total_capital
        self.fee_rate = fee_rate
        self.trailing_up = trailing_up
        self.trailing_down = trailing_down
        
        self.exchange = ccxt.okx({"enableRateLimit": True})

    def fetch_data(self, timeframe="5m", start_date="2024-10-01", end_date="2024-12-31"):
        """拉取币安/OKX的历史数据供回测"""
        since_ms = self.exchange.parse8601(f"{start_date}T00:00:00Z")
        until_ms = self.exchange.parse8601(f"{end_date}T23:59:59Z")
        
        all_ohlcv = []
        cur = since_ms
        logging.info(f"拉取 {self.symbol} {timeframe} 数据: {start_date} 到 {end_date}")
        while cur < until_ms:
            try:
                batch = self.exchange.fetch_ohlcv(self.symbol, timeframe, since=cur, limit=300)
                if not batch:
                    break
                batch = [b for b in batch if b[0] <= until_ms]
                all_ohlcv.extend(batch)
                nxt = batch[-1][0] + 1
                if nxt <= cur or nxt >= until_ms:
                    break
                cur = nxt
                time.sleep(0.2)
            except Exception as e:
                logging.warning(f"获取数据出错: {e}")
                break
        
        if not all_ohlcv:
            return None
            
        df = pd.DataFrame(all_ohlcv, columns=["ts","open","high","low","close","volume"])
        df["datetime"] = pd.to_datetime(df["ts"], unit="ms", utc=True)
        df = df.set_index("datetime").drop(columns=["ts"])
        df = df[~df.index.duplicated(keep="first")]
        logging.info(f"拉取完成。共 {len(df)} 根 K线数据。区间：{df['low'].min()} - {df['high'].max()}")
        return df

    def run_backtest(self, df):
        if df is None or df.empty:
            logging.error("没有数据，回测终止。")
            return
            
        lower_price = self.initial_lower_price
        upper_price = self.initial_upper_price
        grid_spacing = (upper_price - lower_price) / self.num_grids
        
        # 资金分配: 每个网格投入相同数量的资金
        capital_per_grid = self.total_capital / self.num_grids
        grid_profit_rate = grid_spacing / lower_price
        logging.info(f"模式: {'跟随(Trailing)' if self.trailing_up or self.trailing_down else '固定(Fixed)'} 网格")
        logging.info(f"平均每格利润率 (扣除手续费前): 约 {grid_profit_rate * 100:.2f}%")
        
        start_price = df.iloc[0]["open"]
        logging.info(f"回测初始环境 - 首根K线开盘价: {start_price:.2f}")
        
        grids = []
        base_asset = 0.0
        quote_asset = self.total_capital
        
        # 初始建仓
        for i in range(self.num_grids):
            buy_p = lower_price + i * grid_spacing
            sell_p = buy_p + grid_spacing
            qty = capital_per_grid / buy_p
            
            if start_price > sell_p:
                # 价格在网格之上，该处于等待买入状态（空仓，拿着U等待价格跌下来买入）
                status = "waiting_buy"
            elif start_price < buy_p:
                # 价格在网格之下，该等待卖出（手里需要拿着现货，等价格涨上来卖出）
                # 因此需要在开盘时以市价预先买入这部分现货
                status = "waiting_sell"
                cost = qty * start_price * (1 + self.fee_rate)
                quote_asset -= cost
                base_asset += qty
            else:
                # 价格恰好在网格区间内（中间格），按等待卖出处理（也可分拆，这里统一拿着现货等涨）
                status = "waiting_sell"
                cost = qty * start_price * (1 + self.fee_rate)
                quote_asset -= cost
                base_asset += qty

            grids.append({
                "id": i,
                "buy_price": buy_p,
                "sell_price": sell_p,
                "qty": qty,
                "status": status,
                "trade_count": 0
            })
            
        init_account_value = quote_asset + base_asset * start_price
        logging.info(f"初始建仓: Token={base_asset:.4f}, USDT={quote_asset:.2f}, 初始净值={init_account_value:.2f}")
        
        trades = []
        trailing_shifts_up = 0
        trailing_shifts_down = 0

        for idx, row in df.iterrows():
            low = row["low"]
            high = row["high"]
            
            if row["close"] >= row["open"]:
                path = [(low, "low"), (high, "high")]
            else:
                path = [(high, "high"), (low, "low")]
                
            for price, p_type in path:
                # 检查普通网格成交
                for g in grids:
                    if g["status"] == "waiting_buy" and price <= g["buy_price"]:
                        exec_price = g["buy_price"]
                        cost = g["qty"] * exec_price * (1 + self.fee_rate)
                        quote_asset -= cost
                        base_asset += g["qty"]
                        g["status"] = "waiting_sell"
                        trades.append({"time": idx, "type": "BUY", "price": exec_price, "qty": g["qty"]})
                        
                    elif g["status"] == "waiting_sell" and price >= g["sell_price"]:
                        exec_price = g["sell_price"]
                        revenue = g["qty"] * exec_price * (1 - self.fee_rate)
                        quote_asset += revenue
                        base_asset -= g["qty"]
                        g["status"] = "waiting_buy"
                        g["trade_count"] += 1
                        trades.append({"time": idx, "type": "SELL", "price": exec_price, "qty": g["qty"]})

                # 检查跟随移动 (Trailing Up)
                if self.trailing_up and price > upper_price:
                    while price > upper_price:
                        bottom_grid = grids.pop(0)
                        
                        new_buy = upper_price
                        new_sell = upper_price + grid_spacing
                        new_qty = capital_per_grid / new_buy
                        
                        cost = new_qty * price * (1 + self.fee_rate)
                        quote_asset -= cost
                        base_asset += new_qty
                        
                        grids.append({
                            "buy_price": new_buy,
                            "sell_price": new_sell,
                            "qty": new_qty,
                            "status": "waiting_sell",
                            "trade_count": 0
                        })
                        trades.append({"time": idx, "type": "TRAIL_BUY", "price": price, "qty": new_qty})
                        
                        lower_price += grid_spacing
                        upper_price += grid_spacing
                        trailing_shifts_up += 1

                # 检查跟随移动 (Trailing Down)
                if self.trailing_down and price < lower_price:
                    while price < lower_price:
                        top_grid = grids.pop(-1)
                        if top_grid["status"] == "waiting_sell":
                            revenue = top_grid["qty"] * price * (1 - self.fee_rate)
                            quote_asset += revenue
                            base_asset -= top_grid["qty"]
                            trades.append({"time": idx, "type": "TRAIL_SELL", "price": price, "qty": top_grid["qty"]})
                        
                        new_sell = lower_price
                        new_buy = lower_price - grid_spacing
                        new_qty = capital_per_grid / new_buy
                        
                        grids.insert(0, {
                            "buy_price": new_buy,
                            "sell_price": new_sell,
                            "qty": new_qty,
                            "status": "waiting_buy",
                            "trade_count": 0
                        })
                        
                        lower_price -= grid_spacing
                        upper_price -= grid_spacing
                        trailing_shifts_down += 1

        end_price = df.iloc[-1]["close"]
        final_value = quote_asset + base_asset * end_price
        total_grid_profits = sum([g["trade_count"] for g in grids])
        
        logging.info("=" * 60)
        logging.info("【网格做市策略 (Grid Trading) 回测报告】")
        logging.info(f"交易物: {self.symbol}")
        logging.info(f"初始价格区间: {self.initial_lower_price} - {self.initial_upper_price}")
        logging.info(f"最终价格区间: {lower_price:.2f} - {upper_price:.2f}")
        if self.trailing_up or self.trailing_down:
             logging.info(f"网格向上越迁跟随: {trailing_shifts_up} 次 | 向下越迁跟随: {trailing_shifts_down} 次")
        logging.info(f"网格数量: {self.num_grids} | 单格利润幅度: 约 {grid_profit_rate*100:.2f}% | 手续费率: {self.fee_rate*100}%")
        logging.info("-" * 60)
        logging.info(f"投入总资金: {self.total_capital:.2f} USDT")
        logging.info(f"结束时净值: {final_value:.2f} USDT (持有 {base_asset:.4f} Token + {quote_asset:.2f} USDT)")
        
        net_profit = final_value - self.total_capital
        profit_rate = (net_profit / self.total_capital) * 100
        
        bh_profit = self.total_capital * (end_price / start_price - 1)
        bh_rate = bh_profit / self.total_capital * 100
        
        logging.info(f"策略总利润: {net_profit:+.2f} USDT  ({profit_rate:+.2f}%)")
        logging.info(f"同期现货买入持有利润 (Buy & Hold): {bh_profit:+.2f} USDT ({bh_rate:+.2f}%)")
        logging.info(f"常规网格成套交易次数: {total_grid_profits}")
        logging.info("=" * 60)
        
        return trades, df, final_value

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="网格交易（微型做市）回测")
    parser.add_argument("--symbol", type=str, default="BTC/USDT")
    parser.add_argument("--lower", type=float, default=65000, help="网格下限")
    parser.add_argument("--upper", type=float, default=95000, help="网格上限")
    parser.add_argument("--grids", type=int, default=100, help="网格数")
    parser.add_argument("--capital", type=float, default=10000, help="投入总资金")
    parser.add_argument("--start", type=str, default="2024-11-01")
    parser.add_argument("--end", type=str, default="2024-11-10")
    parser.add_argument("--tf", type=str, default="5m", help="K线间隔")
    parser.add_argument("--trail_up", action="store_true", help="允许网格向上跟随")
    parser.add_argument("--trail_down", action="store_true", help="允许网格向下跟随")
    
    args = parser.parse_args()
    
    backtester = GridStrategyBacktester(
        symbol=args.symbol,
        lower_price=args.lower,
        upper_price=args.upper,
        num_grids=args.grids,
        total_capital=args.capital,
        trailing_up=args.trail_up,
        trailing_down=args.trail_down
    )
    
    data = backtester.fetch_data(timeframe=args.tf, start_date=args.start, end_date=args.end)
    if data is not None:
        backtester.run_backtest(data)
