"""
adx_optimizer_top30.py
======================
Grid search optimizer for the ADX-filtered pyramiding strategy.
Searches over: leverage, trailing_pct, pyramid_step_pct, adx_threshold
Outputs a ranked markdown report per ticker (Top 3 configurations).
"""
import yfinance as yf
import pandas as pd
import itertools
from concurrent.futures import ProcessPoolExecutor
import time
import os

from pyramiding_adx import run_pyramiding_adx_strategy


def evaluate_params(args):
    ticker, df, leverage_ratio, trailing_pct, pyramid_step_pct, adx_threshold = args
    curve, roe = run_pyramiding_adx_strategy(
        ticker=ticker,
        df=df,
        adx_threshold=adx_threshold,
        leverage_ratio=leverage_ratio,
        trailing_pct=trailing_pct,
        pyramid_step_pct=pyramid_step_pct,
        enable_profit_sweep=True,
        verbose=False
    )
    if curve is None or len(curve) == 0:
        return (ticker, leverage_ratio, trailing_pct, pyramid_step_pct, adx_threshold, 0.0, 0.0)
    s = pd.Series(curve.values)
    peak = s.expanding().max()
    max_dd = ((s - peak) / peak).min() * 100
    return (ticker, leverage_ratio, trailing_pct, pyramid_step_pct, adx_threshold, round(roe, 2), round(max_dd, 2))


def run():
    # ── Time range ─────────────────────────────────────────────
    start_date = '2025-01-01'      # <-- adjust here
    end_date   = '2026-03-09'      # <-- adjust here

    tickers = [
        'BTC-USD', 'ETH-USD', 'BNB-USD', 'SOL-USD', 'XRP-USD',
        'DOGE-USD', 'ADA-USD', 'SHIB-USD', 'AVAX-USD', 'DOT-USD',
        'LINK-USD', 'TRX-USD', 'BCH-USD', 'LTC-USD', 'NEAR-USD',
        'UNI-USD', 'APT-USD', 'XLM-USD', 'ATOM-USD', 'ICP-USD',
        'FIL-USD', 'STX-USD', 'ARB-USD', 'RNDR-USD', 'HBAR-USD',
        'INJ-USD', 'OP-USD',  'VET-USD', 'ALGO-USD', 'GRT-USD'
    ]

    # ── Download data (once, in main process) ──────────────────
    data_cache = {}
    print("Downloading data for " + str(len(tickers)) + " tickers from " + start_date + " to " + end_date + "...")
    for tk in tickers:
        try:
            df = yf.download(tk, start=start_date, end=end_date, interval='1h', progress=False)
            if df.empty:
                print("  Skip " + tk + ": no data")
                continue
            if isinstance(df.columns, pd.MultiIndex):
                df.columns = [c[0] for c in df.columns]
            data_cache[tk] = df.dropna()
            print("  [" + tk + "] " + str(len(data_cache[tk])) + " candles")
        except Exception as e:
            print("  Skip " + tk + ": " + str(e))

    # ── Parameter search space ─────────────────────────────────
    leverage_ratios    = [10, 15, 20, 25]
    trailing_pcts      = [0.03, 0.04, 0.05]
    pyramid_step_pcts  = [0.01, 0.015, 0.02]
    adx_thresholds     = [0, 20, 25, 30]    # 0 = no filter (same as pyramiding_hourly)

    # 4 x 3 x 3 x 4 = 144 combos per ticker
    combos = list(itertools.product(
        data_cache.keys(),
        leverage_ratios,
        trailing_pcts,
        pyramid_step_pcts,
        adx_thresholds
    ))
    tasks = [(tk, data_cache[tk], lv, tr, py, adx)
             for tk, lv, tr, py, adx in combos]

    total = len(tasks)
    print("\nStarting " + str(total) + " optimization tasks across " + str(os.cpu_count() or 4) + " CPU cores...")
    t0 = time.time()
    results = []
    with ProcessPoolExecutor(max_workers=os.cpu_count() or 4) as executor:
        for res in executor.map(evaluate_params, tasks):
            results.append(res)
    elapsed = round(time.time() - t0, 1)
    print("Done in " + str(elapsed) + "s\n")

    cols = ['Ticker', 'Leverage', 'Trailing_Stop', 'Pyramid_Step', 'ADX_Threshold', 'ROE', 'MaxDD']
    res_df = pd.DataFrame(results, columns=cols)

    # ── Build markdown report ──────────────────────────────────
    md  = "# Top 30 Crypto — ADX Filter Pyramiding Strategy Optimizer Report\n\n"
    md += "> **Strategy**: 1H EMA20/50 breakout + fractal pyramid compounding + trailing stop + ADX trend gate\n"
    md += "> **Period**: " + start_date + " ~ " + end_date + "\n"
    md += "> **Grid**: Leverage (10x-25x) | Trailing Stop (3%-5%) | Pyramid Step (1%-2%) | ADX Threshold (0/20/25/30)\n"
    md += "> **Extras**: Profit Sweep ON (vault locks 50% when equity doubles)\n\n"
    md += "---\n\n"

    # Global champion board (best ROE per ticker)
    best_per_ticker = res_df.loc[res_df.groupby('Ticker')['ROE'].idxmax()].sort_values('ROE', ascending=False)

    md += "## Overall Champion Board (Best ROE per Ticker)\n\n"
    md += "| Rank | Ticker | Leverage | Trailing | Pyramid Step | ADX Gate | ROE (%) | Max DD (%) |\n"
    md += "|:---:|:---|:---:|:---:|:---:|:---:|---:|---:|\n"
    rank = 1
    for _, row in best_per_ticker.iterrows():
        if row['ROE'] <= 0:
            continue
        adx_label = "OFF" if row['ADX_Threshold'] == 0 else (">" + str(int(row['ADX_Threshold'])))
        md += ("| " + str(rank) + " | **" + str(row['Ticker']) + "** | " +
               str(int(row['Leverage'])) + "x | " +
               str(round(row['Trailing_Stop'] * 100, 1)) + "% | " +
               str(round(row['Pyramid_Step'] * 100, 1)) + "% | ADX" + adx_label + " | **+" +
               str(row['ROE']) + "%** | " + str(row['MaxDD']) + "% |\n")
        rank += 1

    md += "\n---\n\n"
    md += "## Per-Ticker Top 3 Parameter Configurations\n\n"

    for tk in best_per_ticker['Ticker']:
        best_roe = float(best_per_ticker[best_per_ticker['Ticker'] == tk]['ROE'].iloc[0])
        if best_roe <= 0:
            continue

        md += "### " + tk + "\n"
        md += "| Leverage | Trailing Stop | Pyramid Step | ADX Gate | ROE (%) | Max DD (%) |\n"
        md += "|---:|---:|---:|:---:|---:|---:|\n"
        sub = res_df[res_df['Ticker'] == tk].sort_values('ROE', ascending=False).head(3)
        for _, row in sub.iterrows():
            adx_label = "OFF" if row['ADX_Threshold'] == 0 else ("ADX>" + str(int(row['ADX_Threshold'])))
            md += ("| " + str(int(row['Leverage'])) + "x | " +
                   str(round(row['Trailing_Stop'] * 100, 1)) + "% | " +
                   str(round(row['Pyramid_Step'] * 100, 1)) + "% | " +
                   adx_label + " | **+" + str(row['ROE']) + "%** | " +
                   str(row['MaxDD']) + "% |\n")
        md += "\n"

    out_md = 'top30_adx_optimizer_report.md'
    with open(out_md, 'w', encoding='utf-8') as f:
        f.write(md)

    # Terminal summary
    print("=" * 72)
    print("CHAMPION BOARD — Best ROE per Ticker (ADX strategy, " + start_date + " ~ " + end_date + ")")
    print("=" * 72)
    print("Rank  Ticker        Leverage  Trailing  Step   ADX      ROE        MaxDD")
    print("-" * 72)
    rank = 1
    for _, row in best_per_ticker.iterrows():
        if row['ROE'] <= 0:
            continue
        adx_label = "OFF   " if row['ADX_Threshold'] == 0 else (">" + str(int(row['ADX_Threshold'])) + "    ")
        print(str(rank).rjust(4) + "  " +
              str(row['Ticker']).ljust(14) +
              str(int(row['Leverage'])).rjust(6) + "x  " +
              str(round(row['Trailing_Stop']*100, 1)).rjust(6) + "%  " +
              str(round(row['Pyramid_Step']*100, 1)).rjust(5) + "%  ADX" + adx_label +
              ("+" + str(row['ROE']) + "%").rjust(10) + "  " +
              str(row['MaxDD']) + "%")
        rank += 1
    print("=" * 72)
    print("Full report saved to: " + out_md)


if __name__ == '__main__':
    run()
