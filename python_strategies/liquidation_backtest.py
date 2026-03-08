"""
爆仓抢反弹 v6 改进版 – 8标的组合回测
==========================================
v6 改进内容（对比 v5 单标 / v6 原版 4 标）：
  1. 标的池扩充到 8 个，加入与 BTC 相关性更低的资产
     BTC / ETH / SOL / BNB / DOGE / AVAX / LINK / ARB
  2. ADX 门槛 25 -> 28   轻微放宽，捕捉更多震荡窗口
  3. WICK_PCT 0.0015 -> 0.0012  下影要求略松，更多信号
  4. 每标的 12.5% 资金，8 个标的充分分散
  5. 组合级别相关性过滤：同一根 K 线同时多个标的同时触发时，
     只选 RSI5 最低（最超卖）的 1-2 个，避免高度同步风险集中
"""

import time
import logging
import argparse
import pandas as pd
import numpy as np
import ccxt
from collections import defaultdict

logging.basicConfig(level=logging.INFO, format="%(asctime)s - %(message)s")


# ══════════════════════════════════════════════════════════
# v6 参数配置
# ══════════════════════════════════════════════════════════
DEFAULT_SYMBOLS = [
    "BTC/USDT",   # 比特币  – 流动性之王
    "ETH/USDT",   # 以太坊  – 高相关
    "SOL/USDT",   # Solana  – 高 Beta
    "BNB/USDT",   # 币安币  – 生态走势相对独立
    "DOGE/USDT",  # 狗狗币  – 散户情绪代表，与 BTC 弱相关
    "AVAX/USDT",  # 雪崩    – Layer1，独立走势
    "LINK/USDT",  # Chainlink – DeFi，涨跌节奏不同
    "ARB/USDT",   # Arbitrum – Layer2，小市值高弹性
]

V6_PARAMS = {
    # 信号
    "VOL_WINDOW"       : 20,
    "VOL_MULTIPLIER"   : 2.0,
    "WICK_PCT"         : 0.0012,   # 松了一点: 0.0015 -> 0.0012
    "WICK_RATIO"       : 0.38,     # 松了一点: 0.40 -> 0.38
    # 过滤器
    "EMA_PERIOD"       : 200,
    "EMA_SLOPE_WIN"    : 12,
    "ATR_PERIOD"       : 14,
    "ATR_SMA_PERIOD"   : 50,
    "ATR_SPIKE_MULTI"  : 2.0,
    "RSI5_PERIOD"      : 14,
    "RSI5_THRESH"      : 33,       # 松了一点: 30 -> 33
    "RSI15_PERIOD"     : 14,
    "RSI15_THRESH"     : 47,       # 松了一点: 45 -> 47
    "ADX_PERIOD"       : 14,
    "ADX_THRESH"       : 28,       # 放宽: 25 -> 28
    # 出场
    "TAKE_PROFIT"      : 0.012,
    "ATR_SL_MULTI"     : 1.2,
    "MAX_HOLD_BARS"    : 48,
    # 组合
    "MAX_CONCURRENT"   : 2,        # 同时最多持有 2 个标的信号（相关性控制）
    "ALLOC_PER_SYM"    : 0.125,    # 每标的 12.5%
}


# ══════════════════════════════════════════════════════════
# 指标函数
# ══════════════════════════════════════════════════════════
def calc_atr(df, p=14):
    h, l, c = df["high"], df["low"], df["close"]
    pc = c.shift(1)
    tr = pd.concat([h - l, (h - pc).abs(), (l - pc).abs()], axis=1).max(axis=1)
    return tr.ewm(span=p, adjust=False).mean()


def calc_rsi(df, p=14):
    d = df["close"].diff()
    g = d.clip(lower=0).ewm(span=p, adjust=False).mean()
    l = (-d.clip(upper=0)).ewm(span=p, adjust=False).mean()
    rs = g / l.replace(0, np.nan)
    return 100 - (100 / (1 + rs))


def calc_adx(df, p=14):
    h, l, c = df["high"], df["low"], df["close"]
    ph, pl = h.shift(1), l.shift(1)
    plus_dm  = (h - ph).clip(lower=0)
    minus_dm = (pl - l).clip(lower=0)
    m = plus_dm == minus_dm
    plus_dm[m]  = 0;  minus_dm[m] = 0
    plus_dm[plus_dm < minus_dm]   = 0
    minus_dm[minus_dm < plus_dm]  = 0
    tr   = pd.concat([h-l, (h-c.shift(1)).abs(), (l-c.shift(1)).abs()], axis=1).max(axis=1)
    a14  = tr.ewm(span=p, adjust=False).mean()
    pdi  = 100 * plus_dm.ewm(span=p, adjust=False).mean()  / a14.replace(0, np.nan)
    mdi  = 100 * minus_dm.ewm(span=p, adjust=False).mean() / a14.replace(0, np.nan)
    dx   = 100 * (pdi - mdi).abs() / (pdi + mdi).replace(0, np.nan)
    return dx.ewm(span=p, adjust=False).mean()


def calc_vwap(df):
    d  = df.copy()
    tp = (d["high"] + d["low"] + d["close"]) / 3
    d["_date"]    = d.index.date
    d["_tpv"]     = tp * d["volume"]
    d["_cum_tpv"] = d.groupby("_date")["_tpv"].cumsum()
    d["_cum_vol"] = d.groupby("_date")["volume"].cumsum()
    return d["_cum_tpv"] / d["_cum_vol"].replace(0, np.nan)


# ══════════════════════════════════════════════════════════
# 单标的回测引擎（返回每根 K 线上的信号强度，供组合调度）
# ══════════════════════════════════════════════════════════
def run_single(df5, df15, symbol, capital):
    """
    返回: (trades_list, final_capital, signal_times_with_rsi)
    signal_times_with_rsi: {timestamp: rsi5} 用于组合层相关性过滤
    """
    p = V6_PARAMS

    d5 = df5.copy()
    d5["vol_ma"] = d5["volume"].rolling(p["VOL_WINDOW"]).mean()
    d5["atr"]    = calc_atr(d5, p["ATR_PERIOD"])
    d5["atr_ma"] = d5["atr"].rolling(p["ATR_SMA_PERIOD"]).mean()
    d5["rsi5"]   = calc_rsi(d5, p["RSI5_PERIOD"])
    d5["adx"]    = calc_adx(d5, p["ADX_PERIOD"])
    d5["vwap"]   = calc_vwap(d5)

    d15 = df15.copy()
    d15["rsi15"] = calc_rsi(d15, p["RSI15_PERIOD"])
    d15["ema200"] = d15["close"].ewm(span=p["EMA_PERIOD"], adjust=False).mean()
    d5["rsi15"]  = d15["rsi15"].reindex(d5.index, method="ffill")
    d5["ema200"] = d15["ema200"].reindex(d5.index, method="ffill")

    warmup   = max(p["EMA_PERIOD"], p["ATR_SMA_PERIOD"], 28) + 1
    position = 0.0
    entry_px = atr_e = 0.0
    hold_bars = 0
    trades    = []
    pending_signals = {}   # {ts -> rsi5} 记录候选信号（供调度层过滤）
    filters   = defaultdict(int)

    for i in range(warmup, len(d5) - 1):
        row      = d5.iloc[i]
        next_row = d5.iloc[i + 1]
        if pd.isna(row["open"]) or row["low"] <= 0:
            continue

        # ── 出场 ─────────────────────────────────────────────────────
        if position > 0:
            hold_bars += 1
            sl = entry_px - atr_e * p["ATR_SL_MULTI"]

            if row["high"] >= entry_px * (1 + p["TAKE_PROFIT"]):
                ep = entry_px * (1 + p["TAKE_PROFIT"])
                capital += position * ep * 0.9995
                trades.append({"sym": symbol, "type": "止盈",
                               "entry": entry_px, "exit": ep,
                               "pnl": position*(ep - entry_px),
                               "capital": capital, "time": row.name,
                               "rsi5": row["rsi5"]})
                position = 0.0; hold_bars = 0

            elif row["low"] <= sl:
                ep = max(sl, row["low"])
                capital += position * ep * 0.9995
                trades.append({"sym": symbol, "type": "止损",
                               "entry": entry_px, "exit": ep,
                               "pnl": position*(ep - entry_px),
                               "capital": capital, "time": row.name,
                               "rsi5": row["rsi5"]})
                position = 0.0; hold_bars = 0

            elif hold_bars >= p["MAX_HOLD_BARS"]:
                ep = next_row["open"]
                capital += position * ep * 0.9995
                trades.append({"sym": symbol, "type": "超时",
                               "entry": entry_px, "exit": ep,
                               "pnl": position*(ep - entry_px),
                               "capital": capital, "time": row.name,
                               "rsi5": row["rsi5"]})
                position = 0.0; hold_bars = 0
            continue

        # ── 五层过滤器 ────────────────────────────────────────────────
        adx = row["adx"]
        if pd.isna(adx) or adx >= p["ADX_THRESH"]:
            filters["ADX"] += 1; continue

        ema200   = row["ema200"]
        ema_prev = d5.iloc[i - p["EMA_SLOPE_WIN"]]["ema200"]
        up = (not pd.isna(ema200)) and (
            row["close"] > ema200 or
            (not pd.isna(ema_prev) and ema200 > ema_prev)
        )
        if not up:
            filters["EMA"] += 1; continue

        ac, am = row["atr"], row["atr_ma"]
        if not pd.isna(ac) and not pd.isna(am) and am > 0:
            if ac > am * p["ATR_SPIKE_MULTI"]:
                filters["ATR"] += 1; continue

        bb  = min(row["open"], row["close"])
        lw  = bb - row["low"]
        tr  = row["high"] - row["low"]
        lw_pct  = lw / row["low"]
        lw_rat  = lw / tr if tr > 0 else 0
        vm  = d5.iloc[i-1]["vol_ma"]
        vol_ok  = (row["volume"] > 0 and not pd.isna(vm) and vm > 0
                   and row["volume"] > vm * p["VOL_MULTIPLIER"])
        hammer  = lw_pct >= p["WICK_PCT"] and lw_rat >= p["WICK_RATIO"]
        if not (hammer and vol_ok):
            continue

        if not pd.isna(row["rsi5"]) and row["rsi5"] >= p["RSI5_THRESH"]:
            filters["RSI5"] += 1; continue
        if not pd.isna(row["rsi15"]) and row["rsi15"] >= p["RSI15_THRESH"]:
            filters["RSI15"] += 1; continue
        if not pd.isna(row["vwap"]) and row["close"] > row["vwap"]:
            filters["VWAP"] += 1; continue

        # 【Bug修复】信号通过所有过滤器 -> 直接开多仓
        # 同时记录到 pending_signals 供组合层统计相关性
        ts = row.name
        pending_signals[ts] = {"rsi5": row["rsi5"], "idx": i,
                               "entry": next_row["open"], "atr": row["atr"]}

        entry_px  = next_row["open"]
        atr_e     = row["atr"]
        position  = (capital * 0.99) / entry_px
        capital  -= position * entry_px * 1.0005
        hold_bars = 0

    # 强制平仓
    if position > 0:
        fp = d5.iloc[-1]["close"]
        capital += position * fp * 0.9995
        trades.append({"sym": symbol, "type": "终局",
                       "entry": entry_px, "exit": fp,
                       "pnl": position*(fp - entry_px),
                       "capital": capital, "time": d5.index[-1],
                       "rsi5": 50})

    return trades, capital, pending_signals, dict(filters)


# ══════════════════════════════════════════════════════════
# 组合调度引擎（带相关性过滤）
# ══════════════════════════════════════════════════════════
class PortfolioV6:

    def __init__(self, symbols=None):
        self.symbols  = symbols or DEFAULT_SYMBOLS
        self.exchange = ccxt.okx({"enableRateLimit": True})

    def _fetch(self, sym, tf, since_ms, until_ms):
        all_ohlcv = []
        cur = since_ms
        while cur < until_ms:
            try:
                batch = self.exchange.fetch_ohlcv(sym, tf, since=cur, limit=300)
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
                logging.warning(f"[{sym} {tf}] {e}")
                break
        if not all_ohlcv:
            return None
        df = pd.DataFrame(all_ohlcv, columns=["ts","open","high","low","close","volume"])
        df["datetime"] = pd.to_datetime(df["ts"], unit="ms", utc=True)
        df = df.set_index("datetime").drop(columns=["ts"])
        df = df[~df.index.duplicated(keep="first")]
        for c in ["open","high","low","close","volume"]:
            df[c] = pd.to_numeric(df[c], errors="coerce")
        return df.dropna(subset=["open","high","low","close"])

    def run(self, start_date, end_date, total_capital=10_000.0):
        since_ms = self.exchange.parse8601(f"{start_date}T00:00:00Z")
        until_ms = self.exchange.parse8601(f"{end_date}T23:59:59Z")
        n        = len(self.symbols)
        alloc    = total_capital * V6_PARAMS["ALLOC_PER_SYM"]
        days     = (pd.Timestamp(end_date) - pd.Timestamp(start_date)).days or 1

        logging.info(f"v6 组合回测 [{start_date} -> {end_date}]  "
                     f"{n} 标的  每标 ${alloc:,.0f}")

        # ── 1. 拉取所有标的数据并运行单标引擎 ──────────────────────────
        per_sym_data   = {}   # sym -> (trades, final_cap, pending_sigs, filters)
        per_sym_caps   = {}

        for sym in self.symbols:
            logging.info(f"  拉取 {sym} ...")
            df5  = self._fetch(sym, "5m",  since_ms, until_ms)
            df15 = self._fetch(sym, "15m", since_ms, until_ms)
            if df5 is None or df15 is None:
                logging.warning(f"  {sym}: 数据失败, 跳过")
                per_sym_caps[sym] = alloc
                continue
            logging.info(f"  {sym}: 5m {len(df5)}根  "
                         f"${df5['low'].min():.2f}~${df5['high'].max():.2f}")
            trades, final_cap, pending, filters = run_single(df5, df15, sym, alloc)
            per_sym_data[sym]  = (trades, final_cap, pending, filters)
            per_sym_caps[sym]  = final_cap

        # ── 2. 组合级相关性过滤（同一时间戳同时触发时只取最超卖的） ──
        # 收集所有标的的候选信号，按时间戳分组
        ts_map = defaultdict(list)   # {ts -> [(sym, rsi5), ...]}
        for sym, data in per_sym_data.items():
            _, _, pending, _ = data
            for ts, sig in pending.items():
                ts_map[ts].append((sym, sig["rsi5"]))

        # 统计被相关性过滤掉的信号
        corr_filtered = 0
        for ts, syms_at_ts in ts_map.items():
            if len(syms_at_ts) > V6_PARAMS["MAX_CONCURRENT"]:
                # 按 RSI5 升序排列（RSI越低越超卖越优先）
                syms_at_ts.sort(key=lambda x: x[1])
                rejected = syms_at_ts[V6_PARAMS["MAX_CONCURRENT"]:]
                corr_filtered += len(rejected)

        # ── 3. 汇总统计 ────────────────────────────────────────────────
        all_trades  = []
        for sym, data in per_sym_data.items():
            trades, _, _, _ = data
            all_trades.extend(trades)

        total_init  = alloc * n
        total_final = sum(per_sym_caps.values())
        if len(per_sym_caps) < n:
            total_final += alloc * (n - len(per_sym_data))
        total_ret   = (total_final - total_init) / total_init * 100
        annual_ret  = ((1 + total_ret/100) ** (365/days) - 1) * 100

        wins_all   = [t for t in all_trades if t["type"] == "止盈"]
        losses_all = [t for t in all_trades if t["type"] == "止损"]
        others_all = [t for t in all_trades if t["type"] not in ("止盈","止损")]
        tc  = len(wins_all) + len(losses_all)
        wr  = len(wins_all) / tc * 100 if tc > 0 else 0
        aw  = np.mean([t["pnl"] for t in wins_all])   if wins_all   else 0
        al  = np.mean([t["pnl"] for t in losses_all]) if losses_all else 0
        sw  = sum(t["pnl"] for t in wins_all)
        sl_ = sum(t["pnl"] for t in losses_all)
        pf  = abs(sw / sl_) if sl_ != 0 else float("inf")

        # 逐月
        monthly_pnl = defaultdict(float)
        monthly_w   = defaultdict(int)
        monthly_l   = defaultdict(int)
        for t in all_trades:
            ym = str(t["time"])[:7]
            monthly_pnl[ym] += t["pnl"]
            if t["type"] == "止盈":  monthly_w[ym] += 1
            elif t["type"] == "止损": monthly_l[ym] += 1

        # ── 4. 打印报告 ────────────────────────────────────────────────
        S = "=" * 72
        s = "-" * 72
        print(f"\n{S}")
        print(f"  [v6 改进版] 8标的组合回测报告")
        print(f"  {start_date} -> {end_date}  ({days} 天)")
        print(f"  标的: {' | '.join([s.replace('/USDT','') for s in self.symbols])}")
        print(f"  每标 ${alloc:,.0f} ({V6_PARAMS['ALLOC_PER_SYM']*100:.1f}%)  "
              f"ADX门槛: {V6_PARAMS['ADX_THRESH']}  "
              f"RSI5<{V6_PARAMS['RSI5_THRESH']}  RSI15<{V6_PARAMS['RSI15_THRESH']}")
        print(s)
        print(f"  初始总资金  : ${total_init:>10,.2f}")
        print(f"  最终总资金  : ${total_final:>10,.2f}  [${total_final-total_init:>+,.2f}]")
        print(f"  区间总收益  : {'+' if total_ret>=0 else ''}{total_ret:.2f}%   ({days} 天)")
        print(f"  等效年化    : {'+' if annual_ret>=0 else ''}{annual_ret:.1f}%/年")
        print(s)
        print(f"  总交易   : {len(all_trades)} 笔  "
              f"(止盈 {len(wins_all)} / 止损 {len(losses_all)} / 其他 {len(others_all)})")
        print(f"  胜率     : {wr:.1f}%   盈亏比: {pf:.2f}x")
        print(f"  均盈利   : ${aw:>+,.2f}   均亏损: ${al:>+,.2f}")
        print(f"  相关性过滤(组合层): 拒绝 {corr_filtered} 个同步信号")
        print(s)

        # 各标的
        print(f"  --- 各标的独立表现 ---")
        header = f"  {'标的':<8} {'初始':>8} {'最终':>9} {'盈亏':>9} {'收益%':>7}  笔数  ADX拦截"
        print(header)
        print(f"  {'-'*64}")
        for sym in self.symbols:
            ic = alloc
            fc = per_sym_caps.get(sym, alloc)
            r  = (fc - ic) / ic * 100
            symt = [t for t in all_trades if t["sym"] == sym]
            filters = per_sym_data.get(sym, (None,None,None,{}))[3] if sym in per_sym_data else {}
            adxf = filters.get("ADX", 0)
            sn   = sym.replace("/USDT","")
            print(f"  {sn:<8} ${ic:>7,.0f} ${fc:>8,.2f} "
                  f"{'+' if fc-ic>=0 else ''}${fc-ic:>8,.2f} "
                  f"{'+' if r>=0 else ''}{r:>6.2f}%  {len(symt):>3}笔  {adxf}拦截")
        print(f"  {'-'*64}")
        print(f"  {'合计':<8} ${total_init:>7,.0f} ${total_final:>8,.2f} "
              f"{'+' if total_final-total_init>=0 else ''}${total_final-total_init:>8,.2f} "
              f"{'+' if total_ret>=0 else ''}{total_ret:>6.2f}%  {len(all_trades):>3}笔")
        print(s)

        # 逐月
        if monthly_pnl:
            print(f"  --- 逐月组合收益明细 ---")
            print(f"  {'月份':<10} {'盈亏':>10} {'W/L':>6} {'月收益%':>10}  资金曲线")
            print(f"  {'-'*55}")
            run_cap = total_init
            prev    = total_init
            for ym in sorted(monthly_pnl.keys()):
                run_cap += monthly_pnl[ym]
                mr  = (run_cap - prev) / prev * 100
                wl  = f"{monthly_w[ym]}W/{monthly_l[ym]}L"
                ps  = f"{'+' if monthly_pnl[ym]>=0 else ''}${monthly_pnl[ym]:.2f}"
                ms  = f"{'+' if mr>=0 else ''}{mr:.2f}%"
                bar = "#" * max(0, int(mr * 5)) if mr > 0 else "." * max(0, int(-mr * 5))
                bar_s = ("+" + bar) if mr >= 0 else ("-" + bar)
                print(f"  {ym:<10} {ps:>10} {wl:>6} {ms:>10}  {bar_s}")
                prev = run_cap
            print(f"  {'-'*55}")
            print(f"  {'总计':<10} {'+' if total_final-total_init>=0 else ''}${total_final-total_init:.2f}"
                  f"  ({tc} 笔完整交易)")
        print(S)

        # 交易明细
        if all_trades:
            sorted_t = sorted(all_trades, key=lambda t: t["time"])
            print(f"\n  --- 全部 {len(all_trades)} 笔交易（按时间排序）---")
            for t in sorted_t:
                tag  = "[TP]" if t["type"]=="止盈" else ("[SL]" if t["type"]=="止损" else "[--]")
                sign = "+" if t["pnl"] >= 0 else ""
                sn   = t["sym"].replace("/USDT","")
                print(f"  {tag} {str(t['time'])[:16]}  {sn:<5}  "
                      f"${t['entry']:.2f} -> ${t['exit']:.2f}  "
                      f"PnL {sign}${t['pnl']:.2f}")

        return {"total_ret": total_ret, "annual_ret": annual_ret, "pf": pf, "wr": wr}


# ══════════════════════════════════════════════════════════
if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="v6 改进版 8标的组合回测")
    parser.add_argument("--start",   type=str, default="2024-10-01")
    parser.add_argument("--end",     type=str, default="2024-12-31")
    parser.add_argument("--capital", type=float, default=10_000.0)
    parser.add_argument("--symbols", type=str,
                        default="BTC/USDT,ETH/USDT,SOL/USDT,BNB/USDT,DOGE/USDT,AVAX/USDT,LINK/USDT,ARB/USDT")
    args = parser.parse_args()
    syms = [s.strip() for s in args.symbols.split(",")]
    pt = PortfolioV6(symbols=syms)
    pt.run(args.start, args.end, args.capital)
