import os
import sys
import time
import json
import re
import asyncio
import websockets
import aiohttp
import pandas as pd
import numpy as np
from datetime import datetime
from collections import deque
from colorama import init, Fore, Style
import subprocess

init(autoreset=True)

STATE = {}
OHLCV_CACHE = {}
# 设置日志文件名按启动时间隔离，放入 logs 目录或加上时间戳
START_TIME_STR = datetime.now().strftime('%Y%m%d_%H%M%S')
LOG_FILE = f"binance_hft_trading_{START_TIME_STR}.log"

START_TIME = None
DASHBOARD_REFRESH_RATE = 5  # 面板刷新频率(秒)
RECENT_LOGS = deque(maxlen=15)

def append_to_log(text):
    with open(LOG_FILE, 'a', encoding='utf-8') as f:
        ansi_escape = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')
        f.write(ansi_escape.sub('', text) + "\n")

def log_event(text):
    RECENT_LOGS.append(text)
    append_to_log(text)

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
                    try:
                        lev = float(cols[1].replace('x', ''))
                        stop = float(cols[2].replace('%', '')) / 100.0
                        step = float(cols[3].replace('%', '')) / 100.0
                        # 转换名称: BTC-USD -> BTCUSDT
                        binance_tk = ticker.split('-')[0] + "USDT"
                        params[binance_tk] = {
                            'leverage': lev,
                            'trailing_pct': stop,
                            'step_pct': step,
                            'original_tk': ticker
                        }
                    except:
                        pass
                    break
    return params

def check_sweep(st, tk):
    if st['capital'] >= st['next_sweep_threshold']:
        excess = st['capital'] - st['initial_cap']
        locked = excess * 0.5
        st['vault'] += locked
        st['capital'] -= locked
        st['next_sweep_threshold'] = st['capital'] * 2.0
        log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🔒 [利润分配] {tk} 锁定 ${locked:.2f} 存入保险箱，累计保险: ${st['vault']:.2f}")

def compute_indicators(df):
    btc = df.copy()
    btc['EMA20'] = btc['Close'].ewm(span=20, adjust=False).mean()
    btc['EMA50'] = btc['Close'].ewm(span=50, adjust=False).mean()
    
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
    
    return btc

def process_live_tick(tk, param, fee_rate=0.0004):
    df = OHLCV_CACHE.get(tk)
    if df is None or len(df) < 50: return
    
    st = STATE[tk]
    analyzed = compute_indicators(df)
    
    _close = float(analyzed['Close'].iloc[-1])
    _ema20 = float(analyzed['EMA20'].iloc[-1])
    _ema50 = float(analyzed['EMA50'].iloc[-1])
    _adx = float(analyzed['ADX'].iloc[-1]) if not pd.isna(analyzed['ADX'].iloc[-1]) else 0.0
    
    st['last_price'] = _close
    
    if st['state'] == 'WAITING':
        st['unrealized'] = 0.0
        st['notional_usd'] = 0.0
    
    if st['cooldown_until'] and time.time() < st['cooldown_until']:
        return
    else:
        st['cooldown_until'] = None
        
    leverage_ratio = min(param['leverage'], 15.0) 
    trailing_pct = max(param['trailing_pct'], 0.025)
    pyramid_step_pct = param['step_pct']
    
    if st['state'] == 'WAITING':
        # 放宽ADX限制到20，避免完全发呆
        if _adx < 20.0: return
        
        # 保护机制：将移动止损硬性放宽，避免假突破扫损
        trailing_pct = max(param['trailing_pct'], 0.035) 
        
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
            # 止盈目标定义：当前入场价的直接 5% 下方
            st['take_profit_target'] = _close * 0.95
            st['next_pyramid_target'] = _close * (1 - pyramid_step_pct)
            log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚨 [高频建仓] {tk} 极速主空(SHORT) 触发！价格: ${_close:.4f} | 杠杆: {leverage_ratio}x | 止损: {trailing_pct*100:.1f}% | ADX: {_adx:.1f}")

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
            st['take_profit_target'] = _close * 1.05
            st['next_pyramid_target'] = _close * (1 + pyramid_step_pct)
            log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚨 [高频建仓] {tk} 极速主多(LONG) 触发！价格: ${_close:.4f} | 杠杆: {leverage_ratio}x | 止损: {trailing_pct*100:.1f}% | ADX: {_adx:.1f}")

    elif st['state'] == 'IN_POSITION':
        # 实时判定
        if st['direction'] == -1:
            unrealized = sum([(p['entry'] - _close) * p['size'] for p in st['positions']])
            st['unrealized'] = unrealized
            st['notional_usd'] = sum([p['size'] * _close for p in st['positions']])
            
            if _close < st['max_favorable_excursion']:
                st['max_favorable_excursion'] = _close
            
            # 使用放宽的止损计算
            trailing_pct = max(param['trailing_pct'], 0.035) 
            new_trail = st['max_favorable_excursion'] * (1 + trailing_pct)
            if new_trail < st['trailing_stop']:
                st['trailing_stop'] = new_trail
                
            # 到达极值止盈线(5%) 或 触及移动止损退场
            if _close <= st.get('take_profit_target', 0) or _close >= st['trailing_stop']:
                reason = "🚩 [目标止盈]" if _close <= st.get('take_profit_target', 0) else "💥 [触点强平]"
                total_notional = sum([p['size'] * _close for p in st['positions']])
                st['capital'] += (unrealized - total_notional * fee_rate)
                log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] {reason} {tk} 空单极速离场 (线: ${_close:.4f}) | 波动PnL: ${unrealized:.2f}")
                st['positions'] = []
                st['state'] = 'WAITING'
                st['unrealized'] = 0; st['notional_usd'] = 0
                st['cooldown_until'] = time.time() + 3600 # 冷却缩短回1小时
                check_sweep(st, tk)
                
            elif _close <= st['next_pyramid_target']:
                virt_eq = st['capital'] + unrealized
                if virt_eq > 0:
                    risk = virt_eq * 0.15
                    notional = risk * leverage_ratio
                    size = notional / _close
                    st['capital'] -= notional * fee_rate
                    st['positions'].append({'entry': _close, 'size': size})
                    log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚀 [网格加注] {tk} 空单顺势猛加！价格: ${_close:.4f} | 当前挂载阵列: {len(st['positions'])}")
                st['next_pyramid_target'] = st['next_pyramid_target'] * (1 - pyramid_step_pct)

        elif st['direction'] == 1:
            unrealized = sum([(_close - p['entry']) * p['size'] for p in st['positions']])
            st['unrealized'] = unrealized
            st['notional_usd'] = sum([p['size'] * _close for p in st['positions']])
            
            if _close > st['max_favorable_excursion']:
                st['max_favorable_excursion'] = _close
                
            trailing_pct = max(param['trailing_pct'], 0.035)
            new_trail = st['max_favorable_excursion'] * (1 - trailing_pct)
            if new_trail > st['trailing_stop']:
                st['trailing_stop'] = new_trail
                
            if _close >= st.get('take_profit_target', 999999) or _close <= st['trailing_stop']:
                reason = "🚩 [目标止盈]" if _close >= st.get('take_profit_target', 999999) else "💥 [触点强平]"
                total_notional = sum([p['size'] * _close for p in st['positions']])
                st['capital'] += (unrealized - total_notional * fee_rate)
                log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] {reason} {tk} 多单极速离场 (线: ${_close:.4f}) | 波动PnL: ${unrealized:.2f}")
                st['positions'] = []
                st['state'] = 'WAITING'
                st['unrealized'] = 0; st['notional_usd'] = 0
                st['cooldown_until'] = time.time() + 3600 # 冷却1小时
                check_sweep(st, tk)
                
            elif _close >= st['next_pyramid_target']:
                virt_eq = st['capital'] + unrealized
                if virt_eq > 0:
                    risk = virt_eq * 0.15
                    notional = risk * leverage_ratio
                    size = notional / _close
                    st['capital'] -= notional * fee_rate
                    st['positions'].append({'entry': _close, 'size': size})
                    log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🚀 [网格加注] {tk} 多单顺势猛加！价格: ${_close:.4f} | 当前挂载阵列: {len(st['positions'])}")
                st['next_pyramid_target'] = st['next_pyramid_target'] * (1 + pyramid_step_pct)

def generate_dashboard():
    lines = []
    lines.append("="*120)
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

    lines.append(Fore.CYAN + Style.BRIGHT + f"⚡ [WebSockets实盘高频级] 15M 金字塔战法 (Binance Pro) | 当前切片: {now.strftime('%Y-%m-%d %H:%M:%S')}" + Style.RESET_ALL)
    lines.append(Fore.CYAN + Style.BRIGHT + f"⏳ 战略合拢: {start_time_str} | 运行主引擎耗时: {run_time_str} | Binance 毫秒流媒体收听中" + Style.RESET_ALL)
    lines.append("="*120)
    
    total_initial = sum([st['initial_cap'] for st in STATE.values()])
    total_current = sum([st['capital'] + st['vault'] + st['unrealized'] for st in STATE.values()])
    total_unrealized = sum([st['unrealized'] for st in STATE.values()])
    total_vault = sum([st['vault'] for st in STATE.values()])
    active_longs = sum([1 for st in STATE.values() if st['state'] == 'IN_POSITION' and st['direction'] == 1])
    active_shorts = sum([1 for st in STATE.values() if st['state'] == 'IN_POSITION' and st['direction'] == -1])
    
    overall_roe = (total_current/total_initial - 1)*100 if total_initial > 0 else 0
    
    lines.append(f"💵 初始划拨准备金: ${total_initial:,.2f} | 💰 HFT账户总面值: ${total_current:,.2f} ({Fore.GREEN if overall_roe > 0 else Fore.RED}{overall_roe:+.2f}%{Style.RESET_ALL})")
    lines.append(f"📈 浮空双向未结盈亏: {Fore.GREEN if total_unrealized >= 0 else Fore.RED}${total_unrealized:,.2f}{Style.RESET_ALL} | 🔒 安全提款离场区: ${total_vault:,.2f}")
    lines.append(f"🔥 列阵雷达: {Fore.GREEN}{active_longs} 主多猎犬{Style.RESET_ALL} | {Fore.RED}{active_shorts} 主空刺客{Style.RESET_ALL} | {len(STATE)-active_longs-active_shorts} Binance数据流测算中")
    lines.append("-" * 120)
    
    # 改为显示宇宙池里的所有监控币种，不再过滤 IN_POSITION
    all_tickers = [(k, v) for k, v in STATE.items()]
    # 按照当前的子账户面值(本金+浮盈)来排序显示
    all_tickers.sort(key=lambda x: x[1]['capital'] + x[1]['unrealized'], reverse=True)
    
    lines.append(Fore.YELLOW + "【实交割战列线 (依据毫秒行情毫秒计算) - 全宇宙监控】" + Style.RESET_ALL)
    lines.append(f"{'交易对(Binance)':<15} | {'状态/方向':<7} | {'子弹数':<5} | {'名义仓位(放大后)':<16} | {'现价追踪':<12} | {'最近买入价':<12} | {'挂载浮盈':<12} | {'单兵账户面值':<15}")
    lines.append("-" * 120)
    
    if not all_tickers:
        lines.append(Fore.LIGHTBLACK_EX + "大盘流速静肃... ADX/EMA 引力核准中..." + Style.RESET_ALL)
    else:
        for tk, st in all_tickers:
            if st['state'] == 'WAITING':
                dir_str = Fore.LIGHTBLACK_EX + "测算中 " + Style.RESET_ALL
                bullets = "0"
                notional_str = "$0"
                stop_loss_str = "N/A"
                pnl_str = Fore.LIGHTBLACK_EX + "$0.00" + Style.RESET_ALL
            else:
                dir_str = Fore.GREEN + "做多 " + Style.RESET_ALL if st['direction'] == 1 else Fore.RED + "做空 " + Style.RESET_ALL
                bullets = str(len(st['positions']))
                notional_str = f"${st['notional_usd']:.0f}"
                last_entry = st['positions'][-1]['entry'] if st['positions'] else 0
                stop_loss_str = f"${last_entry:.4f}"
                pnl_color = Fore.GREEN if st['unrealized'] >= 0 else Fore.RED
                pnl_str = f"{pnl_color}${st['unrealized']:.2f}{Style.RESET_ALL}"
            
            current_p = st.get('last_price', 0)
            net_val = st['capital'] + st['vault'] + st['unrealized']
            
            lines.append(f"{tk:<15} | {dir_str:<16} | {bullets:<5} | {notional_str:<16} | ${current_p:<11.4f} | {stop_loss_str:<12} | {pnl_str:<20} | ${net_val:.2f}")

    lines.append("-" * 120)
    lines.append(Fore.LIGHTBLACK_EX + "【字段解析说明】")
    lines.append("▪ 状态/方向: '测算中'表示正在监控尚未触发入场;'做多/做空'表示已持有仓位。")
    lines.append("▪ 子弹数: 顺势金字塔加仓次数。1代表底仓，2以上代表触发了网格追打加注。")
    lines.append("▪ 名义仓位(放大后): 切出真实可用资金的15%作为保证金，再按对应倍数加上杠杆后，控制的总仓位美元价值。")
    lines.append("▪ 最近买入价: 最新一发子弹/底仓入场时的币价。")
    lines.append("▪ 挂载浮盈: 按照当前行情实时计算出来的临时双向账面盈亏（即波动PnL）。")
    lines.append("▪ 单兵账户面值: 该兵种最初分到的 30000 块钱，历经交易磨损/赚取利润/叠加上当前浮盈后，剩下的真实硬资产总额。" + Style.RESET_ALL)
    
    lines.append("\n" + "." * 120)
    if RECENT_LOGS:
        lines.append(Fore.CYAN + "【近期系统日志】" + Style.RESET_ALL)
        for log in RECENT_LOGS:
            lines.append(log)

    lines.append("=" * 120)
    return "\n".join(lines)

async def fetch_historical_binance(session, tk):
    # 用纯 aiohttp 走免翻墙 data-api 节点获取 k线数据
    url = f"https://data-api.binance.vision/api/v3/klines?symbol={tk}&interval=15m&limit=150"
    try:
        async with session.get(url) as resp:
            data = await resp.json()
            if isinstance(data, list) and len(data) > 0:
                df = pd.DataFrame(data, columns=[
                    'Open time', 'Open', 'High', 'Low', 'Close', 'Volume',
                    'Close time', 'Quote asset volume', 'Number of trades',
                    'Taker buy base asset volume', 'Taker buy quote asset volume', 'Ignore'
                ])
                df['Open time'] = pd.to_datetime(df['Open time'], unit='ms')
                df['Open'] = df['Open'].astype(float)
                df['High'] = df['High'].astype(float)
                df['Low'] = df['Low'].astype(float)
                df['Close'] = df['Close'].astype(float)
                df['Volume'] = df['Volume'].astype(float)
                
                OHLCV_CACHE[tk] = df
    except Exception as e:
        print(f"获取 {tk} 历史预热错误: {e}")

async def binance_ws_stream(active_tk_list, params):
    # Binance websocket stream endpoint (Bypass DNS pollution)
    # 按照防屏蔽指南替换 .com 为 .info
    streams = "/".join([f"{tk.lower()}@kline_15m" for tk in active_tk_list])
    uri = f"wss://stream.binance.info:9443/stream?streams={streams}"
    
    log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🔌 连入 Binance WebSocket 直播母线... ({len(active_tk_list)} 条流)")
    
    while True:
        try:
            async with websockets.connect(uri) as websocket:
                log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] ✅ 接收引擎全功率运作。接收毫秒 K 线快照流。")
                while True:
                    msg = await websocket.recv()
                    data = json.loads(msg)
                    if 'data' in data:
                        kline = data['data']['k']
                        tk = kline['s'] # eg BTCUSDT
                        if tk not in OHLCV_CACHE or tk not in params: continue
                        
                        # 核心推移：更新这根K线的最高、最低、收盘价
                        df = OHLCV_CACHE[tk]
                        is_closed = kline['x']
                        t_open = pd.to_datetime(kline['t'], unit='ms')
                        
                        _open = float(kline['o'])
                        _high = float(kline['h'])
                        _low = float(kline['l'])
                        _close = float(kline['c'])
                        _vol = float(kline['v'])
                        
                        # 判断这根流过来的数据是不是开了一根新K线拉高长条
                        if df.iloc[-1]['Open time'] == t_open:
                            # 覆写最后一根跳动的针
                            df.at[df.index[-1], 'High'] = _high
                            df.at[df.index[-1], 'Low'] = _low
                            df.at[df.index[-1], 'Close'] = _close
                            df.at[df.index[-1], 'Volume'] = _vol
                        else:
                            # 增加新一行
                            new_row = pd.DataFrame([{
                                'Open time': t_open, 'Open': _open, 'High': _high, 'Low': _low, 'Close': _close, 'Volume': _vol
                            }])
                            df = pd.concat([df, new_row], ignore_index=True)
                            # 保持窗口不过度膨胀，吃内存
                            if len(df) > 200:
                                df = df.iloc[-100:].reset_index(drop=True)
                            OHLCV_CACHE[tk] = df
                        
                        # 高频验证，毫秒级抛去处理
                        process_live_tick(tk, params[tk])
                        
        except Exception as e:
            log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] ❌ WS 断线重连: {e}")
            await asyncio.sleep(5)

async def print_dashboard_loop():
    while True:
        try:
            os.system('cls' if os.name == 'nt' else 'clear')
            dash = generate_dashboard()
            print(dash)
        except Exception as e:
            log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] ❌ Dashboard Refresh Error: {e}")
        finally:
            await asyncio.sleep(DASHBOARD_REFRESH_RATE)

async def backup_logging_loop():
    while True:
        await asyncio.sleep(60) # 每分钟往硬盘留一次完整底档
        try:
            dash = generate_dashboard()
            append_to_log("\n--- [每分钟轮询快照备份] ---")
            append_to_log(dash)
            append_to_log("-----------------------\n")
        except Exception as e:
            log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] ❌ Backup Logging Error: {e}")

async def build_universe():
    global START_TIME
    START_TIME = datetime.now()
    
    report_path = 'top50_15m_report.md'
    if not os.path.exists(report_path):
        print(f"错误: 找不到 {report_path} 文件！")
        sys.exit(1)
        
    print("正在解析雅虎历史最优参数，并重塑至 Binance 宇宙 (USDT区)...")
    params = parse_best_params(report_path)
    b_tickers = list(params.keys())
    
    print("完全按照指南启用防污染引擎：采用 data-api.binance.vision 获取现货深度及预热K线...")
    
    async with aiohttp.ClientSession() as session:
        print(f"币安24h交易深度挖掘中...")
        async with session.get("https://data-api.binance.vision/api/v3/ticker/24hr") as resp:
            tickers_data = await resp.json()
            
        vol_dict = {}
        for m in tickers_data:
            sym = m['symbol']
            if sym in b_tickers:
                vol_dict[sym] = float(m['quoteVolume'])
                
        sorted_vols = sorted(vol_dict.items(), key=lambda x: x[1], reverse=True)[:30]
        
        # 用户需求：无视大盘成交量权重，每个币种强制分配固定初始金额 30000 U
        fixed_allocation_per_ticker = 30000.0
        active_tk_list = []
        
        for sym, vol in sorted_vols:
            tk = sym
            active_tk_list.append(tk)
            allocated = fixed_allocation_per_ticker
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
            
        print(f"筛选完毕，{len(active_tk_list)} 路顶列币安军团入队列。正在拉取历史K线预热...")
        
        tasks = [fetch_historical_binance(session, tk) for tk in active_tk_list]
        await asyncio.gather(*tasks)

    log_event("*"*60)
    log_event(f"🟢 [SYSTEM BOOT] Binance 官方防屏蔽直连版 - 毫秒级 HFT 金字塔引擎启动")
    log_event(f"🟢 初始资金分配模式变更: 取消波动率加权，强制为选中的 30 个币种每个分配固定 30,000 U。")
    log_event(f"🟢 理论总持仓面值极限: 900,000 U (30000 U * 30 Tickers)")
    log_event(f"🟢 【警告】目前处于 Websockets 高频双向捕猎态，所有价格均为原生主力流(w/ .info 突破)！")
    append_to_log("*"*60)
    
    return active_tk_list, params
    
async def main_async():
    active_tk_list, params = await build_universe()
    
    # 建立并发任务：1) WebSocket 拉取数据。 2) 打印 Dashboard。 3) 周期备份日志。
    task_ws = asyncio.create_task(binance_ws_stream(active_tk_list, params))
    task_dash = asyncio.create_task(print_dashboard_loop())
    task_log = asyncio.create_task(backup_logging_loop())
    
    # return_exceptions=True prevents one task's crash from destroying the whole system silently without traces
    results = await asyncio.gather(task_ws, task_dash, task_log, return_exceptions=True)
    
    for res in results:
        if isinstance(res, Exception):
            import traceback
            err_msg = "".join(traceback.format_exception(type(res), res, res.__traceback__))
            log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 💥 致命任务异常: {res}")
            append_to_log(err_msg)

if __name__ == '__main__':
    try:
        asyncio.run(main_async())
    except KeyboardInterrupt:
        print(Fore.YELLOW + "\n安全熔断执行完毕退出。" + Style.RESET_ALL)
        log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 🛑 引擎收到强制拔除信号，离线。")
        
        # 自动执行战役收盘复盘分析
        print(Fore.CYAN + f"\n正在为您自动分析本次战役日志: {LOG_FILE} ..." + Style.RESET_ALL)
        try:
            result = subprocess.run([sys.executable, "analyze_binance.py", LOG_FILE], capture_output=True, text=True)
            print(result.stdout)
        except Exception as e:
            print(Fore.RED + f"日志分析报告生成失败: {e}" + Style.RESET_ALL)
            
    except Exception as e:
        import traceback
        err_msg = traceback.format_exc()
        # 捕捉最外层的未知致命级崩溃
        log_event(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] 💥 系统级致命错误，引擎崩溃: {e}")
        append_to_log(err_msg)
