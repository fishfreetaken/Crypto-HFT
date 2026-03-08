import ccxt
import pandas as pd
import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from datetime import timedelta
import time
import sys
import subprocess

def install_deps():
    try:
        from fbm import FBM
    except ImportError:
        print("Installing fbm library...")
        subprocess.check_call([sys.executable, "-m", "pip", "install", "fbm", "matplotlib"])

def generate_fbm(n, hurst):
    from fbm import FBM
    f = FBM(n=n, hurst=hurst, length=1)
    return f.fbm()

def simulate_future():
    install_deps()
    exchange = ccxt.okx()
    symbol = 'BTC/USDT'
    tf = '1d' 
    since = exchange.parse8601('2024-03-08T00:00:00Z')
    until = exchange.parse8601('2026-03-08T00:00:00Z')

    print(f"Fetching historical {symbol} data from 2024-03-08 to 2026-03-08...")
    data = []
    current_since = since
    while current_since < until:
        batch = exchange.fetch_ohlcv(symbol, tf, since=current_since, limit=100)
        if not batch: break
        batch = [b for b in batch if b[0] <= until]
        data.extend(batch)
        current_since = batch[-1][0] + 1
        time.sleep(0.1)

    if not data:
        print("No data fetched.")
        return

    df = pd.DataFrame(data, columns=['ts', 'o', 'h', 'l', 'c', 'v'])
    df['date'] = pd.to_datetime(df['ts'], unit='ms')
    df = df[~df['ts'].duplicated()]
    
    historical_prices = df['c'].values
    historical_dates = df['date'].values

    n_hist = len(historical_prices) - 1
    print(f"Historical data points: {n_hist+1}")

    # Generate future dates (2 years * 365 days = 730 days)
    n_future = 2 * 365
    print(f"Simulating future 2 years ({n_future} days)...")
    
    last_date = historical_dates[-1]
    future_dates = [last_date + np.timedelta64(i+1, 'D') for i in range(n_future)]
    future_dates = np.array(future_dates)
    
    total_dates = np.concatenate([historical_dates, future_dates])
    n_total = len(total_dates) - 1

    hurst = 0.65 
    print(f"Generating full fBm path (Hurst = {hurst}) for {n_total + 1} points...")
    
    # Generate one continuous fBm for history + future
    fbm_sample = generate_fbm(n_total, hurst)

    # Use Geometric Fractional Brownian Motion (fGBM) to prevent negative prices.
    # We model the logarithm of the price instead of the absolute price.
    log_hist_prices = np.log(historical_prices)
    
    # Scale using historical log-volatility
    std_real_log = np.std(log_hist_prices)
    std_fbm_hist = np.std(fbm_sample[:n_hist+1])
    scaled_fbm = fbm_sample * (std_real_log / std_fbm_hist)
    
    # Align starting point in log space
    aligned_log_sim = scaled_fbm - scaled_fbm[0] + log_hist_prices[0]
    
    # Drift override in log space: make historical simulation part end precisely at today's log price
    real_log_drift = log_hist_prices[-1] - log_hist_prices[0]
    sim_hist_log_drift = aligned_log_sim[n_hist] - aligned_log_sim[0]
    drift_diff = real_log_drift - sim_hist_log_drift
    
    # Apply a linear drift correction across the ENTIRE log simulation
    drift_adj = np.linspace(0, drift_diff * (n_total / n_hist), n_total + 1)
    aligned_log_sim += drift_adj

    # Convert back from log prices to absolute prices (exponentiate). This guarantees price > 0.
    aligned_sim = np.exp(aligned_log_sim)

    # Split into history and future for plotting
    sim_history = aligned_sim[:n_hist+1]
    sim_future = aligned_sim[n_hist:]

    # Enhance visual styling
    plt.figure(figsize=(16, 8), facecolor='#1e1e1e')
    ax = plt.gca()
    ax.set_facecolor('#1e1e1e')
    ax.tick_params(colors='white')
    for spine in ax.spines.values():
        spine.set_color('#555555')

    # Plot real history
    plt.plot(historical_dates, historical_prices, label='Real BTC Price (History)', color='#f7931a', linewidth=2, zorder=3)
    
    # Plot simulated history (fBm)
    plt.plot(historical_dates, sim_history, label=f'Simulated History (fBm)', color='#5bc0de', alpha=0.6, linewidth=1.5, zorder=2)
    
    # Plot simulated future (fBm)
    # prepend the last point of history to make the line continuous
    hist_end_date = [historical_dates[-1]]
    fut_dates_concat = np.concatenate([hist_end_date, future_dates])
    plt.plot(fut_dates_concat, sim_future, label=f'Simulated Future (Next 2 Years)', color='#e74c3c', linewidth=2, linestyle='--', zorder=4)

    # Mark "TODAY" divider
    plt.axvline(x=last_date, color='white', linestyle=':', alpha=0.5, zorder=1)
    plt.text(last_date + np.timedelta64(15, 'D'), ax.get_ylim()[0] + (ax.get_ylim()[1] - ax.get_ylim()[0])*0.05, 'TODAY\n(Mar 8)', color='white', alpha=0.7)

    plt.title(f'BTC/USDT 2-Year Real vs 2-Year Future Projection (Geometric fBm, H={hurst})', fontsize=16, color='white', pad=20)
    plt.xlabel('Date', fontsize=12, color='white', labelpad=10)
    plt.ylabel('Price (USDT)', fontsize=12, color='white', labelpad=10)
    
    legend = plt.legend(fontsize=12, facecolor='#2a2a2a', edgecolor='#555555', loc='upper left')
    for text in legend.get_texts():
        text.set_color("white")
        
    plt.grid(True, alpha=0.15, color='#ffffff')
    plt.tight_layout()
    plt.savefig('fractal_simulation_future.png', dpi=300, facecolor='#1e1e1e')
    print("Saved future projection plot to fractal_simulation_future.png")

if __name__ == '__main__':
    simulate_future()
