import glob
import re
import os

replacement = """    tickers = [
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
    ]"""

for f in glob.glob('*.py'):
    if f == 'update_tickers.py': continue
    
    with open(f, 'r', encoding='utf-8') as file:
        content = file.read()
    
    if 'tickers = [' in content:
        if 'BNB-USD' not in content and 'XRP-USD' not in content and len(re.findall(r'-USD', content)) < 10:
             continue # skip files with only 2-3 tickers
        
        # Regex to match tickers list in Python
        pattern = r'^[ \t]*tickers\s*=\s*\[.*?\]'
        new_content = re.sub(pattern, replacement, content, flags=re.DOTALL | re.MULTILINE)
        
        # We want to replace references inside the current logic as well:
        new_content = new_content.replace('30 种加密货币', '50 种加密货币')
        new_content = new_content.replace('30 种', '50 种')
        new_content = new_content.replace('30个', '50个')
        new_content = new_content.replace('30 tickers', '50 tickers')
        new_content = new_content.replace('top30', 'top50')
        new_content = new_content.replace('Top 30', 'Top 50')
        new_content = new_content.replace('x 30 =', 'x 50 =')
        
        if new_content != content:
            new_f = f.replace('top30', 'top50')
            with open(new_f, 'w', encoding='utf-8') as file:
                file.write(new_content)
            print(f'Created/Updated {new_f}')
            if f != new_f:
                 os.remove(f)
                 print(f"Removed {f}")
                 
# Handle markdown report renames if existing
for f in glob.glob('top30*.md'):
    new_f = f.replace('top30', 'top50')
    try:
        os.rename(f, new_f)
        print(f"Renamed {f} to {new_f}")
    except:
        pass
