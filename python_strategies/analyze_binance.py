import sys
import re

log_file = sys.argv[1] if len(sys.argv) > 1 else "binance_hft_trading.log"

max_balance = -1.0
min_balance = float('inf')
current_balance = 0.0
start_balance = 0.0

trades_closed = 0
win_trades = 0
loss_trades = 0
total_profit = 0.0
total_vault = 0.0
pnl_by_ticker = {}

try:
    with open(log_file, 'r', encoding='utf-8') as f:
        first_bal_found = False
        for line in f:
            if "触点强平" in line or "目标止盈" in line:
                match = re.search(r"\] (🚩 \[目标止盈\]|💥 \[触点强平\]) (\w+) .*?波动PnL: \$([\-\d\.]+)", line)
                if not match:
                    match = re.search(r"(?:触点强平|目标止盈)\]?\s+(\w+).*?波动PnL:\s*\$([\-\d\.]+)", line)
                
                if match:
                    tk = match.group(1) if len(match.groups())==2 else match.group(2)
                    pnl_str = match.group(2) if len(match.groups())==2 else match.group(3)
                    pnl = float(pnl_str)
                    
                    trades_closed += 1
                    total_profit += pnl
                    if pnl > 0: win_trades += 1
                    else: loss_trades += 1
                    
                    if tk not in pnl_by_ticker:
                        pnl_by_ticker[tk] = {'pnl': 0.0, 'count': 0}
                    pnl_by_ticker[tk]['pnl'] += pnl
                    pnl_by_ticker[tk]['count'] += 1

            if "利润分配" in line:
                match = re.search(r"锁定\s*\$([\d\.\,]+)", line)
                if match:
                    total_vault += float(match.group(1).replace(",",""))

            if "HFT账户总面值:" in line:
                match = re.search(r"HFT账户总面值:\s*\$(\-?[\d\,\.]+)", line)
                if match:
                    bal = float(match.group(1).replace(",", ""))
                    if not first_bal_found:
                        start_balance = bal
                        first_bal_found = True
                    if bal > max_balance: max_balance = bal
                    if bal < min_balance: min_balance = bal
                    current_balance = bal
                    
except Exception as e:
    print(f"Error parsing log file {log_file}: {e}")

print(f"\n==========================================")
print(f"🔥 本次战役极速复盘战报 (自动生成)")
print(f"📂 解析日志文件: {log_file}")
print(f"==========================================")

print(f"总平仓交割数: {trades_closed} 笔")
if trades_closed > 0:
    print(f"交割胜率: {(win_trades/trades_closed)*100:.2f}% ({win_trades} 胜 / {loss_trades} 败)")
print(f"已实现平仓波动 PnL: ${total_profit:.2f}")

if start_balance > 0:
    print(f"\n起始分配总面值: ${start_balance:,.2f}")
if max_balance != -1.0:
    print(f"战役期最高净值: ${max_balance:,.2f}")
if min_balance != float('inf'):
    print(f"战役期最深回撤: ${min_balance:,.2f}")
print(f"断流收盘时总面值: ${current_balance:,.2f}")
print(f"保险箱锁定防守利润: ${total_vault:,.2f}")

if trades_closed > 0:
    print("\n--- 🩸 亏损重灾区 Top 5 ---")
    sorted_pnl = sorted(pnl_by_ticker.items(), key=lambda x: x[1]['pnl'])
    for tk, data in sorted_pnl[:5]:
        print(f"{tk:10}: ${data['pnl']:8.2f} (交易 {data['count']} 次)")

    print("\n--- 💰 利润护城河 Top 5 ---")
    sorted_pnl_win = sorted(pnl_by_ticker.items(), key=lambda x: x[1]['pnl'], reverse=True)
    profits = [(k, v) for k, v in sorted_pnl_win if v['pnl'] > 0]
    if profits:
        for tk, data in profits[:5]:
            print(f"{tk:10}: ${data['pnl']:8.2f} (交易 {data['count']} 次)")
    else:
        print("暂无产生盈利离场的标的。")
print(f"==========================================")
