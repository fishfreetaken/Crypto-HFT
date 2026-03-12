import matplotlib.pyplot as plt
from pyramiding_hourly import run_pyramiding_hourly_strategy

def multi_coin_compare():
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
    plt.style.use('dark_background')
    plt.figure(figsize=(16, 9))
    
    results = []
    
    for tk in tickers:
        print(f"\n=========================================================")
        print(f"🚀 开始回测 {tk} | 纯合1H级浮盈加仓 | 杠杆: 20x")
        
        # 调用底层的 pyramiding_hourly_strategy，关闭内部冗长的 logging，只看最终结果
        curve, roe = run_pyramiding_hourly_strategy(
            ticker=tk, 
            leverage_ratio=20, 
            trailing_pct=0.03, 
            pyramid_step_pct=0.015, 
            target_single_trade_profit=0, 
            verbose=False
        )
        
        if curve is not None:
            # 外部计算最终结算数据
            initial_cap = 100000.0
            final_eq = curve.iloc[-1]
            total_profit = final_eq - initial_cap
            peak = curve.expanding().max()
            drawdown = (curve - peak) / peak
            max_dd = drawdown.min() * 100
            
            print(f"   📊 最终净值: ${final_eq:.2f}")
            print(f"   🏆 净利润:  ${total_profit:.2f} (ROE: {roe:.2f}%)")
            print(f"   📉 最大回撤: {max_dd:.2f}%")
        
            results.append({'Asset': tk, 'ROE': roe})
            
            # 归一化为1.0方便在同一张图内对比
            normalized_curve = curve / 100000.0
            plt.plot(curve.index, normalized_curve, label=f"{tk} (ROE: {roe:.2f}%)", linewidth=2.5, alpha=0.85)

    if not results:
        print("未获得任何有效的回测数据！")
        return
            
    plt.title('Multi-Coin Pyramiding Comparison (20x Leverage, Pure 1H)', fontsize=18, pad=20, fontweight='bold', color='#FFD700')
    plt.suptitle('Performance of Fractional Compounding Strategy Across Top Crypto Assets', fontsize=12, y=0.91, color='#AAAAAA')
    plt.xlabel('Date / Time', fontsize=12, labelpad=10)
    plt.ylabel('Normalized Equity (1.0 = Initial Capital)', fontsize=12, labelpad=10)
    plt.axhline(1.0, color='white', linestyle='--', alpha=0.5)
    plt.legend(fontsize=12, loc='upper left', frameon=True, facecolor='#111111', edgecolor='#333333')
    plt.grid(True, linestyle=(0, (5, 10)), alpha=0.3, color='#555555')
    
    out_img = r'c:\Users\Administrator\Desktop\pyramiding_multi_result.png'
    plt.savefig(out_img, dpi=300, bbox_inches='tight')
    print(f"\n=========================================================")
    print(f"✅ 多币种横向对比收益曲线已生成并保存至: {out_img}")
    print("=========================================================")
    print("\n🏆 【综合收益率排行榜】:")
    results.sort(key=lambda x: x['ROE'], reverse=True)
    for i, r in enumerate(results):
        print(f"   {i+1}名: {r['Asset']:<8} | 最终ROE: {r['ROE']:>8.2f}%")
    print("=========================================================")

if __name__ == '__main__':
    multi_coin_compare()
