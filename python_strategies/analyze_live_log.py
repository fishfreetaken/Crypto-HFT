import re
from collections import defaultdict

def analyze_log(logfile):
    try:
        with open(logfile, 'r', encoding='utf-8') as f:
            lines = f.readlines()
    except Exception as e:
        print(f"Error reading {logfile}: {e}")
        return

    trades = defaultdict(list)
    total_closed = 0
    winning_trades = 0
    losing_trades = 0
    total_pnl = 0.0
    
    ticker_pnl = defaultdict(float)
    ticker_trades = defaultdict(int)

    for line in lines:
        if "[平仓]" in line:
            # Example: [2026-03-10 12:00:00] 💥 [平仓] BTC-USD 空单离场 (触发移动止损/获利线: $60000.0000) | 单笔PnL: $-50.00 | 余额: $950.00
            match = re.search(r"\[平仓\] ([\w\-]+) .*单笔PnL: \$?([\-\d\.]+)", line)
            if match:
                tk = match.group(1)
                pnl = float(match.group(2))
                
                total_closed += 1
                ticker_trades[tk] += 1
                ticker_pnl[tk] += pnl
                total_pnl += pnl
                
                if pnl > 0:
                    winning_trades += 1
                else:
                    losing_trades += 1

    print("=== 实盘日志硬核解析 (Live Paper Trading Analysis) ===")
    print(f"总平仓次数 (Total Closed Trades): {total_closed}")
    print(f"盈利次数 (Winning Trades)       : {winning_trades}")
    print(f"亏损次数 (Losing Trades)        : {losing_trades}")
    win_rate = (winning_trades / total_closed * 100) if total_closed > 0 else 0
    print(f"实盘真实胜率 (Win Rate)         : {win_rate:.2f}%")
    print(f"已实现总盈亏 (Realized PnL)     : ${total_pnl:.2f}")
    
    print("\n--- 亏损重灾区 Top 5 一览 ---")
    sorted_pnl = sorted(ticker_pnl.items(), key=lambda x: x[1])
    for tk, pnl in sorted_pnl[:5]:
        print(f"{tk:10}: ${pnl:.2f} (交易 {ticker_trades[tk]} 次)")

    print("\n--- 盈利贡献 Top 5 一览 ---")
    sorted_pnl_win = sorted(ticker_pnl.items(), key=lambda x: x[1], reverse=True)
    for tk, pnl in sorted_pnl_win[:5]:
        print(f"{tk:10}: ${pnl:.2f} (交易 {ticker_trades[tk]} 次)")

if __name__ == "__main__":
    analyze_log("live_paper_trading.log")
