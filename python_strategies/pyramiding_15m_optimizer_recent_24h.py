import yfinance as yf
import pandas as pd
import numpy as np
import itertools
from concurrent.futures import ProcessPoolExecutor
import time
import os

from pyramiding_15m import run_pyramiding_15m_strategy

def evaluate_params(args):
    ticker, df, leverage_ratio, trailing_pct, pyramid_step_pct = args
    curve, roe = run_pyramiding_15m_strategy(
        ticker=ticker, 
        df=df, 
        leverage_ratio=leverage_ratio, 
        trailing_pct=trailing_pct, 
        pyramid_step_pct=pyramid_step_pct, 
        target_single_trade_profit=0, 
        verbose=False
    )
    if curve is None or len(curve) == 0:
        return (ticker, leverage_ratio, trailing_pct, pyramid_step_pct, 0.0, 0.0)
    initial_cap = 100000.0
    peak = curve.expanding().max()
    drawdown = (curve - peak) / peak
    max_dd = drawdown.min() * 100
    return (ticker, leverage_ratio, trailing_pct, pyramid_step_pct, roe, max_dd)

def run():
    period = '2d'
    interval = '15m'

    tickers = [
        'BTC-USD', 'ETH-USD', 'BNB-USD', 'SOL-USD', 'XRP-USD', 
        'DOGE-USD', 'ADA-USD', 'SHIB-USD', 'AVAX-USD', 'DOT-USD', 
        'LINK-USD', 'TRX-USD', 'BCH-USD', 'LTC-USD', 'NEAR-USD', 
        'XLM-USD', 'ATOM-USD', 'ICP-USD', 'FIL-USD', 'FET-USD', 
        'ARB-USD', 'RENDER-USD', 'HBAR-USD', 'INJ-USD', 'OP-USD', 
        'VET-USD', 'ALGO-USD', 'WLD-USD', 'AAVE-USD', 'CRV-USD',
        'DASH-USD', 'EGLD-USD', 'ENJ-USD', 'EOS-USD', 'GALA-USD', 
        'MANA-USD', 'MKR-USD', 'NEO-USD', 'RUNE-USD', 'SAND-USD', 
        'SNX-USD', 'THETA-USD', 'ZEC-USD', 'XTZ-USD', 'LDO-USD', 
        'CHZ-USD', 'KLAY-USD', 'XEC-USD', 'ZIL-USD', 'MINA-USD'
    ]
    
    data_cache = {}
    print(f"Downloading data for 50 tickers for the last {period} at {interval} interval...")
    
    for tk in tickers:
        try:
            df = yf.download(tk, period=period, interval=interval, progress=False)
            if df.empty:
                print(f"Skipping {tk}: no data")
                continue
            if isinstance(df.columns, pd.MultiIndex):
                df.columns = [c[0] for c in df.columns]
            data_cache[tk] = df.dropna()
        except:
            print(f"Skipping {tk}: error fetching")

    leverage_ratios = [10, 15, 20, 25]
    trailing_pcts = [0.01, 0.015, 0.02]
    pyramid_step_pcts = [0.005, 0.0075, 0.01]
    
    # 4 x 3 x 3 = 36 combinations per coin. 36 * 30 = ~1080 tasks.
    search_space = list(itertools.product(data_cache.keys(), leverage_ratios, trailing_pcts, pyramid_step_pcts))
    
    tasks = [(tk, data_cache[tk], lv, tr, py) for tk, lv, tr, py in search_space]
    
    print(f"Starting {len(tasks)} optimization tasks across {os.cpu_count() or 4} CPU cores...")
    results = []
    with ProcessPoolExecutor(max_workers=os.cpu_count() or 4) as executor:
        for res in executor.map(evaluate_params, tasks):
            results.append(res)
            
    res_df = pd.DataFrame(results, columns=['Ticker', 'Leverage', 'Trailing_Stop', 'Pyramid_Step', 'ROE(%)', 'Max_Drawdown(%)'])
    
    md_content = "# Top 50 加密货币 15M浮盈加仓策略 最优参数研究报告\n\n"
    md_content += "> 📈 **策略核心**: 15分钟级别 EMA20/50 顺势突破 + 分形几何金字塔浮盈大倍率加仓 + 极窄移动止损截断亏损\n"
    md_content += f"> 📅 **回测周期**: 近 {period} (包含日内各种突发极端波动)\n"
    md_content += "> ⚙️ **参数网格**: 杠杆 (10x-25x), 追踪止损点 (1%-2%), 加仓触发间隔 (0.5%-1%)\n\n"
    
    md_content += "本报告罗列了当前市场上交易量最活跃的 50 种加密货币在该无脑滚雪球策略下(15M级别)的**最强获利参数组合**与抗打击极限。\n\n"
    md_content += "---\n\n"
    
    # 获取每个币种最大总收益的行
    best_overall = res_df.loc[res_df.groupby('Ticker')['ROE(%)'].idxmax()].sort_values(by='ROE(%)', ascending=False)
    
    md_content += "## 🏆 综合战力霸主榜单 (全品种绝对回报率 Top 排行)\n\n"
    md_content += "| 排名 | 币种 (Ticker) | 封神杠杆 | 获利回吐死线 (Stop) | 最优加仓步长 (Step) | 最大 ROE (%) | 极限回撤 (%) |\n"
    md_content += "|:---:|:---|:---:|:---:|:---:|---:|---:|\n"
    
    rank = 1
    for _, row in best_overall.iterrows():
        if row['ROE(%)'] <= 0:
            continue
        md_content += f"| {rank} | **{row['Ticker']}** | {int(row['Leverage'])}x | **{row['Trailing_Stop']*100:.2f}%** | {row['Pyramid_Step']*100:.2f}% | <span style='color:green'>+{row['ROE(%)']:.2f}%</span> | <span style='color:red'>{row['Max_Drawdown(%)']:.2f}%</span> |\n"
        rank += 1
        
    md_content += "\n---\n\n## 📊 各大币种核心参数微调指南 (Top 3 阵型)\n\n"
    md_content += "针对不同币种的15分钟微观洗盘结构（日内插针深度与单边持续力不同），即使是同样的信号引擎，也必须适配专属的加挂武器模块才能最大化生存和杀伤。\n\n"
    
    for tk in best_overall['Ticker']:
        if best_overall[best_overall['Ticker'] == tk]['ROE(%)'].iloc[0] <= 0:
             continue
        md_content += f"### {tk} \n"
        md_content += f"| 杠杆 (Leverage) | 追踪止损 (Trailing Stop) | 加仓步幅 (Pyramid Step) | 预期总收益 (ROE %) | 预期最大回撤 (Max DD) |\n"
        md_content += f"|---:|---:|---:|---:|---:|\n"
        sub_df = res_df[res_df['Ticker'] == tk].sort_values(by='ROE(%)', ascending=False).head(3)
        for _, row in sub_df.iterrows():
            md_content += f"| {int(row['Leverage'])}x | {row['Trailing_Stop']*100:.2f}% | {row['Pyramid_Step']*100:.2f}% | **+{row['ROE(%)']:.2f}%** | {row['Max_Drawdown(%)']:.2f}% |\n"
        md_content += "\n"
        
    with open('top50_15m_report_24h.md', 'w', encoding='utf-8') as f:
        f.write(md_content)
        
    print("Done. Saved to top50_15m_report_24h.md")

if __name__ == '__main__':
    run()
