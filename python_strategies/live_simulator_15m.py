import yfinance as yf
import pandas as pd
import numpy as np
import time
import os
from colorama import init, Fore, Style
from datetime import datetime

init(autoreset=True)

def parse_best_params(report_path):
    # Reads the md file to extract the top 1 parameter for each ticker
    params = {}
    with open(report_path, 'r', encoding='utf-8') as f:
        content = f.read()
    
    # Split by ### Ticker
    sections = content.split('### ')
    for sec in sections[1:]:
        lines = sec.split('\n')
        ticker = lines[0].strip()
        if not ticker: continue
        # Find the first data row in the table
        for line in lines:
            if line.startswith('|') and 'x' in line and '%' in line and 'Leverage' not in line:
                cols = [c.strip() for c in line.split('|')]
                if len(cols) >= 6:
                    lev_str = cols[1].replace('x', '')
                    stop_str = cols[2].replace('%', '')
                    step_str = cols[3].replace('%', '')
                    
                    try:
                        lev = float(lev_str)
                        stop = float(stop_str) / 100.0
                        step = float(step_str) / 100.0
                        params[ticker] = {
                            'leverage': lev,
                            'trailing_pct': stop,
                            'step_pct': step
                        }
                        break
                    except:
                        pass
    return params

def run_simulation_up_to_now(ticker, df, leverage_ratio, trailing_pct, pyramid_step_pct, fee_rate=0.0005):
    # We copy the exact logic from run_pyramiding_15m_strategy but return the live state
    btc = df.copy()
    btc['EMA20'] = btc['Close'].ewm(span=20, adjust=False).mean()
    btc['EMA50'] = btc['Close'].ewm(span=50, adjust=False).mean()
    
    initial_cap = 100000.0
    capital = initial_cap
    
    enable_profit_sweep = True
    sweep_multiplier = 2.0
    sweep_keep_ratio = 0.5
    
    vault = 0.0
    next_sweep_threshold = initial_cap * sweep_multiplier
    
    positions = [] 
    state = "WAITING"
    direction = 0  
    trailing_stop = 0.0
    max_favorable_excursion = 0.0
    next_pyramid_target = 0.0
    cooldown_candles = 0
    
    last_idx = None
    last_close = 0
    
    for idx, row in btc.iterrows():
        last_idx = idx
        _close = float(row['Close'])
        _high = float(row['High'])
        _low = float(row['Low'])
        _ema20 = float(row['EMA20'])
        _ema50 = float(row['EMA50'])
        last_close = _close
        
        if cooldown_candles > 0:
            cooldown_candles -= 1
            
        current_equity = capital
        
        if state == "WAITING" and cooldown_candles == 0:
            if _ema20 < _ema50 and _close < _ema20:
                direction = -1
                state = "IN_POSITION"
                risk_amount = capital * 0.15
                notional = risk_amount * leverage_ratio
                size = notional / _close
                capital -= notional * fee_rate
                positions.append({'entry': _close, 'size': size})
                max_favorable_excursion = _low
                trailing_stop = _close * (1 + trailing_pct)
                next_pyramid_target = _close * (1 - pyramid_step_pct)
                
            elif _ema20 > _ema50 and _close > _ema20:
                direction = 1
                state = "IN_POSITION"
                risk_amount = capital * 0.15
                notional = risk_amount * leverage_ratio
                size = notional / _close
                capital -= notional * fee_rate
                positions.append({'entry': _close, 'size': size})
                max_favorable_excursion = _high
                trailing_stop = _close * (1 - trailing_pct)
                next_pyramid_target = _close * (1 + pyramid_step_pct)

        elif state == "IN_POSITION":
            if direction == -1: # SHORT
                if _low < max_favorable_excursion:
                    max_favorable_excursion = _low
                new_trailing = max_favorable_excursion * (1 + trailing_pct)
                if new_trailing < trailing_stop:
                    trailing_stop = new_trailing
                    
                unrealized = sum([(p['entry'] - _close) * p['size'] for p in positions])
                current_equity = capital + unrealized

                if _high >= trailing_stop:
                    total_profit = sum([(p['entry'] - trailing_stop) * p['size'] for p in positions])
                    total_notional = sum([p['size'] * trailing_stop for p in positions])
                    capital += (total_profit - total_notional * fee_rate)
                    positions = []
                    state = "WAITING"
                    cooldown_candles = 24
                    if enable_profit_sweep and capital >= next_sweep_threshold:
                        excess = capital - initial_cap
                        locked = excess * sweep_keep_ratio
                        vault += locked
                        capital -= locked
                        next_sweep_threshold = capital * sweep_multiplier
                else:
                    if _low <= next_pyramid_target:
                        unrealized_for_pyramid = sum([(p['entry'] - next_pyramid_target) * p['size'] for p in positions])
                        virtual_equity = capital + unrealized_for_pyramid
                        if virtual_equity > 0:
                            new_risk = virtual_equity * 0.15
                            new_notional = new_risk * leverage_ratio
                            new_size = new_notional / next_pyramid_target
                            capital -= new_notional * fee_rate
                            positions.append({'entry': next_pyramid_target, 'size': new_size})
                        next_pyramid_target = next_pyramid_target * (1 - pyramid_step_pct)

            elif direction == 1: # LONG
                if _high > max_favorable_excursion:
                    max_favorable_excursion = _high
                new_trailing = max_favorable_excursion * (1 - trailing_pct)
                if new_trailing > trailing_stop:
                    trailing_stop = new_trailing

                unrealized = sum([(_close - p['entry']) * p['size'] for p in positions])
                current_equity = capital + unrealized
                    
                if _low <= trailing_stop:
                    total_profit = sum([(trailing_stop - p['entry']) * p['size'] for p in positions])
                    total_notional = sum([p['size'] * trailing_stop for p in positions])
                    capital += (total_profit - total_notional * fee_rate)
                    positions = []
                    state = "WAITING"
                    cooldown_candles = 24
                    if enable_profit_sweep and capital >= next_sweep_threshold:
                        excess = capital - initial_cap
                        locked = excess * sweep_keep_ratio
                        vault += locked
                        capital -= locked
                        next_sweep_threshold = capital * sweep_multiplier
                else:
                    if _high >= next_pyramid_target:
                        unrealized_for_pyramid = sum([(next_pyramid_target - p['entry']) * p['size'] for p in positions])
                        virtual_equity = capital + unrealized_for_pyramid
                        if virtual_equity > 0:
                            new_risk = virtual_equity * 0.15
                            new_notional = new_risk * leverage_ratio
                            new_size = new_notional / next_pyramid_target
                            capital -= new_notional * fee_rate
                            positions.append({'entry': next_pyramid_target, 'size': new_size})
                        next_pyramid_target = next_pyramid_target * (1 + pyramid_step_pct)
                        
        if current_equity <= 0:
            current_equity = 0
            if capital <= 0:
                break

    unrealized = 0
    if state == "IN_POSITION":
        if direction == 1:
            unrealized = sum([(last_close - p['entry']) * p['size'] for p in positions])
        else:
            unrealized = sum([(p['entry'] - last_close) * p['size'] for p in positions])

    notional_position_usd = 0.0
    if state == "IN_POSITION":
        notional_position_usd = sum([p['size'] * last_close for p in positions])

    return {
        'ticker': ticker,
        'last_update': last_idx,
        'last_price': last_close,
        'state': state,
        'direction': "LONG" if direction == 1 else ("SHORT" if direction == -1 else "NONE"),
        'positions_count': len(positions),
        'notional_usd': notional_position_usd,
        'unrealized': unrealized,
        'active_capital': capital,
        'vault': vault,
        'total_equity': capital + vault + unrealized,
        'initial_cap': initial_cap,
        'trailing_stop': trailing_stop if state == "IN_POSITION" else 0.0
    }

def print_dashboard(results):
    os.system('cls' if os.name == 'nt' else 'clear')
    print("="*105)
    print(Fore.CYAN + Style.BRIGHT + f"⚡ 15M 分形金字塔实时交易监控面板 (LIVE) | 当区时间: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print("="*105)
    
    total_initial = 0
    total_current = 0
    total_unrealized = 0
    total_vault = 0
    active_longs = 0
    active_shorts = 0
    
    for r in results:
        total_initial += r['initial_cap']
        total_current += r['total_equity']
        total_unrealized += r['unrealized']
        total_vault += r['vault']
        if r['state'] == 'IN_POSITION':
            if r['direction'] == 'LONG': active_longs += 1
            if r['direction'] == 'SHORT': active_shorts += 1

    overall_roe = (total_current/total_initial - 1)*100 if total_initial > 0 else 0
    
    print(f"💵 整体投资组合本金: ${total_initial:,.2f} | 💰 组合总净值: ${total_current:,.2f} ({Fore.GREEN if overall_roe > 0 else Fore.RED}{overall_roe:+.2f}%{Style.RESET_ALL})")
    print(f"📈 当前浮动盈亏: {Fore.GREEN if total_unrealized >= 0 else Fore.RED}${total_unrealized:,.2f}{Style.RESET_ALL} | 🔒 保险箱锁定: ${total_vault:,.2f}")
    print(f"🔥 当前持仓分布: {Fore.GREEN}{active_longs} 主多{Style.RESET_ALL} | {Fore.RED}{active_shorts} 主空{Style.RESET_ALL} | {len(results)-active_longs-active_shorts} 空仓观望")
    print("-" * 105)
    
    active_results = [r for r in results if r['state'] == 'IN_POSITION']
    active_results.sort(key=lambda x: x['unrealized'], reverse=True)
    
    print(Fore.YELLOW + "【当期活跃猛虎持仓榜 (已排序)】" + Style.RESET_ALL)
    print(f"{'Ticker':<12} | {'Dir':<5} | {'Bullets':<7} | {'Notional ($)':<14} | {'Curr Price':<12} | {'Trail Stop':<14} | {'Unrealized PnL':<15} | {'Sub Total':<15}")
    print("-" * 120)
    
    if not active_results:
        print(Fore.LIGHTBLACK_EX + "当前没有触发交易信号，正在蹲守突破点..." + Style.RESET_ALL)
    else:
        for r in active_results:
            dir_str = Fore.GREEN + "LONG " + Style.RESET_ALL if r['direction'] == 'LONG' else Fore.RED + "SHORT" + Style.RESET_ALL
            pnl_color = Fore.GREEN if r['unrealized'] >= 0 else Fore.RED
            stop_dist = abs(r['last_price'] - r['trailing_stop']) / r['last_price'] * 100
            dist_str = f"(-{stop_dist:.1f}%)" if r['direction'] == 'LONG' else f"(+{stop_dist:.1f}%)"
            
            pnl_str = f"{pnl_color}${r['unrealized']:<14.2f}{Style.RESET_ALL}"
            equity_str = f"${r['total_equity']:,.2f}"
            notional_str = f"${r['notional_usd']:,.0f}"
            
            print(f"{r['ticker']:<12} | {dir_str} | {r['positions_count']:<7} | {notional_str:<14} | ${r['last_price']:<11.4f} | ${r['trailing_stop']:<8.4f}{dist_str:<5} | {pnl_str} | {equity_str}")

    print("=" * 105)

def main():
    report_path = 'top50_15m_report.md'
    if not os.path.exists(report_path):
        print(f"Error: 找不到 {report_path} 文件！")
        return
        
    print("正在解析最优参数报告...")
    params = parse_best_params(report_path)
    if not params:
        print("未能解析到任何有效参数组，请检查报告格式。")
        return
        
    tickers = list(params.keys())
    print(f"成功加载 {len(tickers)} 个交易对的顶级配置，开始连接交易所拉取实时K线切片 (这可能需要几秒钟)...")
    
    # We loop intentionally to simulate continuous fetching.
    # In a real setup, you might run this on a cronjob or loop every 1-5 minutes.
    while True:
        try:
            data = yf.download(tickers, period='60d', interval='15m', progress=False)
            
            df_dict = {}
            if isinstance(data.columns, pd.MultiIndex):
                for tk in tickers:
                    if tk in data['Close'].columns:
                        tk_df = pd.DataFrame({
                            'Close': data['Close'][tk],
                            'High': data['High'][tk],
                            'Low': data['Low'][tk]
                        }).dropna()
                        if len(tk_df) > 50:
                            df_dict[tk] = tk_df
            else:
                 pass
                 
            results = []
            for tk, df in df_dict.items():
                if tk in params:
                    p = params[tk]
                    res = run_simulation_up_to_now(tk, df, p['leverage'], p['trailing_pct'], p['step_pct'])
                    results.append(res)
                
            print_dashboard(results)
            
            print(Fore.LIGHTBLACK_EX + f"\n下一次实时同步刷新将在 60 秒后..." + Style.RESET_ALL)
            time.sleep(60)
            
        except KeyboardInterrupt:
            print(Fore.YELLOW + "\n已接收中断指令，安全退出监控终端。" + Style.RESET_ALL)
            break
        except Exception as e:
            print(Fore.RED + f"\n拉取数据或计算出错: {e}" + Style.RESET_ALL)
            time.sleep(10)

if __name__ == '__main__':
    main()
