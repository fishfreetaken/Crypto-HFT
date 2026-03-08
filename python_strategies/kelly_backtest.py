import yfinance as yf
import pandas as pd
import numpy as np
import random
import matplotlib.pyplot as plt

def run_kelly_backtest():
    print("Downloading BTC 5-minute data for the last 60 days (max allowed for 5m resolution)...")
    btc = yf.download('BTC-USD', period='60d', interval='5m')
    
    if isinstance(btc.columns, pd.MultiIndex):
        btc.columns = [c[0] for c in btc.columns]
        
    btc = btc.dropna()
    
    # 策略配置
    initial_cap = 10000.0
    capital = initial_cap
    
    # 我们假设模型有着"严格控制下"的50%胜率，这里盈亏比必须要大于1凯利公式才成立。
    # 我们设止损为 1.5%，止盈为 3%，盈亏比为 2:1
    win_rate = 0.50
    rr_ratio = 2.0   
    sl_pct = 0.015    
    tp_pct = sl_pct * rr_ratio 
    
    # 计算凯利风险比例: p - q/b
    full_kelly_pct = win_rate - ((1.0 - win_rate) / rr_ratio)
    
    # 使用半凯利 (Half-Kelly) 来规避极端回撤的破产风险
    kelly_fraction = 0.5 
    actual_risk_pct = full_kelly_pct * kelly_fraction
    
    print(f"--- 📊 核心策略参数配置 ---")
    print(f"预估胜率 (Win Rate): {win_rate*100}%")
    print(f"设定盈亏比 (RR Ratio): {rr_ratio}:1")
    print(f"止损点: {sl_pct*100}%, 止盈点: {tp_pct*100}%")
    print(f"全仓凯利风险暴露: {full_kelly_pct*100:.2f}% / 笔交易")
    print(f"采用半凯利风险暴露: {actual_risk_pct*100:.2f}% / 笔交易")
    print("------------------------------\n")
    
    if actual_risk_pct <= 0:
        print("凯利预期为负或者为0，此策略在数学上无法实现盈利。")
        return
        
    in_position = False
    direction = 0 # 1 代表做多 (Long), -1 代表做空 (Short)
    tp_price = 0.0
    sl_price = 0.0
    risk_amount = 0.0
    
    equity_curve = []
    timestamps = []
    
    trades = 0
    wins = 0
    losses = 0
    liquidated = False
    
    print("🚀 开始在真实历史K线中进行微观事件回测...\n")
    
    for idx, row in btc.iterrows():
        timestamps.append(idx)
        equity_curve.append(capital)
        
        if liquidated:
            continue
            
        if capital <= 0:
            liquidated = True
            print(f"\n💀 遭遇严重破产毁损，资金归零 (时间: {idx})")
            continue
            
        _open = float(row['Open'])
        _high = float(row['High'])
        _low = float(row['Low'])
        
        if not in_position:
            # 开启下一轮：无缝进场，正负极抛硬币模拟（因为用户要求模拟50%胜率）
            in_position = True
            direction = random.choice([1, -1]) 
            entry_price = _open
            
            # 当前这一把能够亏损的极限金额（由动态追踪的本金 * 凯利风控敞口得出）
            risk_amount = capital * actual_risk_pct
            
            # 计算绝对的止盈和止损价格
            if direction == 1:
                tp_price = entry_price * (1 + tp_pct)
                sl_price = entry_price * (1 - sl_pct)
            else:
                tp_price = entry_price * (1 - tp_pct)
                sl_price = entry_price * (1 + sl_pct)
                
            trades += 1
            
        # 监测是否有穿透止盈或止损边界
        hit_sl = False
        hit_tp = False
        
        if direction == 1:
            if _low <= sl_price: hit_sl = True
            if _high >= tp_price: hit_tp = True
        else:
            if _high >= sl_price: hit_sl = True
            if _low <= tp_price: hit_tp = True
            
        # 如果在同一根5分钟K线里同时打到了止盈和止损，在回测里为了保守估算，我们认定为吃到了止损
        if hit_sl and hit_tp:
            hit_sl = True
            hit_tp = False
            
        # 结算逻辑，“赚到盈亏点就立马下一轮”
        if hit_sl:
            capital -= risk_amount
            losses += 1
            in_position = False
        elif hit_tp:
            # 盈利 = 风险抵押金 * 盈亏比
            capital += risk_amount * rr_ratio
            wins += 1
            in_position = False

    # 最后如果是持仓状态强平掉
    if in_position:
        equity_curve[-1] = capital

    actual_win_rate = (wins / trades) * 100 if trades > 0 else 0
    
    print("=== 📈 过去 60 天 高频回测终局结果 ===")
    print(f"原始本金         : ${initial_cap:.2f}")
    print(f"总交易笔数       : {trades} 笔 (永不停歇流转)")
    print(f"获利笔数         : {wins} 笔")
    print(f"亏损笔数         : {losses} 笔")
    print(f"随机掷骰实际胜率: {actual_win_rate:.2f}% (大样本下回归至约50%)")
    print("------------------------------")
    print(f"结算后最终本金   : ${capital:.2f}")
    if capital > initial_cap:
        print(f"🏆 净利润         : +${capital - initial_cap:.2f}")
    else:
        print(f"🩸 净亏损         : -${initial_cap - capital:.2f}")
    print(f"总资产回报率(ROE): {((capital - initial_cap) / initial_cap) * 100:.2f}%")
    print("===============================\n")
    
    # 绘制资金曲线
    plt.style.use('dark_background')
    plt.figure(figsize=(14, 7))
    plt.plot(timestamps, equity_curve, color='lime' if capital > initial_cap else 'red', linewidth=1.5)
    plt.title(f'Continuous BT Kelly Equity Curve (RR {rr_ratio}:1, Risk: {actual_risk_pct*100:.2f}%)')
    plt.xlabel('Date / Time')
    plt.ylabel('Account Capital ($ USD)')
    plt.grid(True, linestyle='--', alpha=0.3)
    
    out_img = r'c:\Users\Administrator\Desktop\kelly_backtest_equity.png'
    plt.savefig(out_img, dpi=300, bbox_inches='tight')
    print(f"✅ 资金曲线已绘制完毕并保存至: {out_img}")

if __name__ == '__main__':
    run_kelly_backtest()
