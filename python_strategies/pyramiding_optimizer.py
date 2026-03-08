import yfinance as yf
import pandas as pd
import numpy as np
import itertools
from concurrent.futures import ProcessPoolExecutor
import time
import os

from pyramiding_hourly import run_pyramiding_hourly_strategy

def evaluate_params(args):
    ticker, df, leverage_ratio, trailing_pct, pyramid_step_pct = args
    
    # 调用底层策略，关闭冗长输出以防终端爆炸
    curve, roe = run_pyramiding_hourly_strategy(
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

def run_optimizer():
    # 测试的币种范围
    tickers = ['BTC-USD', 'ETH-USD']
    
    # 避免子进程疯狂调用下载受到限流，我们一次性在主进程把所有需要的数据下载好
    data_cache = {}
    print(">>> 正在为主进程下载缓存数据...")
    for tk in tickers:
        df = yf.download(tk, start='2026-01-01', end='2026-03-09', interval='1h', progress=False)
        if isinstance(df.columns, pd.MultiIndex):
            df.columns = [c[0] for c in df.columns]
        data_cache[tk] = df.dropna()
        print(f"[{tk}] 数据下载完成! 共 {len(data_cache[tk])} 根K线")

    # 参数网格空间定义
    leverage_ratios = [10, 15, 20, 25]
    trailing_pcts = [0.02, 0.03, 0.04, 0.05]
    pyramid_step_pcts = [0.01, 0.015, 0.02, 0.025]
    
    # 建立测试列表
    search_space = list(itertools.product(tickers, leverage_ratios, trailing_pcts, pyramid_step_pcts))
    
    tasks = []
    for tk, lv, tr, py in search_space:
        tasks.append((tk, data_cache[tk], lv, tr, py))
        
    print(f"\n🚀 开始网格参数暴力搜索: 共计 {len(tasks)} 种组合的宇宙大碰撞！(采用多核并发)")
    start_time = time.time()
    
    results = []
    
    # 使用基于 CPU 核心数的多进程池
    with ProcessPoolExecutor(max_workers=os.cpu_count() or 4) as executor:
        for res in executor.map(evaluate_params, tasks):
            results.append(res)
            
    print(f"搜索完成！耗时: {time.time() - start_time:.2f} 秒\n")
    
    # 将结果转换为 DataFrame 方便排序和呈现
    res_df = pd.DataFrame(results, columns=['Ticker', 'Leverage', 'Trailing_Stop', 'Pyramid_Step', 'ROE(%)', 'Max_Drawdown(%)'])
    
    # 分别为每一个币种打印最优前 5 名
    for tk in tickers:
        print(f"=========================================================================")
        print(f"🏆 【{tk}】 最强获利参数兵器谱 (Top 5 暴力组合):")
        sub_df = res_df[res_df['Ticker'] == tk].sort_values(by='ROE(%)', ascending=False).head(5)
        
        print(f"{'Leverage':<10} | {'Trailing_Stop':<15} | {'Pyramid_Step':<15} || {'ROE(%)':<12} | {'Max_Drawdown(%)'}")
        print("-" * 75)
        for _, row in sub_df.iterrows():
            print(f"{int(row['Leverage']):<10} | {row['Trailing_Stop']*100:>14.2f}% | {row['Pyramid_Step']*100:>14.2f}% || +{row['ROE(%)']:<11.2f} | {row['Max_Drawdown(%)']:.2f}%")
        print("=========================================================================\n")

if __name__ == '__main__':
    run_optimizer()
