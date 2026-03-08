"""
利润锁定机制（Profit Sweep）效果对比测试
- 对比 1年周期 vs 2个月短周期，开关保险箱的差异
- 展示"非对称回撤性吞噬"在长期的破坏力，以及保险箱的修复效果
"""
import yfinance as yf
import pandas as pd
import numpy as np
import matplotlib.pyplot as plt
import matplotlib.gridspec as gridspec

from pyramiding_hourly import run_pyramiding_hourly_strategy

def run_comparison():
    tickers = ['BTC-USD', 'OP-USD', 'ETH-USD']

    # 两段时间窗口
    periods = [
        {'label': '2个月 (2026.1-2026.3)', 'start': '2026-01-01', 'end': '2026-03-09'},
        {'label': '1年 (2025.1-2026.3)',   'start': '2025-01-01', 'end': '2026-03-09'},
    ]

    # 参数配置
    configs = [
        {'enable_profit_sweep': False, 'label': '❌ 无保险箱 (纯复利)', 'color': '#FF6B6B'},
        {'enable_profit_sweep': True,  'sweep_multiplier': 2.0, 'sweep_keep_ratio': 0.5,
         'label': '🔒 有保险箱 (翻倍锁50%)', 'color': '#4CAF50'},
    ]

    plt.style.use('dark_background')
    fig = plt.figure(figsize=(20, 14))
    fig.suptitle('💡 利润锁定保险箱效果横向对比\n(Profit Sweep Mechanism: 长短周期真实回报稳定性验证)', 
                 fontsize=15, fontweight='bold', color='#FFD700', y=0.98)

    gs = gridspec.GridSpec(len(tickers), len(periods), figure=fig, hspace=0.5, wspace=0.35)

    summary_rows = []

    for ti, tk in enumerate(tickers):
        print(f"\n{'='*60}")
        print(f"  {tk}")
        print(f"{'='*60}")

        for pi, period in enumerate(periods):
            ax = fig.add_subplot(gs[ti, pi])
            ax.set_title(f'{tk} | {period["label"]}', fontsize=10, color='#AAAAAA', pad=6)
            ax.set_xlabel('时间', fontsize=8, color='#888888')
            ax.set_ylabel('总财富 (USD)', fontsize=8, color='#888888')
            ax.tick_params(colors='#888888', labelsize=7)
            ax.grid(True, linestyle=':', alpha=0.2, color='#555555')
            ax.axhline(100000, color='white', linestyle='--', alpha=0.3, linewidth=0.8)

            # 下载数据一次，共用
            df = yf.download(tk, start=period['start'], end=period['end'], interval='1h', progress=False)
            if df.empty:
                ax.text(0.5, 0.5, 'No Data', ha='center', va='center', transform=ax.transAxes, color='gray')
                continue
            if isinstance(df.columns, pd.MultiIndex):
                df.columns = [c[0] for c in df.columns]
            df = df.dropna()

            for cfg in configs:
                curve, roe = run_pyramiding_hourly_strategy(
                    ticker=tk,
                    df=df,
                    leverage_ratio=20,
                    trailing_pct=0.03,
                    pyramid_step_pct=0.015,
                    enable_profit_sweep=cfg['enable_profit_sweep'],
                    sweep_multiplier=cfg.get('sweep_multiplier', 2.0),
                    sweep_keep_ratio=cfg.get('sweep_keep_ratio', 0.5),
                    verbose=False
                )

                if curve is not None:
                    final = curve.iloc[-1]
                    peak = curve.expanding().max()
                    dd = ((curve - peak) / peak).min() * 100

                    ax.plot(curve.index, curve.values, 
                            label=f"{cfg['label']}\nROE: {roe:.1f}%  MaxDD: {dd:.1f}%",
                            color=cfg['color'], linewidth=1.5, alpha=0.9)
                    
                    print(f"  [{period['label']}] {cfg['label']}")
                    print(f"    最终净值: ${final:,.0f}  ROE: {roe:.2f}%  最大回撤: {dd:.2f}%")

                    summary_rows.append({
                        'Ticker': tk, 'Period': period['label'],
                        'Mode': cfg['label'], 'ROE(%)': roe,
                        'MaxDD(%)': dd, 'Final($)': final
                    })

            ax.legend(fontsize=6.5, loc='upper left', frameon=True, 
                      facecolor='#1a1a2e', edgecolor='#333333', framealpha=0.85)
            ax.fill_between(curve.index, 100000, curve.values,
                            where=(curve.values > 100000), alpha=0.05, color='#4CAF50')

    out_img = r'c:\Users\Administrator\Desktop\profit_sweep_comparison.png'
    plt.savefig(out_img, dpi=220, bbox_inches='tight', facecolor='#0d0d1a')
    print(f"\n✅ 对比图已保存至: {out_img}")

    # 打印汇总表
    sum_df = pd.DataFrame(summary_rows)
    print("\n" + "="*90)
    print("📊 汇总对比表 (ROE %)")
    print("="*90)
    for tk in tickers:
        print(f"\n  【{tk}】")
        sub = sum_df[sum_df['Ticker'] == tk][['Period', 'Mode', 'ROE(%)', 'MaxDD(%)']].copy()
        sub['ROE(%)'] = sub['ROE(%)'].apply(lambda x: f"+{x:.2f}%" if x > 0 else f"{x:.2f}%")
        sub['MaxDD(%)'] = sub['MaxDD(%)'].apply(lambda x: f"{x:.2f}%")
        print(sub.to_string(index=False))
    print("="*90)

if __name__ == '__main__':
    run_comparison()
