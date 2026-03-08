import yfinance as yf
import pandas as pd

def calculate_leverage():
    print("Fetching BTC data...")
    btc = yf.download('BTC-USD', period='6mo')
    
    if isinstance(btc.columns, pd.MultiIndex):
        btc.columns = [c[0] for c in btc.columns]
        
    btc.index = pd.to_datetime(btc.index).tz_localize(None)
    
    # 2026-01-15 data
    jan15_data = btc[(btc.index >= '2026-01-15') & (btc.index < '2026-01-16')]
    if jan15_data.empty:
        print("No data found for 2026-01-15.")
        return
        
    entry_price = float(jan15_data['High'].iloc[0])
    
    # Data from Entry point onwards
    post_entry = btc[btc.index >= '2026-01-15']
    lowest_price = float(post_entry['Low'].min())
    highest_price = float(post_entry['High'].max())
    current_price = float(btc['Close'].iloc[-1])
    
    # Standard maintenance margin rate (e.g. Binance usually 0.4% - 0.5% for BTC)
    mmr = 0.005 
    
    # Long Calculations
    # Liquidation Price = Entry Price * (1 - 1/Lev + MMR)
    # We need: Lowest Price > Liquidation Price
    # Lowest > Entry * (1 - 1/Lev + MMR)
    # 1/Lev > 1 + MMR - Lowest/Entry
    # Lev < 1 / (1 + MMR - Lowest/Entry)
    
    long_lev_den = (1 + mmr - lowest_price / entry_price)
    if long_lev_den > 0:
        max_long_lev = 1 / long_lev_den
    else:
        max_long_lev = 100.0 # Could be infinite if price went up
        
    liq_long = entry_price * (1 - 1/max_long_lev + mmr)
    
    # Short Calculations
    # Liquidation Price = Entry Price * (1 + 1/Lev - MMR)
    # We need: Highest Price < Liquidation Price
    # Highest < Entry * (1 + 1/Lev - MMR)
    # 1/Lev > Highest/Entry - 1 + MMR
    # Lev < 1 / (Highest/Entry - 1 + MMR)
    
    short_lev_den = (highest_price / entry_price - 1 + mmr)
    if short_lev_den > 0:
        max_short_lev = 1 / short_lev_den
    else:
        max_short_lev = 100.0 # Arbitrary max if it never exceeded entry
        
    liq_short = entry_price * (1 + 1/max_short_lev - mmr)
    
    # Short Profit (ROE)
    # Profit = (Entry - Current) / Entry * Leverage
    short_profit_pct = ((entry_price - current_price) / entry_price) * max_short_lev * 100
    
    print(f"Entry Price (Jan 15 High): {entry_price:.2f}")
    print(f"Lowest Price (Wash): {lowest_price:.2f}")
    print(f"Highest Price since entry: {highest_price:.2f}")
    print(f"Current Price: {current_price:.2f}")
    print("-" * 30)
    print("LONG POSITION:")
    print(f"Max Leverage (Strict Control): {max_long_lev:.2f}x")
    actual_long_lev = int(max_long_lev) if max_long_lev > 1 else max_long_lev
    print(f"Usable whole-number Leverage: {actual_long_lev}x")
    print(f"Liquidation Price at {actual_long_lev}x: {entry_price * (1 - 1/actual_long_lev + mmr):.2f}")
    print("-" * 30)
    print("SHORT POSITION:")
    print(f"Max Leverage (Strict Control): {max_short_lev:.2f}x")
    actual_short_lev = int(max_short_lev) if max_short_lev > 1 else max_short_lev
    print(f"Usable whole-number Leverage: {actual_short_lev}x")
    print(f"Liquidation Price at {actual_short_lev}x: {entry_price * (1 + 1/actual_short_lev - mmr):.2f}")
    print(f"Short ROE at {actual_short_lev}x Leverage: {((entry_price - current_price) / entry_price) * actual_short_lev * 100:.2f}%")

if __name__ == '__main__':
    calculate_leverage()
