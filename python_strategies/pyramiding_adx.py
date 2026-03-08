"""
pyramiding_adx.py — 趋势过滤版金字塔浮盈加仓策略
============================================================
核心升级：在原有 EMA 破位信号基础上，新增
  - ADX (Average Directional Index) 趋势强度过滤：ADX < adx_threshold 时禁止入场
  - ATR 过滤（可选）：排除低波动率横盘阶段

策略入场必须同时满足：
  1. EMA20 / EMA50 排列方向一致（趋势方向确认）
  2. 收盘价完全突破 EMA20（动量确认）
  3. ADX > adx_threshold（趋势强度足够，非横盘）

此文件完全独立，不依赖 pyramiding_hourly.py。
"""

import yfinance as yf
import pandas as pd
import numpy as np
import matplotlib.pyplot as plt


def compute_adx(df, period=14):
    """计算 ADX / +DI / -DI"""
    high = df['High']
    low  = df['Low']
    close = df['Close']

    # True Range
    tr1 = high - low
    tr2 = (high - close.shift(1)).abs()
    tr3 = (low  - close.shift(1)).abs()
    tr = pd.concat([tr1, tr2, tr3], axis=1).max(axis=1)
    atr = tr.ewm(alpha=1/period, adjust=False).mean()

    # Directional Movement
    up   = high - high.shift(1)
    down = low.shift(1) - low

    plus_dm  = np.where((up > down) & (up > 0), up, 0.0)
    minus_dm = np.where((down > up) & (down > 0), down, 0.0)

    plus_dm_s  = pd.Series(plus_dm,  index=df.index).ewm(alpha=1/period, adjust=False).mean()
    minus_dm_s = pd.Series(minus_dm, index=df.index).ewm(alpha=1/period, adjust=False).mean()

    plus_di  = 100 * plus_dm_s  / atr.replace(0, np.nan)
    minus_di = 100 * minus_dm_s / atr.replace(0, np.nan)

    dx  = 100 * (plus_di - minus_di).abs() / (plus_di + minus_di).replace(0, np.nan)
    adx = dx.ewm(alpha=1/period, adjust=False).mean()

    df = df.copy()
    df['ADX']      = adx
    df['+DI']      = plus_di
    df['-DI']      = minus_di
    df['ATR']      = atr
    return df


def run_pyramiding_adx_strategy(
    ticker='BTC-USD',
    df=None,
    # ── 趋势过滤 ──────────────────────────────────────────
    adx_threshold=25,        # ADX 最低阈值：低于此值判定为横盘，禁止入场
    adx_period=14,           # ADX 计算周期
    # ── 核心交易参数 ─────────────────────────────────────
    leverage_ratio=20,
    trailing_pct=0.03,
    pyramid_step_pct=0.015,
    fee_rate=0.0005,
    target_single_trade_profit=0,
    # ── 利润锁定参数 ─────────────────────────────────────
    enable_profit_sweep=True,
    sweep_multiplier=2.0,
    sweep_keep_ratio=0.5,
    # ─────────────────────────────────────────────────────
    verbose=True
):
    if verbose:
        print("---------------------------------------------------------")
        print(f">>> Pyramiding + ADX Filter Strategy: {ticker} <<<")
        print(f">>> ADX阈值: {adx_threshold} | 杠杆: {leverage_ratio}x | 追踪止损: {trailing_pct*100:.0f}% | 步长: {pyramid_step_pct*100:.1f}% <<<")
        print("---------------------------------------------------------")

    if df is None:
        try:
            raw = yf.download(ticker, start='2025-01-01', end='2026-03-09', interval='1h', progress=False)
            if isinstance(raw.columns, pd.MultiIndex):
                raw.columns = [c[0] for c in raw.columns]
            raw = raw.dropna()
        except Exception as e:
            if verbose:
                print(f"Data fetch failed: {e}")
            return None, 0
    else:
        raw = df.copy()

    if len(raw) < adx_period * 3:
        return None, 0

    # ── 计算指标 ─────────────────────────────────────────
    raw['EMA20'] = raw['Close'].ewm(span=20, adjust=False).mean()
    raw['EMA50'] = raw['Close'].ewm(span=50, adjust=False).mean()
    raw = compute_adx(raw, period=adx_period)
    raw = raw.dropna()

    # ── 回测引擎 ─────────────────────────────────────────
    initial_cap = 100_000.0
    capital = initial_cap
    vault = 0.0
    next_sweep_threshold = initial_cap * sweep_multiplier

    equity_curve = []
    timestamps   = []

    positions             = []
    state                 = "WAITING"
    direction             = 0
    trailing_stop         = 0.0
    max_favorable_excursion = 0.0
    next_pyramid_target   = 0.0
    cooldown_candles      = 0

    blocked_by_adx = 0   # 被 ADX 过滤掉的信号计数

    for idx, row in raw.iterrows():
        timestamps.append(idx)
        _close = float(row['Close'])
        _high  = float(row['High'])
        _low   = float(row['Low'])
        _ema20 = float(row['EMA20'])
        _ema50 = float(row['EMA50'])
        _adx   = float(row['ADX'])

        if cooldown_candles > 0:
            cooldown_candles -= 1

        current_equity = capital

        # ── 入场逻辑 (加了 ADX 守门员) ──────────────────
        if state == "WAITING" and cooldown_candles == 0:

            short_signal = _ema20 < _ema50 and _close < _ema20
            long_signal  = _ema20 > _ema50 and _close > _ema20
            adx_ok       = _adx >= adx_threshold

            if (short_signal or long_signal) and not adx_ok:
                # 有信号，但 ADX 太低，处于横盘区间 → 直接过滤
                blocked_by_adx += 1
                if verbose:
                    print(f"[{idx}] ⚠️ [ADX过滤] ADX={_adx:.1f} < {adx_threshold}，横盘禁入！信号作废。")

            elif short_signal and adx_ok:
                direction = -1
                state = "IN_POSITION"
                risk_amount = capital * 0.15
                notional    = risk_amount * leverage_ratio
                size        = notional / _close
                capital    -= notional * fee_rate

                positions.append({'entry': _close, 'size': size})
                max_favorable_excursion = _low
                trailing_stop           = _close * (1 + trailing_pct)
                next_pyramid_target     = _close * (1 - pyramid_step_pct)

                if verbose:
                    print(f"[{idx}] 🔥 [ADX={_adx:.1f}✅ 空头入场] {_close:.4f} | 敞口: ${notional:.2f}")

            elif long_signal and adx_ok:
                direction = 1
                state = "IN_POSITION"
                risk_amount = capital * 0.15
                notional    = risk_amount * leverage_ratio
                size        = notional / _close
                capital    -= notional * fee_rate

                positions.append({'entry': _close, 'size': size})
                max_favorable_excursion = _high
                trailing_stop           = _close * (1 - trailing_pct)
                next_pyramid_target     = _close * (1 + pyramid_step_pct)

                if verbose:
                    print(f"[{idx}] 🔥 [ADX={_adx:.1f}✅ 多头入场] {_close:.4f} | 敞口: ${notional:.2f}")

        # ── 持仓管理（与 pyramiding_hourly 完全相同）────────
        elif state == "IN_POSITION":

            def _do_profit_sweep():
                nonlocal capital, vault, next_sweep_threshold
                if enable_profit_sweep and capital >= next_sweep_threshold:
                    excess = capital - initial_cap
                    locked = excess * sweep_keep_ratio
                    vault += locked
                    capital -= locked
                    next_sweep_threshold = capital * sweep_multiplier
                    if verbose:
                        print(f"[{idx}] 🔒 [利润锁定] 净值突破{sweep_multiplier}倍！锁定 ${locked:.2f}（总锁仓: ${vault:.2f}），活跃资金: ${capital:.2f}")

            if direction == -1:  # SHORT
                if _low < max_favorable_excursion:
                    max_favorable_excursion = _low
                new_trail = max_favorable_excursion * (1 + trailing_pct)
                if new_trail < trailing_stop:
                    trailing_stop = new_trail

                unrealized = sum([(p['entry'] - _close) * p['size'] for p in positions])
                current_equity = capital + unrealized

                if target_single_trade_profit > 0 and unrealized >= target_single_trade_profit:
                    fee = sum([p['size'] * _close for p in positions]) * fee_rate
                    capital += (unrealized - fee)
                    if verbose:
                        print(f"[{idx}] 💰 [空头止盈] 结转: ${unrealized - fee:.2f}")
                    positions = []; state = "WAITING"; cooldown_candles = 12
                    current_equity = capital
                    _do_profit_sweep()

                elif _high >= trailing_stop:
                    tp = sum([(p['entry'] - trailing_stop) * p['size'] for p in positions])
                    fee = sum([p['size'] * trailing_stop for p in positions]) * fee_rate
                    capital += (tp - fee)
                    if verbose:
                        print(f"[{idx}] 💥 [空头止损] 触发价 {trailing_stop:.4f}  结转: ${tp-fee:.2f}")
                    positions = []; state = "WAITING"; cooldown_candles = 12
                    _do_profit_sweep()

                else:
                    if _low <= next_pyramid_target:
                        unr_py = sum([(p['entry'] - next_pyramid_target) * p['size'] for p in positions])
                        virt   = capital + unr_py
                        if virt > 0:
                            nn = virt * 0.15 * leverage_ratio
                            capital -= nn * fee_rate
                            positions.append({'entry': next_pyramid_target, 'size': nn / next_pyramid_target})
                            if verbose:
                                print(f"[{idx}] 🚀 [空头加仓] 降至 {next_pyramid_target:.4f}，子弹: {len(positions)} 颗")
                        next_pyramid_target *= (1 - pyramid_step_pct)

            elif direction == 1:  # LONG
                if _high > max_favorable_excursion:
                    max_favorable_excursion = _high
                new_trail = max_favorable_excursion * (1 - trailing_pct)
                if new_trail > trailing_stop:
                    trailing_stop = new_trail

                unrealized = sum([(_close - p['entry']) * p['size'] for p in positions])
                current_equity = capital + unrealized

                if target_single_trade_profit > 0 and unrealized >= target_single_trade_profit:
                    fee = sum([p['size'] * _close for p in positions]) * fee_rate
                    capital += (unrealized - fee)
                    if verbose:
                        print(f"[{idx}] 💰 [多头止盈] 结转: ${unrealized - fee:.2f}")
                    positions = []; state = "WAITING"; cooldown_candles = 12
                    current_equity = capital
                    _do_profit_sweep()

                elif _low <= trailing_stop:
                    tp = sum([(trailing_stop - p['entry']) * p['size'] for p in positions])
                    fee = sum([p['size'] * trailing_stop for p in positions]) * fee_rate
                    capital += (tp - fee)
                    if verbose:
                        print(f"[{idx}] 💥 [多头止损] 触发价 {trailing_stop:.4f}  结转: ${tp-fee:.2f}")
                    positions = []; state = "WAITING"; cooldown_candles = 12
                    _do_profit_sweep()

                else:
                    if _high >= next_pyramid_target:
                        unr_py = sum([(next_pyramid_target - p['entry']) * p['size'] for p in positions])
                        virt   = capital + unr_py
                        if virt > 0:
                            nn = virt * 0.15 * leverage_ratio
                            capital -= nn * fee_rate
                            positions.append({'entry': next_pyramid_target, 'size': nn / next_pyramid_target})
                            if verbose:
                                print(f"[{idx}] 🚀 [多头加仓] 飙至 {next_pyramid_target:.4f}，子弹: {len(positions)} 颗")
                        next_pyramid_target *= (1 + pyramid_step_pct)

        # ── 爆仓检查 ──────────────────────────────────────
        if current_equity <= 0:
            current_equity = 0
        equity_curve.append(current_equity + vault)

        if current_equity == 0 and capital <= 0:
            if verbose:
                print(f"[{idx}] 💀 爆仓...")
            break

    # ── 输出结果 ──────────────────────────────────────────
    if len(equity_curve) == 0:
        return None, 0

    final    = equity_curve[-1]
    profit   = final - initial_cap
    roe      = profit / initial_cap * 100
    s        = pd.Series(equity_curve)
    peak     = s.expanding().max()
    max_dd   = ((s - peak) / peak).min() * 100

    if verbose:
        print("---------------------------------------------------------")
        print(f"=== 📊 ADX过滤版金字塔策略：{ticker} 终局 ===")
        print(f"初始本金:         ${initial_cap:,.2f}")
        print(f"🔒 保险箱锁仓:    ${vault:,.2f}")
        print(f"💰 活跃资金:      ${capital:,.2f}")
        print(f"🏆 最终总财富:    ${final:,.2f}")
        print(f"净利润:           ${profit:,.2f}")
        print(f"ROE:              {roe:.2f}%")
        print(f"最大回撤:         {max_dd:.2f}%")
        print(f"被ADX过滤的信号:  {blocked_by_adx} 次")
        print("=========================================================")

    return pd.Series(equity_curve, index=timestamps[:len(equity_curve)]), roe


# ── 直接运行：与 pyramiding_hourly 的结果做对比 ──────────────
if __name__ == '__main__':
    from pyramiding_hourly import run_pyramiding_hourly_strategy

    test_configs = [
        {'adx_threshold': 0,  'label': '❌ 无ADX过滤 (原版)', 'color': '#FF6B6B'},
        {'adx_threshold': 20, 'label': '🟡 ADX>20 (弱过滤)',  'color': '#FFD700'},
        {'adx_threshold': 25, 'label': '🟢 ADX>25 (标准)',    'color': '#4CAF50'},
        {'adx_threshold': 30, 'label': '🔵 ADX>30 (严格)',    'color': '#00BFFF'},
    ]

    ticker = 'BTC-USD'
    # 一年周期（才能看出ADX过滤的长期优势）
    df = yf.download(ticker, start='2025-01-01', end='2026-03-09', interval='1h', progress=False)
    if isinstance(df.columns, pd.MultiIndex):
        df.columns = [c[0] for c in df.columns]
    df = df.dropna()

    plt.style.use('dark_background')
    fig, ax = plt.subplots(figsize=(16, 8))
    ax.set_title(f'ADX趋势过滤 vs 无过滤  [ {ticker} | 1年周期 | 20x杠杆 ]',
                 fontsize=14, fontweight='bold', color='#FFD700', pad=12)
    ax.set_xlabel('Date / Time', fontsize=11)
    ax.set_ylabel('Strategy Equity + Vault (USD)', fontsize=11)
    ax.axhline(100_000, color='white', linestyle='--', alpha=0.4, linewidth=1)
    ax.grid(True, linestyle=':', alpha=0.2, color='#555555')

    for cfg in test_configs:
        curve, roe = run_pyramiding_adx_strategy(
            ticker=ticker,
            df=df,
            adx_threshold=cfg['adx_threshold'],
            leverage_ratio=20,
            trailing_pct=0.03,
            pyramid_step_pct=0.015,
            enable_profit_sweep=True,
            verbose=False
        )
        if curve is not None:
            s    = pd.Series(curve.values)
            peak = s.expanding().max()
            dd   = ((s - peak) / peak).min() * 100
            ax.plot(curve.index, curve.values,
                    label=f"{cfg['label']}  ROE: {roe:.1f}%  MaxDD: {dd:.1f}%",
                    color=cfg['color'], linewidth=2, alpha=0.9)

    ax.legend(fontsize=11, loc='upper left', frameon=True,
              facecolor='#1a1a2e', edgecolor='#555555', framealpha=0.9)

    out_img = r'c:\Users\Administrator\Desktop\pyramiding_adx_result.png'
    plt.savefig(out_img, dpi=280, bbox_inches='tight', facecolor='#0d0d1a')
    print(f"\n✅ ADX对比图已保存至: {out_img}")
