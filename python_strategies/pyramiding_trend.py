import yfinance as yf
import pandas as pd
import numpy as np
import matplotlib.pyplot as plt

def run_pyramiding_strategy():
    print("---------------------------------------------------------")
    print(">>> Initializing High-Risk Pyramiding Strategy (浮盈加仓) <<<")
    print(">>> 核心逻辑：趋势追踪 + 浮盈加仓 + 10倍高杠杆 + 极窄移动止损 <<<")
    print("---------------------------------------------------------")
    
    try:
        btc = yf.download('BTC-USD', start='2026-01-01', end='2026-03-09', interval='1h', progress=False)
        if isinstance(btc.columns, pd.MultiIndex):
            btc.columns = [c[0] for c in btc.columns]
        btc = btc.dropna()
    except Exception as e:
        print(f"Data fetching failed: {e}")
        return

    btc['EMA20'] = btc['Close'].ewm(span=20 * 4, adjust=False).mean()
    btc['EMA50'] = btc['Close'].ewm(span=50 * 4, adjust=False).mean()
    
    initial_cap = 100000.0
    capital = initial_cap
    
    equity_curve = []
    timestamps = []
    
    positions = [] # List of dicts: {'entry': price, 'size': size}
    
    state = "WAITING"
    direction = 0  # 1 for long, -1 for short
    trailing_stop = 0.0
    max_favorable_excursion = 0.0
    trailing_pct = 0.06  # 6% trailing stop
    pyramid_step_pct = 0.02 # 加仓步幅加大一点以防过于频繁加仓导致爆仓
    next_pyramid_target = 0.0
    cooldown_candles = 0
    
    for idx, row in btc.iterrows():
        timestamps.append(idx)
        _close = float(row['Close'])
        _high = float(row['High'])
        _low = float(row['Low'])
        _ema20 = float(row['EMA20'])
        _ema50 = float(row['EMA50'])
        
        if cooldown_candles > 0:
            cooldown_candles -= 1
            
        current_equity = capital
        
        if state == "WAITING" and cooldown_candles == 0:
            # 做空条件：均线空头排列，且价格处在EMA20下方确认阻力
            if _ema20 < _ema50 and _close < _ema20:
                direction = -1
                state = "IN_POSITION"
                risk_amount = capital * 0.15
                notional = risk_amount * 10 
                size = notional / _close
                
                positions.append({'entry': _close, 'size': size})
                
                max_favorable_excursion = _low
                trailing_stop = _close * (1 + trailing_pct)
                next_pyramid_target = _close * (1 - pyramid_step_pct)
                
                print(f"[{idx}] 🔥 [信号触发] 均线空头排列，破位做空! 价格: {_close:.2f}, 初始名义敞口: ${notional:.2f}")

            # 做多条件：均线多头排列，且价格处在EMA20上方确认支撑
            elif _ema20 > _ema50 and _close > _ema20:
                direction = 1
                state = "IN_POSITION"
                risk_amount = capital * 0.15
                notional = risk_amount * 10
                size = notional / _close
                
                positions.append({'entry': _close, 'size': size})
                
                max_favorable_excursion = _high
                trailing_stop = _close * (1 - trailing_pct)
                next_pyramid_target = _close * (1 + pyramid_step_pct)
                
                print(f"[{idx}] 🔥 [信号触发] 均线多头排列，突破做多! 价格: {_close:.2f}, 初始名义敞口: ${notional:.2f}")

        elif state == "IN_POSITION":
            if direction == -1: # SHORT
                if _low < max_favorable_excursion:
                    max_favorable_excursion = _low
                    
                new_trailing = max_favorable_excursion * (1 + trailing_pct)
                if new_trailing < trailing_stop:
                    trailing_stop = new_trailing
                    
                if _high >= trailing_stop:
                    total_profit = sum([(p['entry'] - trailing_stop) * p['size'] for p in positions])
                    capital += total_profit
                    print(f"[{idx}] 💥 [空头平仓] 狂暴反弹打穿 6% 移动止损保护线 (触发价 {trailing_stop:.2f})！")
                    print(f"[{idx}] 💰 退出所有空头头寸，共计 {len(positions)} 个仓位。单笔结转: ${total_profit:.2f}")
                    positions = []
                    state = "WAITING"
                    cooldown_candles = 24 # 平仓后冷却 24 根 1H K线
                else:
                    if _low <= next_pyramid_target:
                        unrealized = sum([(p['entry'] - next_pyramid_target) * p['size'] for p in positions])
                        virtual_equity = capital + unrealized
                        if virtual_equity > 0:
                            new_risk = virtual_equity * 0.15
                            new_notional = new_risk * 10
                            new_size = new_notional / next_pyramid_target
                            positions.append({'entry': next_pyramid_target, 'size': new_size})
                            print(f"[{idx}] 🚀 [浮盈空头加仓!] 价格暴降至 {next_pyramid_target:.2f}！调用浮盈增加 ${new_notional:.2f} 名义价值头寸。当前总仓位: {len(positions)}")
                        next_pyramid_target = next_pyramid_target * (1 - pyramid_step_pct)
                        
                unrealized = sum([(p['entry'] - _close) * p['size'] for p in positions])
                current_equity = capital + unrealized

            elif direction == 1: # LONG
                if _high > max_favorable_excursion:
                    max_favorable_excursion = _high
                    
                new_trailing = max_favorable_excursion * (1 - trailing_pct)
                if new_trailing > trailing_stop:
                    trailing_stop = new_trailing
                    
                if _low <= trailing_stop:
                    total_profit = sum([(trailing_stop - p['entry']) * p['size'] for p in positions])
                    capital += total_profit
                    print(f"[{idx}] 💥 [多头平仓] 瀑布暴跌打穿 6% 移动止损保护线 (触发价 {trailing_stop:.2f})！")
                    print(f"[{idx}] 💰 退出所有多头头寸，共计 {len(positions)} 个仓位。单笔结转: ${total_profit:.2f}")
                    positions = []
                    state = "WAITING"
                    cooldown_candles = 24 # 平仓后冷却 24 根 1H K线
                else:
                    if _high >= next_pyramid_target:
                        unrealized = sum([(next_pyramid_target - p['entry']) * p['size'] for p in positions])
                        virtual_equity = capital + unrealized
                        if virtual_equity > 0:
                            new_risk = virtual_equity * 0.15
                            new_notional = new_risk * 10
                            new_size = new_notional / next_pyramid_target
                            positions.append({'entry': next_pyramid_target, 'size': new_size})
                            print(f"[{idx}] 🚀 [浮盈多头加仓!] 价格暴涨至 {next_pyramid_target:.2f}！调用浮盈增加 ${new_notional:.2f} 名义价值头寸。当前总仓位: {len(positions)}")
                        next_pyramid_target = next_pyramid_target * (1 + pyramid_step_pct)
                        
                unrealized = sum([(_close - p['entry']) * p['size'] for p in positions])
                current_equity = capital + unrealized

        # 防止破产归零出bug
        if current_equity <= 0:
            current_equity = 0
            if capital <= 0:
                print(f"[{idx}] 💀 账户彻底爆仓归零...")
                break

        equity_curve.append(current_equity)

    print("---------------------------------------------------------")
    print("=== 🩸 极度暴利与毁灭：浮盈加仓模型 回测终局 ===")
    print(f"初始本金: ${initial_cap:.2f}")
    if len(equity_curve) > 0:
        print(f"最终净值: ${equity_curve[-1]:.2f}")
        total_profit = equity_curve[-1] - initial_cap
        print(f"🏆 净利润: ${total_profit:.2f}")
        print(f"总资产回报率(ROE): {total_profit / initial_cap * 100:.2f}%")
        
        peak = pd.Series(equity_curve).expanding().max()
        drawdown = (pd.Series(equity_curve) - peak) / peak
        print(f"最大回撤(Max Drawdown): {drawdown.min()*100:.2f}%")
        
    print("=========================================================")
    
    plt.style.use('dark_background')
    plt.figure(figsize=(15, 8))
    plt.plot(timestamps, equity_curve, color='#ff0055', linewidth=2)
    plt.fill_between(timestamps, initial_cap, equity_curve, where=(np.array(equity_curve) > initial_cap), facecolor='#ff0055', alpha=0.15)
    plt.title('High-Risk Pyramiding (Trend Follower with Aggressive Compounding)', fontsize=14, pad=15)
    plt.xlabel('Date Time', fontsize=12)
    plt.ylabel('Strategy Equity (USD)', fontsize=12)
    plt.grid(True, linestyle=(0, (5, 10)), alpha=0.3)
    
    out_img = r'c:\Users\Administrator\Desktop\pyramiding_result.png'
    plt.savefig(out_img, dpi=300, bbox_inches='tight')
    print(f"✅ 高风险收益对比图表已保存至: {out_img}")

if __name__ == '__main__':
    run_pyramiding_strategy()
