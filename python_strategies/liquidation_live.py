"""
爆仓抢反弹 v5 策略 - 实盘信号引擎（纸上交易 / 双标的模式）
=============================================================
推荐配置：BTC/USDT 70% + DOGE/USDT 30%
  - BTC：高流动性，ADX 过滤最有效，趋势期完美保本
  - DOGE：散户情绪标的，震荡期反弹弹性更强（回测横盘 +3%）

运行方式：
  # 默认双标的（BTC 70% + DOGE 30%），总资金 $10,000
  python liquidation_live.py

  # 只跑 BTC
  python liquidation_live.py --symbols BTC/USDT --allocs 100

  # 自定义三标的
  python liquidation_live.py --symbols BTC/USDT,ETH/USDT,DOGE/USDT --allocs 60,20,20

  # 单次测试（不循环）
  python liquidation_live.py --once
"""

import time
import logging
import argparse
import os
from datetime import datetime, timezone

import pandas as pd
import numpy as np
import ccxt

# ── 日志配置 ──────────────────────────────────────────────────────────
LOG_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "logs")
os.makedirs(LOG_DIR, exist_ok=True)
log_file = os.path.join(LOG_DIR, f"live_{datetime.now().strftime('%Y%m%d_%H%M')}.log")

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
    handlers=[
        logging.StreamHandler(),
        logging.FileHandler(log_file, encoding="utf-8"),
    ],
)
log = logging.getLogger("v5_live")


# ══════════════════════════════════════════════════════════════════════
# 策略参数（与回测 v5 完全一致）
# ══════════════════════════════════════════════════════════════════════
PARAMS = {
    "VOL_WINDOW"       : 20,
    "VOL_MULTIPLIER"   : 2.0,
    "WICK_PCT"         : 0.0015,
    "WICK_RATIO"       : 0.40,
    "EMA_PERIOD"       : 200,
    "EMA_SLOPE_WIN"    : 12,
    "ATR_PERIOD"       : 14,
    "ATR_SMA_PERIOD"   : 50,
    "ATR_SPIKE_MULTI"  : 2.0,
    "RSI5_PERIOD"      : 14,
    "RSI5_THRESH"      : 30,
    "RSI15_PERIOD"     : 14,
    "RSI15_THRESH"     : 45,
    "ADX_PERIOD"       : 14,
    "ADX_THRESH"       : 25,
    "TAKE_PROFIT"      : 0.012,
    "ATR_SL_MULTI"     : 1.2,
    "MAX_HOLD_BARS"    : 48,
}


# ══════════════════════════════════════════════════════════════════════
# 指标计算
# ══════════════════════════════════════════════════════════════════════
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
    m = (plus_dm == minus_dm)
    plus_dm[m] = 0; minus_dm[m] = 0
    plus_dm[plus_dm < minus_dm]  = 0
    minus_dm[minus_dm < plus_dm] = 0
    tr    = pd.concat([h-l, (h-c.shift(1)).abs(), (l-c.shift(1)).abs()], axis=1).max(axis=1)
    a14   = tr.ewm(span=p, adjust=False).mean()
    pdi   = 100 * plus_dm.ewm(span=p, adjust=False).mean()  / a14.replace(0, np.nan)
    mdi   = 100 * minus_dm.ewm(span=p, adjust=False).mean() / a14.replace(0, np.nan)
    dx    = 100 * (pdi - mdi).abs() / (pdi + mdi).replace(0, np.nan)
    return dx.ewm(span=p, adjust=False).mean()


def calc_vwap(df):
    d  = df.copy()
    tp = (d["high"] + d["low"] + d["close"]) / 3
    d["_date"]    = d.index.date
    d["_tpv"]     = tp * d["volume"]
    d["_cum_tpv"] = d.groupby("_date")["_tpv"].cumsum()
    d["_cum_vol"] = d.groupby("_date")["volume"].cumsum()
    return d["_cum_tpv"] / d["_cum_vol"].replace(0, np.nan)


# ══════════════════════════════════════════════════════════════════════
# 单标的纸上持仓
# ══════════════════════════════════════════════════════════════════════
class PaperPosition:
    def __init__(self, symbol, capital):
        self.symbol       = symbol
        self.sname        = symbol.replace("/USDT", "")
        self.capital      = capital
        self.init_capital = capital
        self.position     = 0.0
        self.entry_px     = 0.0
        self.atr_entry    = 0.0
        self.hold_bars    = 0
        self.tp_price     = 0.0
        self.sl_price     = 0.0
        self.trades       = []

    @property
    def is_open(self):
        return self.position > 0

    def open_long(self, price, atr):
        self.entry_px  = price
        self.atr_entry = atr
        self.position  = (self.capital * 0.99) / price
        self.capital  -= self.position * price * 1.0005
        self.tp_price  = price * (1 + PARAMS["TAKE_PROFIT"])
        self.sl_price  = price - atr * PARAMS["ATR_SL_MULTI"]
        self.hold_bars = 0
        log.info(f"  [{self.sname}] [OPEN LONG]  入场 ${price:.4f}  "
                 f"止盈 ${self.tp_price:.4f}(+{PARAMS['TAKE_PROFIT']*100:.1f}%)  "
                 f"止损 ${self.sl_price:.4f}(ATR×{PARAMS['ATR_SL_MULTI']})")

    def check_exit(self, high, low, close, ts):
        if not self.is_open:
            return
        self.hold_bars += 1
        kind = exit_px = pnl = None

        if high >= self.tp_price:
            exit_px = self.tp_price
            pnl  = self.position * (exit_px - self.entry_px)
            self.capital += self.position * exit_px * 0.9995
            kind = "止盈"

        elif low <= self.sl_price:
            exit_px = max(self.sl_price, low)
            pnl  = self.position * (exit_px - self.entry_px)
            self.capital += self.position * exit_px * 0.9995
            kind = "止损"

        elif self.hold_bars >= PARAMS["MAX_HOLD_BARS"]:
            exit_px = close
            pnl  = self.position * (exit_px - self.entry_px)
            self.capital += self.position * exit_px * 0.9995
            kind = "超时"

        if kind:
            self.position = 0.0
            ret = (self.capital - self.init_capital) / self.init_capital * 100
            self.trades.append({"type": kind, "entry": self.entry_px,
                                "exit": exit_px, "pnl": pnl,
                                "capital": self.capital, "time": ts})
            emoji = "✅" if pnl >= 0 else "❌"
            log.info(f"  [{self.sname}] [{kind}] {emoji}  "
                     f"出场 ${exit_px:.4f}  "
                     f"PnL {'+'if pnl>=0 else ''}${pnl:.2f}  "
                     f"账户 ${self.capital:.2f}({'+' if ret>=0 else ''}{ret:.2f}%)")

    def summary(self, current_price=None):
        ret = (self.capital - self.init_capital) / self.init_capital * 100
        wins   = sum(1 for t in self.trades if t["type"] == "止盈")
        total  = sum(1 for t in self.trades if t["type"] in ("止盈","止损"))
        wr     = wins / total * 100 if total > 0 else 0
        unreal = ""
        if self.is_open and current_price:
            ur = self.position * (current_price - self.entry_px)
            unreal = f"  持多中 未实现{'+'if ur>=0 else ''}${ur:.2f}"
        return (f"[{self.sname}] 账户${self.capital:.2f}"
                f"({'+' if ret>=0 else ''}{ret:.2f}%)  "
                f"{total}笔 胜率{wr:.0f}%{unreal}")


# ══════════════════════════════════════════════════════════════════════
# 单标的信号检查
# ══════════════════════════════════════════════════════════════════════
def check_signal(df, symbol):
    """
    检查最新闭合 K 线（倒数第2根）是否触发做多信号
    返回: (is_signal, info_dict)
    """
    p = PARAMS
    if len(df) < max(p["EMA_PERIOD"], p["ATR_SMA_PERIOD"]) + 5:
        return False, {"reason": "数据不足"}

    row      = df.iloc[-2]
    prev_ema = df.iloc[-2 - p["EMA_SLOPE_WIN"]]["ema200"]

    info = {
        "sym"      : symbol.replace("/USDT", ""),
        "time"     : str(row.name)[:16],
        "close"    : round(row["close"], 4),
        "adx"      : round(row.get("adx", 0), 1),
        "rsi5"     : round(row.get("rsi5", 50), 1),
        "rsi15"    : round(row.get("rsi15", 50), 1),
        "ema200"   : round(row.get("ema200", 0), 4),
        "atr"      : round(row.get("atr", 0), 6),
        "vwap"     : round(row.get("vwap", 0), 4),
        "filter"   : "",
    }

    # 过滤器 0: ADX
    adx = row["adx"]
    if pd.isna(adx) or adx >= p["ADX_THRESH"]:
        info["filter"] = f"ADX={adx:.1f}>={p['ADX_THRESH']} 趋势行情"
        return False, info

    # 过滤器 1: EMA200
    ema200 = row["ema200"]
    up = (not pd.isna(ema200)) and (
        row["close"] > ema200 or
        (not pd.isna(prev_ema) and ema200 > prev_ema)
    )
    if not up:
        info["filter"] = f"EMA200={ema200:.4f} 趋势向下"
        return False, info

    # 过滤器 2: ATR 异常波动
    ac, am = row["atr"], row["atr_ma"]
    if not pd.isna(ac) and not pd.isna(am) and am > 0:
        if ac > am * p["ATR_SPIKE_MULTI"]:
            info["filter"] = f"ATR异常 {ac:.4f}>{am:.4f}×{p['ATR_SPIKE_MULTI']}"
            return False, info

    # 核心信号：爆量锤子线
    bb = min(row["open"], row["close"])
    lw = bb - row["low"]
    tr = row["high"] - row["low"]
    lw_pct = lw / row["low"] if row["low"] > 0 else 0
    lw_rat = lw / tr if tr > 0 else 0
    vm = df.iloc[-3]["vol_ma"]
    vol_ok = (row["volume"] > 0 and not pd.isna(vm) and vm > 0
              and row["volume"] > vm * p["VOL_MULTIPLIER"])
    hammer = lw_pct >= p["WICK_PCT"] and lw_rat >= p["WICK_RATIO"]

    info["lw_pct"]   = round(lw_pct * 100, 3)
    info["lw_ratio"] = round(lw_rat, 2)
    info["vol_x"]    = round(row["volume"] / vm, 2) if (not pd.isna(vm) and vm > 0) else 0

    if not hammer:
        info["filter"] = f"非锤子线 下影={lw_pct*100:.3f}%/{lw_rat:.2f}"
        return False, info
    if not vol_ok:
        info["filter"] = f"量不足 {info['vol_x']}x<{p['VOL_MULTIPLIER']}x"
        return False, info

    # 过滤器 3: RSI5
    if not pd.isna(row["rsi5"]) and row["rsi5"] >= p["RSI5_THRESH"]:
        info["filter"] = f"RSI5={row['rsi5']:.1f}>={p['RSI5_THRESH']}"
        return False, info

    # 过滤器 4: RSI15
    rsi15 = row.get("rsi15", 50)
    if not pd.isna(rsi15) and rsi15 >= p["RSI15_THRESH"]:
        info["filter"] = f"RSI15={rsi15:.1f}>={p['RSI15_THRESH']}"
        return False, info

    # 过滤器 5: VWAP
    if not pd.isna(row["vwap"]) and row["close"] > row["vwap"]:
        info["filter"] = f"价格>${row['close']:.4f}>VWAP${row['vwap']:.4f}"
        return False, info

    info["filter"] = "ALL PASS"
    return True, info


# ══════════════════════════════════════════════════════════════════════
# 主引擎：多标的并行信号监控
# ══════════════════════════════════════════════════════════════════════
class MultiSymbolEngine:

    def __init__(self, symbols, allocs, total_capital):
        """
        symbols : ["BTC/USDT", "DOGE/USDT"]
        allocs  : [70, 30]          (百分比，需合计 100)
        """
        assert len(symbols) == len(allocs), "symbols 和 allocs 长度必须一致"
        assert sum(allocs) == 100, f"allocs 合计必须 = 100，当前 = {sum(allocs)}"

        self.exchange = ccxt.okx({"enableRateLimit": True})
        self.positions = {
            sym: PaperPosition(sym, total_capital * (pct / 100))
            for sym, pct in zip(symbols, allocs)
        }
        self.run_count = 0
        self.total_cap = total_capital

        log.info("=" * 62)
        log.info("  v5 爆仓抢反弹策略 - 多标的实盘信号引擎")
        log.info(f"  总资金: ${total_capital:,.0f}")
        for sym, pct in zip(symbols, allocs):
            cap = total_capital * pct / 100
            log.info(f"    {sym:<15} {pct:>3}%  ${cap:,.0f}")
        log.info(f"  日志: {log_file}")
        log.info("=" * 62)

    def _fetch(self, sym, tf, limit):
        try:
            bars = self.exchange.fetch_ohlcv(sym, tf, limit=limit)
            if not bars:
                return None
            df = pd.DataFrame(bars, columns=["ts","open","high","low","close","volume"])
            df["datetime"] = pd.to_datetime(df["ts"], unit="ms", utc=True)
            df = df.set_index("datetime").drop(columns=["ts"])
            df = df[~df.index.duplicated(keep="first")]
            for c in ["open","high","low","close","volume"]:
                df[c] = pd.to_numeric(df[c], errors="coerce")
            return df.dropna(subset=["open","high","low","close"])
        except Exception as e:
            log.warning(f"[{sym} {tf}] 拉取失败: {e}")
            return None

    def _build_df(self, sym):
        """拉取 5m + 15m 并计算所有指标"""
        p   = PARAMS
        df5 = self._fetch(sym, "5m", 350)
        df15= self._fetch(sym, "15m", 300)
        if df5 is None or len(df5) < 250:
            return None

        df5["vol_ma"] = df5["volume"].rolling(p["VOL_WINDOW"]).mean()
        df5["atr"]    = calc_atr(df5, p["ATR_PERIOD"])
        df5["atr_ma"] = df5["atr"].rolling(p["ATR_SMA_PERIOD"]).mean()
        df5["rsi5"]   = calc_rsi(df5, p["RSI5_PERIOD"])
        df5["adx"]    = calc_adx(df5, p["ADX_PERIOD"])
        df5["vwap"]   = calc_vwap(df5)

        if df15 is not None:
            df15["rsi15"] = calc_rsi(df15, p["RSI15_PERIOD"])
            df15["ema200"] = df15["close"].ewm(span=p["EMA_PERIOD"], adjust=False).mean()
            df5["rsi15"]  = df15["rsi15"].reindex(df5.index, method="ffill")
            df5["ema200"] = df15["ema200"].reindex(df5.index, method="ffill")
        else:
            df5["rsi15"] = 50.0
            df5["ema200"] = float('nan')

        return df5

    def run_once(self):
        self.run_count += 1
        now = datetime.now(timezone.utc)
        log.info(f"\n── 第 {self.run_count} 轮  UTC {str(now)[:19]} ──────────────────────")

        for sym, pos in self.positions.items():
            sname = sym.replace("/USDT", "")
            df = self._build_df(sym)
            if df is None:
                log.warning(f"  [{sname}] 数据不足，跳过")
                continue

            last    = df.iloc[-2]
            current = df.iloc[-1]["close"]

            # 检查出场
            if pos.is_open:
                pos.check_exit(last["high"], last["low"], last["close"], last.name)

            # 检查入场信号
            if not pos.is_open:
                is_sig, info = check_signal(df, sym)

                # 打印当前市场状态一行
                lw_str  = f"下影={info.get('lw_pct','?')}%"  if info.get('lw_pct') else ""
                vol_str = f"量={info.get('vol_x','?')}x"      if info.get('vol_x') else ""
                log.info(f"  [{sname}] ${current:.4f}  "
                         f"ADX={info['adx']}  RSI5={info['rsi5']}  RSI15={info['rsi15']}  "
                         f"VWAP=${info['vwap']}  {lw_str}  {vol_str}")

                if info.get("filter") and info["filter"] != "ALL PASS":
                    log.info(f"  [{sname}] 过滤: {info['filter']}")

                if is_sig:
                    entry = df.iloc[-1]["open"]   # 用当前最新 K 线开盘价入场
                    atr   = last["atr"]
                    log.info(f"  {'='*55}")
                    log.info(f"  [{sname}] ★★ 信号触发 ★★  {info['time']}")
                    log.info(f"    ADX={info['adx']}  RSI5={info['rsi5']}  RSI15={info['rsi15']}")
                    log.info(f"    下影={info.get('lw_pct')}%  爆量={info.get('vol_x')}x  "
                             f"VWAP=${info['vwap']}")
                    log.info(f"    入场价=${entry:.4f}  ATR=${atr:.6f}")
                    log.info(f"  {'='*55}")
                    pos.open_long(entry, atr)

        # 打印组合账户总结
        log.info("  ── 账户总览 ──────────────────────────────────────────")
        total_now = 0.0
        for sym, pos in self.positions.items():
            cur = df.iloc[-1]["close"] if sym in self.positions else 0
            log.info(f"  {pos.summary()}")
            total_now += pos.capital
            if pos.is_open:
                # 加上未平仓市值
                total_now += pos.position * cur - 0  # 已在capital中扣了仓位买入资金

        total_ret = (total_now - self.total_cap) / self.total_cap * 100
        log.info(f"  [组合] 总账户 ${total_now:.2f}  "
                 f"({'+' if total_ret>=0 else ''}{total_ret:.2f}%)")

    def run_loop(self, interval_sec=300):
        log.info(f"主循环启动，每 {interval_sec//60} 分钟检查 (Ctrl+C 退出)")
        while True:
            try:
                self.run_once()
            except KeyboardInterrupt:
                self._final_summary()
                break
            except Exception as e:
                log.error(f"运行异常: {e}", exc_info=True)

            # 精准等到下根 5m K线的开头后 5 秒
            now_sec  = time.time()
            wait_sec = interval_sec - (now_sec % interval_sec) + 5
            log.info(f"  等待 {wait_sec:.0f}s 后下次检查...\n")
            time.sleep(wait_sec)

    def _final_summary(self):
        print("\n" + "=" * 64)
        print("  v5 策略引擎停止 - 最终汇总")
        print("=" * 64)
        total_cap = 0.0
        for sym, pos in self.positions.items():
            wins  = [t for t in pos.trades if t["type"] == "止盈"]
            loss  = [t for t in pos.trades if t["type"] == "止损"]
            tc    = len(wins) + len(loss)
            wr    = len(wins) / tc * 100 if tc > 0 else 0
            ret   = (pos.capital - pos.init_capital) / pos.init_capital * 100
            total_cap += pos.capital
            print(f"  {sym:<15} 最终 ${pos.capital:.2f} ({'+' if ret>=0 else ''}{ret:.2f}%)"
                  f"  {tc}笔 胜率{wr:.0f}%")
            for t in pos.trades:
                tag  = "[TP]" if t["type"]=="止盈" else "[SL]" if t["type"]=="止损" else "[--]"
                sign = "+" if t["pnl"] >= 0 else ""
                print(f"    {tag} {str(t['time'])[:16]}  "
                      f"${t['entry']:.4f}->${t['exit']:.4f}  "
                      f"PnL {sign}${t['pnl']:.2f}")
        total_ret = (total_cap - self.total_cap) / self.total_cap * 100
        print(f"\n  组合总资金: ${total_cap:.2f}  "
              f"({'+' if total_ret>=0 else ''}{total_ret:.2f}%)")
        print("=" * 64)


# ──────────────────────────────────────────────────────────────────────
if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="v5 爆仓抢反弹 - 多标的实盘信号引擎（纸上交易）"
    )
    parser.add_argument("--symbols",  type=str, default="BTC/USDT,DOGE/USDT",
                        help="标的列表，逗号分隔 (默认: BTC/USDT,DOGE/USDT)")
    parser.add_argument("--allocs",   type=str, default="70,30",
                        help="资金分配比例，合计需为100 (默认: 70,30)")
    parser.add_argument("--capital",  type=float, default=10_000.0,
                        help="总纸上资金 (默认: $10,000)")
    parser.add_argument("--interval", type=int,   default=300,
                        help="检查间隔秒数 (默认: 300 = 5分钟)")
    parser.add_argument("--once",     action="store_true",
                        help="只运行一次后退出（测试用）")
    args = parser.parse_args()

    syms   = [s.strip() for s in args.symbols.split(",")]
    allocs = [int(a.strip()) for a in args.allocs.split(",")]

    engine = MultiSymbolEngine(syms, allocs, args.capital)

    if args.once:
        engine.run_once()
    else:
        engine.run_loop(interval_sec=args.interval)
