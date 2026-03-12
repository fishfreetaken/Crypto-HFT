import yfinance as yf
import pandas as pd
import numpy as np
import time
import os
import re
from colorama import init, Fore, Style
from datetime import datetime

init(autoreset=True)

# 全局状态字典，实现无状态回放的“真·长效模拟”
STATE = {}
LOG_FILE = "live_paper_trading.log"
START_TIME = None

def append_to_log(text):
    with open(LOG_FILE, 'a', encoding='utf-8') as f:
        # 去除颜色代码，避免日志写入乱码
        ansi_escape = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')
        clean_text = ansi_escape.sub('', text)
        f.write(clean_text + "\n")

def parse_best_params(report_path):
    params = {}
    with open(report_path, 'r', encoding='utf-8') as f:
        content = f.read()
    
    sections = content.split('### ')
    for sec in sections[1:]:
        lines = sec.split('\n')
        ticker = lines[0].strip()
        if not ticker: continue
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

def init_state(tickers):
    for tk in tickers:
        STATE[tk] = {
            'capital': 1000.0,
            'vault': 0.0,
            'initial_cap': 1000.0,
            'next_sweep_threshold': 2000.0,
            'positions': [],
            'state': 'WAITING',
            'direction': 0,
            'trailing_stop': 0.0,
            'max_favorable_excursion': 0.0,
            'next_pyramid_target': 0.0,
            'cooldown_until': None,
            'last_price': 0.0,
            'unrealized': 0.0,
            'notional_usd': 0.0
        }

def check_sweep(st, tk):
    if st['capital'] >= st['next_sweep_threshold']:
        excess = st['capital'] - st['initial_cap']
        locked = excess * 0.5
        st['vault'] += locked
        st['capital'] -= locked
        st['next_sweep_threshold'] = st['capital'] * 2.0
        append_to_log(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🔒 [利润分配] {tk} 锁定 ${locked:.2f} 进入保险箱，累计安全资金: ${st['vault']:.2f}")

def process_live_tick(tk, df, param, fee_rate=0.0005):
    st = STATE[tk]
    
    btc = df.copy()
    # 计算当前K线切片的EMA和ADX (使用 14 周期真实波幅 TR, +DM, -DM 计算 ADX)
    btc['EMA20'] = btc['Close'].ewm(span=20, adjust=False).mean()
    btc['EMA50'] = btc['Close'].ewm(span=50, adjust=False).mean()
    
    # ADX 计算核心 (14 周期)
    btc['H-L'] = btc['High'] - btc['Low']
    btc['H-C'] = np.abs(btc['High'] - btc['Close'].shift(1))
    btc['L-C'] = np.abs(btc['Low'] - btc['Close'].shift(1))
    btc['TR'] = btc[['H-L', 'H-C', 'L-C']].max(axis=1)
    
    btc['+DM'] = np.where((btc['High'] - btc['High'].shift(1)) > (btc['Low'].shift(1) - btc['Low']), 
                           np.maximum(btc['High'] - btc['High'].shift(1), 0), 0)
    btc['-DM'] = np.where((btc['Low'].shift(1) - btc['Low']) > (btc['High'] - btc['High'].shift(1)), 
                           np.maximum(btc['Low'].shift(1) - btc['Low'], 0), 0)
                           
    btc['TR14'] = btc['TR'].rolling(window=14).sum()
    btc['+DM14'] = btc['+DM'].rolling(window=14).sum()
    btc['-DM14'] = btc['-DM'].rolling(window=14).sum()
    
    btc['+DI14'] = 100 * (btc['+DM14'] / btc['TR14'])
    btc['-DI14'] = 100 * (btc['-DM14'] / btc['TR14'])
    btc['DX'] = 100 * np.abs(btc['+DI14'] - btc['-DI14']) / (btc['+DI14'] + btc['-DI14'])
    btc['ADX'] = btc['DX'].rolling(window=14).mean()
    
    _close = float(btc['Close'].iloc[-1])
    _ema20 = float(btc['EMA20'].iloc[-1])
    _ema50 = float(btc['EMA50'].iloc[-1])
    _adx = float(btc['ADX'].iloc[-1]) if not pd.isna(btc['ADX'].iloc[-1]) else 0.0
    
    st['last_price'] = _close
    
    if st['state'] == 'WAITING':
        st['unrealized'] = 0.0
        st['notional_usd'] = 0.0
    
    # 冷却期检查
    if st['cooldown_until'] and time.time() < st['cooldown_until']:
        return
    else:
        st['cooldown_until'] = None
        
    # 新增策略 3：强制压制杠杆最高 15x，拉宽止损底线至 2.5% 以防插针
    leverage_ratio = min(param['leverage'], 15.0) 
    trailing_pct = max(param['trailing_pct'], 0.025)
    pyramid_step_pct = param['step_pct']
    
    if st['state'] == 'WAITING':
        # 新增策略 1：ADX 趋势过滤器。ADX低说明在震荡狗皮膏药市，坚决不建仓！
        if _adx < 25.0:
            return  # 过滤噪音，直接跳过计算

        # 破位做空
        if _ema20 < _ema50 and _close < _ema20:
            st['direction'] = -1
            st['state'] = 'IN_POSITION'
            risk = st['capital'] * 0.15
            notional = risk * leverage_ratio
            size = notional / _close
            st['capital'] -= notional * fee_rate
            st['positions'].append({'entry': _close, 'size': size})
            st['max_favorable_excursion'] = _close
            st['trailing_stop'] = _close * (1 + trailing_pct)
            st['next_pyramid_target'] = _close * (1 - pyramid_step_pct)
            
            append_to_log(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚨 [建仓] {tk} 主空(SHORT) 触发！价格: ${_close:.4f} | 杠杆: {leverage_ratio}x | 名义规模: ${notional:.2f}")

        # 破位做多
        elif _ema20 > _ema50 and _close > _ema20:
            st['direction'] = 1
            st['state'] = 'IN_POSITION'
            risk = st['capital'] * 0.15
            notional = risk * leverage_ratio
            size = notional / _close
            st['capital'] -= notional * fee_rate
            st['positions'].append({'entry': _close, 'size': size})
            st['max_favorable_excursion'] = _close
            st['trailing_stop'] = _close * (1 - trailing_pct)
            st['next_pyramid_target'] = _close * (1 + pyramid_step_pct)
            
            append_to_log(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚨 [建仓] {tk} 主多(LONG) 触发！价格: ${_close:.4f} | 杠杆: {leverage_ratio}x | 止损宽幅: {trailing_pct*100:.1f}% | ADX: {_adx:.1f} | 名义规模: ${notional:.2f}")

    elif st['state'] == 'IN_POSITION':
        # 实时计算持仓盈亏
        if st['direction'] == -1:
            unrealized = sum([(p['entry'] - _close) * p['size'] for p in st['positions']])
            st['unrealized'] = unrealized
            st['notional_usd'] = sum([p['size'] * _close for p in st['positions']])
            
            if _close < st['max_favorable_excursion']:
                st['max_favorable_excursion'] = _close
            
            new_trail = st['max_favorable_excursion'] * (1 + trailing_pct)
            if new_trail < st['trailing_stop']:
                st['trailing_stop'] = new_trail
                
            # 止损/离场检查
            if _close >= st['trailing_stop']:
                total_notional = sum([p['size'] * _close for p in st['positions']])
                st['capital'] += (unrealized - total_notional * fee_rate)
                append_to_log(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 💥 [平仓] {tk} 空单离场 (触发移动止损/获利线: ${_close:.4f}) | 单笔PnL: ${unrealized:.2f} | 余额: ${st['capital']:.2f}")
                st['positions'] = []
                st['state'] = 'WAITING'
                st['unrealized'] = 0
                st['notional_usd'] = 0
                st['cooldown_until'] = time.time() + 6 * 3600 # 冷却 6小时
                check_sweep(st, tk)
                
            # 加仓检查
            elif _close <= st['next_pyramid_target']:
                virt_eq = st['capital'] + unrealized
                if virt_eq > 0:
                    risk = virt_eq * 0.15
                    notional = risk * leverage_ratio
                    size = notional / _close
                    st['capital'] -= notional * fee_rate
                    st['positions'].append({'entry': _close, 'size': size})
                    append_to_log(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚀 [加仓] {tk} 空单加注重仓！价格: ${_close:.4f} | 当前挂载子弹数: {len(st['positions'])}")
                st['next_pyramid_target'] = st['next_pyramid_target'] * (1 - pyramid_step_pct)

        elif st['direction'] == 1:
            unrealized = sum([(_close - p['entry']) * p['size'] for p in st['positions']])
            st['unrealized'] = unrealized
            st['notional_usd'] = sum([p['size'] * _close for p in st['positions']])
            
            if _close > st['max_favorable_excursion']:
                st['max_favorable_excursion'] = _close
                
            new_trail = st['max_favorable_excursion'] * (1 - trailing_pct)
            if new_trail > st['trailing_stop']:
                st['trailing_stop'] = new_trail
                
            # 止损/离场检查
            if _close <= st['trailing_stop']:
                total_notional = sum([p['size'] * _close for p in st['positions']])
                st['capital'] += (unrealized - total_notional * fee_rate)
                append_to_log(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 💥 [平仓] {tk} 多单离场 (触发移动止损/获利线: ${_close:.4f}) | 单笔PnL: ${unrealized:.2f} | 余额: ${st['capital']:.2f}")
                st['positions'] = []
                st['state'] = 'WAITING'
                st['unrealized'] = 0
                st['notional_usd'] = 0
                st['cooldown_until'] = time.time() + 6 * 3600 # 冷却 6小时
                check_sweep(st, tk)
                
            # 加仓检查
            elif _close >= st['next_pyramid_target']:
                virt_eq = st['capital'] + unrealized
                if virt_eq > 0:
                    risk = virt_eq * 0.15
                    notional = risk * leverage_ratio
                    size = notional / _close
                    st['capital'] -= notional * fee_rate
                    st['positions'].append({'entry': _close, 'size': size})
                    append_to_log(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚀 [加仓] {tk} 多单加注重仓！价格: ${_close:.4f} | 当前挂载子弹数: {len(st['positions'])}")
                st['next_pyramid_target'] = st['next_pyramid_target'] * (1 + pyramid_step_pct)


def generate_dashboard():
    lines = []
    lines.append("="*120)
    
    # 计算运行时间
    now = datetime.now()
    run_duration = now - START_TIME if START_TIME else None
    
    if run_duration:
        days = run_duration.days
        hours, remainder = divmod(run_duration.seconds, 3600)
        minutes, seconds = divmod(remainder, 60)
        run_time_str = f"{days}天 {hours}小时 {minutes}分钟 {seconds}秒"
    else:
        run_time_str = "刚刚启动"
        
    start_time_str = START_TIME.strftime('%Y-%m-%d %H:%M:%S') if START_TIME else now.strftime('%Y-%m-%d %H:%M:%S')

    lines.append(Fore.CYAN + Style.BRIGHT + f"🚀 15M 初生代金字塔 实时真·模拟环境 | 当前时间: {now.strftime('%Y-%m-%d %H:%M:%S')}" + Style.RESET_ALL)
    lines.append(Fore.CYAN + Style.BRIGHT + f"⏳ 启动时间: {start_time_str} | 运行总时长: {run_time_str}" + Style.RESET_ALL)
    lines.append("="*120)
    
    total_initial = 0
    total_current = 0
    total_unrealized = 0
    total_vault = 0
    active_longs = 0
    active_shorts = 0
    
    for tk, st in STATE.items():
        total_initial += st['initial_cap']
        
        eq = st['capital'] + st['vault'] + st['unrealized']
        total_current += eq
        total_unrealized += st['unrealized']
        total_vault += st['vault']
        
        if st['state'] == 'IN_POSITION':
            if st['direction'] == 1: active_longs += 1
            if st['direction'] == -1: active_shorts += 1

    overall_roe = (total_current/total_initial - 1)*100 if total_initial > 0 else 0
    
    lines.append(f"💵 初始分配资金池: ${total_initial:,.2f} | 💰 当前全部身家: ${total_current:,.2f} ({Fore.GREEN if overall_roe > 0 else Fore.RED}{overall_roe:+.2f}%{Style.RESET_ALL})")
    lines.append(f"📈 实时未结盈亏: {Fore.GREEN if total_unrealized >= 0 else Fore.RED}${total_unrealized:,.2f}{Style.RESET_ALL} | 🔒 保险箱锁定: ${total_vault:,.2f}")
    lines.append(f"🔥 当前持仓分布: {Fore.GREEN}{active_longs} 主多{Style.RESET_ALL} | {Fore.RED}{active_shorts} 主空{Style.RESET_ALL} | {len(STATE)-active_longs-active_shorts} 空仓测算中")
    lines.append("-" * 120)
    
    active_tickers = [(k, v) for k, v in STATE.items() if v['state'] == 'IN_POSITION']
    active_tickers.sort(key=lambda x: x[1]['unrealized'], reverse=True)
    
    lines.append(Fore.YELLOW + "【实盘现役阵列 (基于最近信号触发)】" + Style.RESET_ALL)
    lines.append(f"{'交易对':<12} | {'方向':<5} | {'子弹数':<7} | {'名义仓位 ($)':<14} | {'当前价格':<12} | {'追踪止损':<14} | {'浮动盈亏':<15} | {'账户净值':<15}")
    lines.append("-" * 120)
    
    if not active_tickers:
        lines.append(Fore.LIGHTBLACK_EX + "市场安静的可怕... 均线突破准备中..." + Style.RESET_ALL)
    else:
        for tk, st in active_tickers:
            dir_str = Fore.GREEN + "做多 " + Style.RESET_ALL if st['direction'] == 1 else Fore.RED + "做空 " + Style.RESET_ALL
            pnl_color = Fore.GREEN if st['unrealized'] >= 0 else Fore.RED
            dist = abs(st['last_price'] - st['trailing_stop']) / st['last_price'] * 100
            dist_str = f"(-{dist:.1f}%)" if st['direction'] == 1 else f"(+{dist:.1f}%)"
            
            pnl_str = f"{pnl_color}${st['unrealized']:<14.2f}{Style.RESET_ALL}"
            equity_str = f"${(st['capital']+st['vault']+st['unrealized']):,.2f}"
            notional_str = f"${st['notional_usd']:,.0f}"
            
            lines.append(f"{tk:<12} | {dir_str} | {len(st['positions']):<7} | {notional_str:<14} | ${st['last_price']:<11.4f} | ${st['trailing_stop']:<8.4f}{dist_str:<5} | {pnl_str} | {equity_str}")

    lines.append("=" * 120)
    return "\n".join(lines)

def main():
    global START_TIME
    START_TIME = datetime.now()
    
    report_path = 'top50_15m_report.md'
    if not os.path.exists(report_path):
        print(f"错误: 找不到 {report_path} 文件！")
        return
        
    print("正在解析最优参数报告并构建初始宇宙...")
    params = parse_best_params(report_path)
    if not params:
        print("未提取到参数。")
        return
        
    tickers = list(params.keys())
    
    print("正在拉取 5D 聚合日线数据以重塑资金池结构...")
    pool_df = yf.download(tickers, period='5d', interval='1d', progress=False)
    vols = pool_df['Volume'].mean().sort_values(ascending=False)
    
    # 新增策略 2：剔除垃圾标的，只保留全网成交量 Top 30，且以万U本金依量配比
    top30_vols = vols.head(30)
    total_vol = top30_vols.sum()
    weights = top30_vols / total_vol
    
    total_capital = 10000.0  # 10,000 U 总身家
    
    # 基于权重分配资本并初始化字典
    for tk, weight in weights.items():
        if tk in params:
            allocated = round(total_capital * float(weight), 2)
            STATE[tk] = {
                'capital': allocated,
                'vault': 0.0,
                'initial_cap': allocated,
                'next_sweep_threshold': allocated * 2.0,
                'positions': [],
                'state': 'WAITING',
                'direction': 0,
                'trailing_stop': 0.0,
                'max_favorable_excursion': 0.0,
                'next_pyramid_target': 0.0,
                'cooldown_until': None,
                'last_price': 0.0,
                'unrealized': 0.0,
                'notional_usd': 0.0
            }

    
    # 写入初始启动日志
    append_to_log("*"*60)
    append_to_log(f"🟢 [SYSTEM BOOT] 升级版高精实盘引擎 - ADX>25 过滤版")
    append_to_log(f"🟢 初始条件：总注资 $10,000 USDT，全网最热 Top 30 代币按活跃权重精准输血！")
    append_to_log(f"🟢 降维稳态打击：所有过拟合杠杆上限强制削弱为 15x，追踪止损放宽至 2.5%+ 拒抗无端画门插针！")
    append_to_log("*"*60)
    
    print(f"宇宙启动完成！每 60 秒获取一次最新实时 15M K线切片，验证开仓/加仓逻辑。")
    print("所有触发记录以及每分钟的大局观变化将被永久留存在: -> live_paper_trading.log\n")
    
    while True:
        try:
            # 获取最近几天的数据，只要足够算出 EMA50 即可 (15m 级别的50根K线大概需要1天的数据，拉取5天绰绰有余)
            data = yf.download(tickers, period='5d', interval='15m', progress=False)
            
            df_dict = {}
            if isinstance(data.columns, pd.MultiIndex):
                for tk in tickers:
                    if tk in data['Close'].columns:
                        tk_df = pd.DataFrame({
                            'Close': data['Close'][tk],
                            'High': data['High'][tk],
                            'Low': data['Low'][tk]
                        }).dropna()
                        # 确保计算的K线足够包含50周期
                        if len(tk_df) > 50:
                            df_dict[tk] = tk_df
            
            
            # 这里取我们刚刚筛选出的 Top30 字典的主键
            active_tk_list = list(STATE.keys())

            for tk, tk_df in df_dict.items():
                if tk in params and tk in active_tk_list:
                    process_live_tick(tk, tk_df, params[tk])
            
            os.system('cls' if os.name == 'nt' else 'clear')
            dash = generate_dashboard()
            print(dash)
            print(Fore.LIGHTBLACK_EX + "\n刷新循环运作中（周期：60秒）... 查阅日志文件 'live_paper_trading.log' 跟踪资金轨迹。" + Style.RESET_ALL)
            
            # 将目前的面板状态追加写入到日志文件尾部
            append_to_log("\n--- [每分钟轮询快照] ---")
            append_to_log(dash)
            append_to_log("-----------------------\n")
            
            time.sleep(60)
            
        except KeyboardInterrupt:
            print(Fore.YELLOW + "\n安全退出监控。" + Style.RESET_ALL)
            append_to_log(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🛑 引擎收到中断信号，程序关闭。")
            break
        except Exception as e:
            print(Fore.RED + f"\n网络抓取/计算故障: {e}" + Style.RESET_ALL)
            time.sleep(10)

if __name__ == '__main__':
    main()
