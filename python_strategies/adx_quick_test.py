"""
adx_quick_test.py -- Top 30 crypto ADX filter comparison scan
Runs: no-filter / ADX>20 / ADX>25 / ADX>30
Period: 2025-01-01 to 2026-03-09
"""
import yfinance as yf
import pandas as pd
import itertools
from concurrent.futures import ProcessPoolExecutor
import os
import time

from pyramiding_adx import run_pyramiding_adx_strategy

TICKERS = [
    'BTC-USD', 'ETH-USD', 'BNB-USD', 'SOL-USD', 'XRP-USD',
    'DOGE-USD', 'ADA-USD', 'SHIB-USD', 'AVAX-USD', 'DOT-USD',
    'LINK-USD', 'TRX-USD', 'BCH-USD', 'LTC-USD', 'NEAR-USD',
    'UNI-USD', 'APT-USD', 'XLM-USD', 'ATOM-USD', 'ICP-USD',
    'FIL-USD', 'STX-USD', 'ARB-USD', 'RNDR-USD', 'HBAR-USD',
    'INJ-USD', 'OP-USD',  'VET-USD', 'ALGO-USD', 'GRT-USD'
]

ADX_THRESHOLDS = [0, 20, 25, 30]
LEVERAGE       = 20
TRAILING_PCT   = 0.03
PYRAMID_STEP   = 0.015
START_DATE     = '2025-01-01'
END_DATE       = '2026-03-09'


def evaluate(args):
    tk, df, thr = args
    curve, roe = run_pyramiding_adx_strategy(
        ticker=tk, df=df,
        adx_threshold=thr,
        leverage_ratio=LEVERAGE,
        trailing_pct=TRAILING_PCT,
        pyramid_step_pct=PYRAMID_STEP,
        enable_profit_sweep=True,
        verbose=False
    )
    if curve is None:
        return (tk, thr, 0.0, 0.0, 100000)
    s    = pd.Series(curve.values)
    peak = s.expanding().max()
    dd   = ((s - peak) / peak).min() * 100
    return (tk, thr, round(roe, 2), round(dd, 2), int(curve.iloc[-1]))


def main():
    data_cache = {}
    print("Downloading 1H data (" + START_DATE + " ~ " + END_DATE + ") for " + str(len(TICKERS)) + " tickers...")
    for tk in TICKERS:
        try:
            df = yf.download(tk, start=START_DATE, end=END_DATE, interval='1h', progress=False)
            if df.empty:
                print("  Skip " + tk + ": no data")
                continue
            if isinstance(df.columns, pd.MultiIndex):
                df.columns = [c[0] for c in df.columns]
            df = df.dropna()
            if len(df) > 50:
                data_cache[tk] = df
        except Exception as e:
            print("  Skip " + tk + ": " + str(e))

    print("Data ready for " + str(len(data_cache)) + " tickers.\n")

    tasks = [(tk, data_cache[tk], thr)
             for tk, thr in itertools.product(data_cache.keys(), ADX_THRESHOLDS)]

    print("Running " + str(len(tasks)) + " tasks across " + str(os.cpu_count()) + " cores...")
    t0 = time.time()
    results = []
    with ProcessPoolExecutor(max_workers=os.cpu_count() or 4) as ex:
        for res in ex.map(evaluate, tasks):
            results.append(res)
    print("Done in " + str(round(time.time()-t0, 1)) + "s\n")

    res_df = pd.DataFrame(results, columns=['Ticker', 'ADX_Threshold', 'ROE', 'MaxDD', 'Final'])

    no_filter_df = res_df[res_df['ADX_Threshold'] == 0].set_index('Ticker')
    adx30_df     = res_df[res_df['ADX_Threshold'] == 30].set_index('Ticker')

    improvements = []
    for tk in data_cache.keys():
        if tk in no_filter_df.index and tk in adx30_df.index:
            base  = float(no_filter_df.loc[tk, 'ROE'])
            adx30 = float(adx30_df.loc[tk, 'ROE'])
            improvements.append({'Ticker': tk, 'NoFilter': base, 'ADX30': adx30,
                                  'Gain': round(adx30 - base, 2)})

    imp_df = pd.DataFrame(improvements).sort_values('Gain', ascending=False)

    # Build markdown
    md = "# Top 30 Crypto ADX Filter Comparison Report\n\n"
    md += "> Period: " + START_DATE + " ~ " + END_DATE + "\n"
    md += "> Params: Leverage " + str(LEVERAGE) + "x | Trailing " + str(int(TRAILING_PCT*100)) + "% | Step " + str(PYRAMID_STEP*100) + "% | Profit Sweep ON\n\n"
    md += "---\n\n"
    md += "## Top Gainers from ADX>30 Filter\n\n"
    md += "| Rank | Ticker | No-Filter ROE | ADX>30 ROE | Improvement |\n"
    md += "|:---:|:---|---:|---:|---:|\n"
    for rank, (_, row) in enumerate(imp_df.iterrows(), 1):
        sign = "+" if row['Gain'] > 0 else ""
        md += "| " + str(rank) + " | **" + row['Ticker'] + "** | " + str(round(row['NoFilter'],2)) + "% | " + str(round(row['ADX30'],2)) + "% | **" + sign + str(row['Gain']) + "%** |\n"

    md += "\n---\n\n## Full Comparison Table\n\n"
    md += "| Ticker | No-Filter | ADX>20 | ADX>25 | ADX>30 | ADX>30 MaxDD |\n"
    md += "|:---|---:|---:|---:|---:|---:|\n"
    for tk in data_cache.keys():
        sub = res_df[res_df['Ticker'] == tk].set_index('ADX_Threshold')
        def gr(t): return (str(round(float(sub.loc[t,'ROE']),2)) + "%") if t in sub.index else "N/A"
        def gd(t): return (str(round(float(sub.loc[t,'MaxDD']),2)) + "%") if t in sub.index else "N/A"
        md += "| **" + tk + "** | " + gr(0) + " | " + gr(20) + " | " + gr(25) + " | " + gr(30) + " | " + gd(30) + " |\n"

    out_md = 'top30_adx_report.md'
    with open(out_md, 'w', encoding='utf-8') as f:
        f.write(md)

    # Print summary
    print("="*70)
    print("ADX>30 vs No-Filter - Top 10 Improvement:")
    print("="*70)
    for _, row in imp_df.head(10).iterrows():
        sign = "+" if row['Gain'] > 0 else ""
        line = "  " + str(row['Ticker']).ljust(12)
        line += "  no-filter: " + str(round(row['NoFilter'],2)).rjust(8) + "%"
        line += "  ADX>30: " + str(round(row['ADX30'],2)).rjust(8) + "%"
        line += "  gain: " + sign + str(row['Gain']) + "%"
        print(line)
    print("="*70)
    print("Report saved to: " + out_md)


if __name__ == '__main__':
    main()
