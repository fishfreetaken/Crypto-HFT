import yfinance as yf
import pandas as pd
import numpy as np
import matplotlib.pyplot as plt

def run_pyramiding_1m_strategy(ticker='BTC-USD', df=None, leverage_ratio=20, trailing_pct=0.005, pyramid_step_pct=0.0025, target_single_trade_profit=0, fee_rate=0.0005,
                                   enable_profit_sweep=True,   # 🔒 利润锁定开关，默认开启
                                   sweep_multiplier=2.0,        # 每当资金翻 N 倍时触发一次锁定
                                   sweep_keep_ratio=0.5,        # 每次触发时，锁定超额利润的百分比（0.5=锁定一半）
                                   verbose=True):
    if verbose:
        print("---------------------------------------------------------")
        print(f">>> Initializing 1M-Level Pyramiding Strategy: {ticker} <<<")
        print(">>> 理论基础：市场分形几何 (Fractal Markets) <<<")
        print(f">>> 核心参数：1M周期均线 + {leverage_ratio}倍高杠杆 + {trailing_pct*100:.2f}%移动止损 + {pyramid_step_pct*100:.2f}%加仓间隔 <<<")
        print("---------------------------------------------------------")
    
    if df is None:
        try:
            # 截取近期数据进行回测，yfinance对1分钟线最多只能提取60天数据
            btc = yf.download(ticker, period='7d', interval='1m', progress=False)
            if isinstance(btc.columns, pd.MultiIndex):
                btc.columns = [c[0] for c in btc.columns]
            btc = btc.dropna()
        except Exception as e:
            if verbose:
                print(f"Data fetching failed for {ticker}: {e}")
            return None, 0
    else:
        btc = df.copy()

    if len(btc) == 0:
        return None, 0

    # 根据"分形的礼物"，下移到纯粹的 15M 级别
    btc['EMA20'] = btc['Close'].ewm(span=20, adjust=False).mean()
    btc['EMA50'] = btc['Close'].ewm(span=50, adjust=False).mean()
    
    initial_cap = 100000.0
    capital = initial_cap
    
    # 🔒 利润锁定保险箱
    vault = 0.0                        # 永久锁定的安全利润，不再参与高风险加仓
    next_sweep_threshold = initial_cap * sweep_multiplier  # 下一次触发锁定的净值目标
    
    equity_curve = []
    timestamps = []
    
    positions = [] 
    
    state = "WAITING"
    direction = 0  
    trailing_stop = 0.0
    max_favorable_excursion = 0.0
    
    next_pyramid_target = 0.0
    cooldown_candles = 0      # 平仓冷却时间
    
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
            # 破位做空 (1M级别)
            if _ema20 < _ema50 and _close < _ema20:
                direction = -1
                state = "IN_POSITION"
                risk_amount = capital * 0.15
                notional = risk_amount * leverage_ratio
                size = notional / _close
                
                # Deduct entry fee
                fee = notional * fee_rate
                capital -= fee
                
                positions.append({'entry': _close, 'size': size})
                
                max_favorable_excursion = _low
                trailing_stop = _close * (1 + trailing_pct)
                next_pyramid_target = _close * (1 - pyramid_step_pct)
                
                if verbose:
                    print(f"[{idx}] 🔥 [1M信号触发] 空头排列破位做空! 价格: {_close:.2f}, 名义敞口: ${notional:.2f}")

            # 破位做多 (1M级别)
            elif _ema20 > _ema50 and _close > _ema20:
                direction = 1
                state = "IN_POSITION"
                risk_amount = capital * 0.15
                notional = risk_amount * leverage_ratio
                size = notional / _close
                
                # Deduct entry fee
                fee = notional * fee_rate
                capital -= fee
                
                positions.append({'entry': _close, 'size': size})
                
                max_favorable_excursion = _high
                trailing_stop = _close * (1 - trailing_pct)
                next_pyramid_target = _close * (1 + pyramid_step_pct)
                
                if verbose:
                    print(f"[{idx}] 🔥 [1M信号触发] 多头排列突破做多! 价格: {_close:.2f}, 名义敞口: ${notional:.2f}")

        elif state == "IN_POSITION":
            if direction == -1: # SHORT
                if _low < max_favorable_excursion:
                    max_favorable_excursion = _low
                    
                new_trailing = max_favorable_excursion * (1 + trailing_pct)
                if new_trailing < trailing_stop:
                    trailing_stop = new_trailing
                    
                # 计算当前浮动利润
                unrealized = sum([(p['entry'] - _close) * p['size'] for p in positions])
                current_equity = capital + unrealized

                # 止盈检查：若设置了大于0的止盈目标则触发强力截断
                if target_single_trade_profit > 0 and unrealized >= target_single_trade_profit:
                    total_notional = sum([p['size'] * _close for p in positions])
                    fee = total_notional * fee_rate
                    capital += (unrealized - fee)
                    if verbose:
                        print(f"[{idx}] 💰 [空头神级止盈!] 单笔获利爆表！强行截断利润离场！结转: ${unrealized:.2f}")
                    positions = []
                    state = "WAITING"
                    cooldown_candles = 60
                    current_equity = capital
                    
                elif _high >= trailing_stop:
                    total_profit = sum([(p['entry'] - trailing_stop) * p['size'] for p in positions])
                    total_notional = sum([p['size'] * trailing_stop for p in positions])
                    fee = total_notional * fee_rate
                    capital += (total_profit - fee)
                    if verbose:
                        print(f"[{idx}] 💥 [空头平仓] 1M宽幅震荡打穿保护 (触发价 {trailing_stop:.2f})！结转: ${total_profit:.2f}")
                    positions = []
                    state = "WAITING"
                    cooldown_candles = 60 # 1M级别降频：冷却 24 根K (6小时)后重新寻找趋势机会
                    # 🔒 利润锁定检查
                    if enable_profit_sweep and capital >= next_sweep_threshold:
                        excess = capital - initial_cap
                        locked = excess * sweep_keep_ratio
                        vault += locked
                        capital -= locked
                        next_sweep_threshold = capital * sweep_multiplier
                        if verbose:
                            print(f"[{idx}] 🔒 [利润锁定] 净值突破 {sweep_multiplier}倍！锁定 ${locked:.2f} 入保险箱 (总锁仓: ${vault:.2f})，剩余活跃资金: ${capital:.2f}")
                else:
                    if _low <= next_pyramid_target:
                        unrealized_for_pyramid = sum([(p['entry'] - next_pyramid_target) * p['size'] for p in positions])
                        virtual_equity = capital + unrealized_for_pyramid
                        if virtual_equity > 0:
                            new_risk = virtual_equity * 0.15
                            new_notional = new_risk * leverage_ratio
                            new_size = new_notional / next_pyramid_target
                            
                            # Deduct pyramiding entry fee
                            fee = new_notional * fee_rate
                            capital -= fee
                            
                            positions.append({'entry': next_pyramid_target, 'size': new_size})
                            if verbose:
                                print(f"[{idx}] 🚀 [向下突进连击!] 降至 {next_pyramid_target:.2f}！狂暴追加{leverage_ratio}倍空单，当前子弹: {len(positions)} 颗")
                        next_pyramid_target = next_pyramid_target * (1 - pyramid_step_pct)

            elif direction == 1: # LONG
                if _high > max_favorable_excursion:
                    max_favorable_excursion = _high
                    
                new_trailing = max_favorable_excursion * (1 - trailing_pct)
                if new_trailing > trailing_stop:
                    trailing_stop = new_trailing

                # 计算当前浮动利润
                unrealized = sum([(_close - p['entry']) * p['size'] for p in positions])
                current_equity = capital + unrealized
                    
                # 止盈检查：若设置了大于0的止盈目标则触发强力截断
                if target_single_trade_profit > 0 and unrealized >= target_single_trade_profit:
                    total_notional = sum([p['size'] * _close for p in positions])
                    fee = total_notional * fee_rate
                    capital += (unrealized - fee)
                    if verbose:
                        print(f"[{idx}] 💰 [多头神级止盈!] 单笔获利爆表！强行截断利润离场！结转: ${unrealized:.2f}")
                    positions = []
                    state = "WAITING"
                    cooldown_candles = 60
                    current_equity = capital

                elif _low <= trailing_stop:
                    total_profit = sum([(trailing_stop - p['entry']) * p['size'] for p in positions])
                    total_notional = sum([p['size'] * trailing_stop for p in positions])
                    fee = total_notional * fee_rate
                    capital += (total_profit - fee)
                    if verbose:
                        print(f"[{idx}] 💥 [多头平仓] 1M宽幅震荡打穿保护 (触发价 {trailing_stop:.2f})！结转: ${total_profit:.2f}")
                    positions = []
                    state = "WAITING"
                    cooldown_candles = 60 # 1M级别降频：冷却 24 根K (6小时)
                    # 🔒 利润锁定检查
                    if enable_profit_sweep and capital >= next_sweep_threshold:
                        excess = capital - initial_cap
                        locked = excess * sweep_keep_ratio
                        vault += locked
                        capital -= locked
                        next_sweep_threshold = capital * sweep_multiplier
                        if verbose:
                            print(f"[{idx}] 🔒 [利润锁定] 净值突破 {sweep_multiplier}倍！锁定 ${locked:.2f} 入保险箱 (总锁仓: ${vault:.2f})，剩余活跃资金: ${capital:.2f}")
                else:
                    if _high >= next_pyramid_target:
                        unrealized_for_pyramid = sum([(next_pyramid_target - p['entry']) * p['size'] for p in positions])
                        virtual_equity = capital + unrealized_for_pyramid
                        if virtual_equity > 0:
                            new_risk = virtual_equity * 0.15
                            new_notional = new_risk * leverage_ratio
                            new_size = new_notional / next_pyramid_target
                            
                            # Deduct pyramiding entry fee
                            fee = new_notional * fee_rate
                            capital -= fee
                            
                            positions.append({'entry': next_pyramid_target, 'size': new_size})
                            if verbose:
                                print(f"[{idx}] 🚀 [向上突进连击!] 飙至 {next_pyramid_target:.2f}！狂暴追加{leverage_ratio}倍多单，当前子弹: {len(positions)} 颗")
                        next_pyramid_target = next_pyramid_target * (1 + pyramid_step_pct)
                        
        if current_equity <= 0:
            current_equity = 0
            if capital <= 0:
                if verbose:
                    print(f"[{idx}] 💀 极度杠杆导致彻底爆仓...")
                equity_curve.append(current_equity)
                break

        # 资金曲线：活跃资金 + 保险箱锁定利润 = 真实总财富
        equity_curve.append(current_equity + vault)

    if len(equity_curve) > 0:
        total_profit = equity_curve[-1] - initial_cap
        roe = total_profit / initial_cap * 100
        
        peak = pd.Series(equity_curve).expanding().max()
        drawdown = (pd.Series(equity_curve) - peak) / peak
        
        if verbose:
            print("---------------------------------------------------------")
            print(f"=== 🩸 分形降维：{ticker} 1M 回测终局 ===")
            print(f"初始本金: ${initial_cap:.2f}")
            if enable_profit_sweep and vault > 0:
                print(f"🔒 锁定保险箱: ${vault:.2f} (永久安全，不再高风险运作)")
                print(f"💰 活跃资金池: ${capital:.2f} (继续滚雪球)")
            print(f"最终总财富: ${equity_curve[-1]:.2f}")
            print(f"🏆 净利润: ${total_profit:.2f}")
            print(f"总资产回报率(ROE): {roe:.2f}%")
            print(f"最大回撤(Max Drawdown): {drawdown.min()*100:.2f}%")
            print("=========================================================")
        
        return pd.Series(equity_curve, index=timestamps), roe
        
    return None, 0

if __name__ == '__main__':
    curve, roe = run_pyramiding_1m_strategy(ticker='BTC-USD', leverage_ratio=20, verbose=True)
    if curve is not None:
        plt.style.use('dark_background')
        plt.figure(figsize=(15, 8))
        plt.plot(curve.index, curve.values, color='#00ffaa', linewidth=2)
        plt.fill_between(curve.index, 100000.0, curve.values, where=(np.array(curve.values) > 100000.0), facecolor='#00ffaa', alpha=0.15)
        plt.title('Fractal Pyramiding (Pure 1M Timeframe Trend Compounding)', fontsize=14, pad=15)
        plt.xlabel('Date Time', fontsize=12)
        plt.ylabel('Strategy Equity (USD)', fontsize=12)
        plt.grid(True, linestyle=(0, (5, 10)), alpha=0.3)
        
        out_img = r'c:\Users\Administrator\Desktop\pyramiding_1m_result.png'
        plt.savefig(out_img, dpi=300, bbox_inches='tight')
        print(f"✅ 1M级别降维测算收益曲线已保存至: {out_img}")
