import yfinance as yf
import pandas as pd
import numpy as np
import matplotlib.pyplot as plt

def run_portfolio_strategy():
    tickers = [
        'BTC-USD', 'ETH-USD', 'BNB-USD', 'SOL-USD', 'XRP-USD', 
        'DOGE-USD', 'ADA-USD', 'SHIB-USD', 'AVAX-USD', 'DOT-USD', 
        'LINK-USD', 'TRX-USD', 'BCH-USD', 'LTC-USD', 'NEAR-USD', 
        'UNI-USD', 'APT-USD', 'XLM-USD', 'ATOM-USD', 'ICP-USD', 
        'FIL-USD', 'STX-USD', 'ARB-USD', 'RNDR-USD', 'HBAR-USD', 
        'INJ-USD', 'OP-USD', 'VET-USD', 'ALGO-USD', 'GRT-USD'
    ]
    
    print("---------------------------------------------------------")
    print(">>> Initializing 1H Portfolio Pyramiding Strategy <<<")
    print(">>> 资金全仓轮动模式 (Singleton Global Trade) <<<")
    print("---------------------------------------------------------")
    
    all_dfs = {}
    print("Downloading data for portfolio...")
    for tk in tickers:
        try:
            df = yf.download(tk, start='2026-01-01', end='2026-03-09', interval='1h', progress=False)
            if df.empty:
                continue
            if isinstance(df.columns, pd.MultiIndex):
                df.columns = [c[0] for c in df.columns]
            df = df.dropna()
            
            if len(df) > 0:
                df['EMA20'] = df['Close'].ewm(span=20, adjust=False).mean()
                df['EMA50'] = df['Close'].ewm(span=50, adjust=False).mean()
                all_dfs[tk] = df
        except:
            pass
            
    print(f"Data ready for {len(all_dfs)} tickers.")
    
    all_timestamps = set()
    for df in all_dfs.values():
        all_timestamps.update(df.index)
    all_timestamps = sorted(list(all_timestamps))
    
    # Define globally robust params
    leverage_ratio = 20
    trailing_pct = 0.03
    pyramid_step_pct = 0.015
    fee_rate = 0.0005
    target_single_trade_profit = 0
    
    capital = 100000.0
    initial_cap = capital
    
    equity_curve = []
    equity_timestamps = []
    
    active_trade = None
    cooldown_candles = 0
    
    print("\n🚀 开始跨品种资金轮动回测...")
    
    for ts in all_timestamps:
        if cooldown_candles > 0:
            cooldown_candles -= 1
            
        current_equity = capital
        
        # 1. Manage active trade
        if active_trade is not None:
            tk = active_trade['ticker']
            df = all_dfs[tk]
            
            if ts in df.index:
                row = df.loc[ts]
                _close = float(row['Close'].iloc[0] if isinstance(row, pd.DataFrame) else row['Close'])
                _high = float(row['High'].iloc[0] if isinstance(row, pd.DataFrame) else row['High'])
                _low = float(row['Low'].iloc[0] if isinstance(row, pd.DataFrame) else row['Low'])
                
                direction = active_trade['direction']
                positions = active_trade['positions']
                max_favorable_excursion = active_trade['max_favorable_excursion']
                trailing_stop = active_trade['trailing_stop']
                next_pyramid_target = active_trade['next_pyramid_target']
                
                trade_ended = False
                
                if direction == -1: # SHORT
                    if _low < max_favorable_excursion:
                        max_favorable_excursion = _low
                        
                    new_trailing = max_favorable_excursion * (1 + trailing_pct)
                    if new_trailing < trailing_stop:
                        trailing_stop = new_trailing
                        
                    unrealized = sum([(p['entry'] - _close) * p['size'] for p in positions])
                    current_equity = capital + unrealized
                    
                    if target_single_trade_profit > 0 and unrealized >= target_single_trade_profit:
                        total_notional = sum([p['size'] * _close for p in positions])
                        fee = total_notional * fee_rate
                        capital += (unrealized - fee)
                        print(f"[{ts}] 💰 [{tk} 空头止盈] 利润截断离场！结转: ${unrealized - fee:.2f}")
                        trade_ended = True
                        
                    elif _high >= trailing_stop:
                        total_profit = sum([(p['entry'] - trailing_stop) * p['size'] for p in positions])
                        total_notional = sum([p['size'] * trailing_stop for p in positions])
                        fee = total_notional * fee_rate
                        capital += (total_profit - fee)
                        print(f"[{ts}] 💥 [{tk} 空头止损] 宽幅震荡打穿保护！结转: ${total_profit - fee:.2f}")
                        trade_ended = True
                        
                    else:
                        if _low <= next_pyramid_target:
                            unrealized_for_pyramid = sum([(p['entry'] - next_pyramid_target) * p['size'] for p in positions])
                            virtual_equity = capital + unrealized_for_pyramid
                            if virtual_equity > 0:
                                new_risk = virtual_equity * 0.15
                                new_notional = new_risk * leverage_ratio
                                new_size = new_notional / next_pyramid_target
                                
                                fee = new_notional * fee_rate
                                capital -= fee
                                
                                positions.append({'entry': next_pyramid_target, 'size': new_size})
                                print(f"[{ts}] 🚀 [{tk}] 降至 {next_pyramid_target:.2f} 追加空单，子弹: {len(positions)} 颗")
                                
                            next_pyramid_target = next_pyramid_target * (1 - pyramid_step_pct)
                            active_trade['next_pyramid_target'] = next_pyramid_target

                elif direction == 1: # LONG
                    if _high > max_favorable_excursion:
                        max_favorable_excursion = _high
                        
                    new_trailing = max_favorable_excursion * (1 - trailing_pct)
                    if new_trailing > trailing_stop:
                        trailing_stop = new_trailing

                    unrealized = sum([(_close - p['entry']) * p['size'] for p in positions])
                    current_equity = capital + unrealized
                        
                    if target_single_trade_profit > 0 and unrealized >= target_single_trade_profit:
                        total_notional = sum([p['size'] * _close for p in positions])
                        fee = total_notional * fee_rate
                        capital += (unrealized - fee)
                        print(f"[{ts}] 💰 [{tk} 多头止盈] 利润截断离场！结转: ${unrealized - fee:.2f}")
                        trade_ended = True

                    elif _low <= trailing_stop:
                        total_profit = sum([(trailing_stop - p['entry']) * p['size'] for p in positions])
                        total_notional = sum([p['size'] * trailing_stop for p in positions])
                        fee = total_notional * fee_rate
                        capital += (total_profit - fee)
                        print(f"[{ts}] 💥 [{tk} 多头止损] 宽幅震荡打穿保护！结转: ${total_profit - fee:.2f}")
                        trade_ended = True
                        
                    else:
                        if _high >= next_pyramid_target:
                            unrealized_for_pyramid = sum([(next_pyramid_target - p['entry']) * p['size'] for p in positions])
                            virtual_equity = capital + unrealized_for_pyramid
                            if virtual_equity > 0:
                                new_risk = virtual_equity * 0.15
                                new_notional = new_risk * leverage_ratio
                                new_size = new_notional / next_pyramid_target
                                
                                fee = new_notional * fee_rate
                                capital -= fee
                                
                                positions.append({'entry': next_pyramid_target, 'size': new_size})
                                print(f"[{ts}] 🚀 [{tk}] 飙至 {next_pyramid_target:.2f} 追加多单，子弹: {len(positions)} 颗")
                                
                            next_pyramid_target = next_pyramid_target * (1 + pyramid_step_pct)
                            active_trade['next_pyramid_target'] = next_pyramid_target
                            
                if not trade_ended:
                    active_trade['max_favorable_excursion'] = max_favorable_excursion
                    active_trade['trailing_stop'] = trailing_stop
                else:
                    active_trade = None
                    cooldown_candles = 12
                    
        # 2. Open new trade
        if active_trade is None and cooldown_candles == 0 and capital > 0:
            for tk in tickers:
                if tk not in all_dfs:
                    continue
                df = all_dfs[tk]
                if ts in df.index:
                    row = df.loc[ts]
                    _close = float(row['Close'].iloc[0] if isinstance(row, pd.DataFrame) else row['Close'])
                    _high = float(row['High'].iloc[0] if isinstance(row, pd.DataFrame) else row['High'])
                    _low = float(row['Low'].iloc[0] if isinstance(row, pd.DataFrame) else row['Low'])
                    _ema20 = float(row['EMA20'].iloc[0] if isinstance(row, pd.DataFrame) else row['EMA20'])
                    _ema50 = float(row['EMA50'].iloc[0] if isinstance(row, pd.DataFrame) else row['EMA50'])
                    
                    if _ema20 < _ema50 and _close < _ema20:
                        risk_amount = capital * 0.15
                        notional = risk_amount * leverage_ratio
                        size = notional / _close
                        fee = notional * fee_rate
                        capital -= fee
                        
                        active_trade = {
                            'ticker': tk,
                            'direction': -1,
                            'positions': [{'entry': _close, 'size': size}],
                            'max_favorable_excursion': _low,
                            'trailing_stop': _close * (1 + trailing_pct),
                            'next_pyramid_target': _close * (1 - pyramid_step_pct)
                        }
                        print(f"[{ts}] 🔥 [{tk}] 全仓轮动: 空头破位介入! 进场价: {_close:.2f}")
                        break
                        
                    elif _ema20 > _ema50 and _close > _ema20:
                        risk_amount = capital * 0.15
                        notional = risk_amount * leverage_ratio
                        size = notional / _close
                        fee = notional * fee_rate
                        capital -= fee
                        
                        active_trade = {
                            'ticker': tk,
                            'direction': 1,
                            'positions': [{'entry': _close, 'size': size}],
                            'max_favorable_excursion': _high,
                            'trailing_stop': _close * (1 - trailing_pct),
                            'next_pyramid_target': _close * (1 + pyramid_step_pct)
                        }
                        print(f"[{ts}] 🔥 [{tk}] 全仓轮动: 多头突破介入! 进场价: {_close:.2f}")
                        break
                        
        if current_equity <= 0:
            current_equity = 0
            
        equity_curve.append(current_equity)
        equity_timestamps.append(ts)
        
        if current_equity == 0:
            print(f"[{ts}] 💀 极度杠杆导致彻底爆仓...")
            break

    print("---------------------------------------------------------")
    print("=== 🩸 跨品种全局轮动引擎 (Portfolio Rotation) 回测终局 ===")
    print(f"初始本金: ${initial_cap:.2f}")
    if len(equity_curve) > 0:
        final_eq = equity_curve[-1]
        print(f"最终净值: ${final_eq:.2f}")
        total_profit = final_eq - initial_cap
        print(f"🏆 净利润: ${total_profit:.2f}")
        print(f"总资产回报率(ROE): {total_profit / initial_cap * 100:.2f}%")
        
        peak = pd.Series(equity_curve).expanding().max()
        drawdown = (pd.Series(equity_curve) - peak) / peak
        print(f"最大回撤(Max Drawdown): {drawdown.min()*100:.2f}%")
        
    print("=========================================================")
    
    plt.style.use('dark_background')
    plt.figure(figsize=(15, 8))
    plt.plot(equity_timestamps, equity_curve, color='#4CAF50', linewidth=2)
    plt.fill_between(equity_timestamps, initial_cap, equity_curve, where=(np.array(equity_curve) > initial_cap), facecolor='#4CAF50', alpha=0.15)
    plt.title('Global Auto-Rotation Pyramiding (Single Active Trade across Top 30)', fontsize=14, pad=15, fontweight='bold', color='#FFD700')
    plt.xlabel('Date Time', fontsize=12)
    plt.ylabel('Strategy Global Equity (USD)', fontsize=12)
    plt.grid(True, linestyle=(0, (5, 10)), alpha=0.3, color='#555555')
    
    out_img = r'c:\Users\Administrator\Desktop\pyramiding_portfolio_result.png'
    plt.savefig(out_img, dpi=300, bbox_inches='tight')
    print(f"✅ 全局轮动资金曲线已保存至: {out_img}")

if __name__ == '__main__':
    run_portfolio_strategy()
