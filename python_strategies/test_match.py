import re
f = open('binance_hft_trading.log', 'r', encoding='utf-8')
c = 0
for L in f:
    if '触点强平' in L:
        m = re.search(r'触点强平\]\s+(\w+).*?波动PnL:\s*\$([\-\d\.]+)', L)
        if m: c+=1
print("matched:", c)
