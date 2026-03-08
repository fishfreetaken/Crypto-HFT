import yfinance as yf
import pandas as pd
import matplotlib.pyplot as plt
import matplotlib.dates as mdates

def generate_chart():
    # Set style
    plt.style.use('dark_background')

    print("Downloading BTC-USD data for the last 2 years...")
    btc = yf.download('BTC-USD', period='2y')

    # If format is multi-index (recent yfinance versions), flatten it
    if isinstance(btc.columns, pd.MultiIndex):
        btc.columns = [c[0] for c in btc.columns]

    # Calculate EMA 200
    btc['EMA_200'] = btc['Close'].ewm(span=200, adjust=False).mean()

    # Calculate RSI (14 periods)
    delta = btc['Close'].diff()
    # Use exponential moving average for RSI logic per Wilder's original smoothing
    gain = delta.where(delta > 0, 0.0)
    loss = -delta.where(delta < 0, 0.0)
    avg_gain = gain.ewm(alpha=1/14, min_periods=14, adjust=False).mean()
    avg_loss = loss.ewm(alpha=1/14, min_periods=14, adjust=False).mean()
    rs = avg_gain / avg_loss
    btc['RSI_14'] = 100 - (100 / (1 + rs))

    # Calculate MACD (12, 26, 9)
    ema_12 = btc['Close'].ewm(span=12, adjust=False).mean()
    ema_26 = btc['Close'].ewm(span=26, adjust=False).mean()
    btc['MACD'] = ema_12 - ema_26
    btc['MACD_Signal'] = btc['MACD'].ewm(span=9, adjust=False).mean()
    btc['MACD_Hist'] = btc['MACD'] - btc['MACD_Signal']

    # Plotting
    fig = plt.figure(figsize=(14, 10))
    grid = plt.GridSpec(4, 1, hspace=0.3)

    # 1. Price and EMA 200
    ax1 = fig.add_subplot(grid[0:2, 0])
    ax1.plot(btc.index, btc['Close'], label='BTC Price', color='cyan', linewidth=1.5)
    ax1.plot(btc.index, btc['EMA_200'], label='EMA 200', color='orange', linewidth=2)
    ax1.set_title('Bitcoin (BTC-USD) Price vs EMA 200 (Last 2 Years)', fontsize=16)
    ax1.set_ylabel('Price (USD)')
    ax1.legend(loc='upper left')
    ax1.grid(True, linestyle='--', alpha=0.3)

    # 2. MACD
    ax2 = fig.add_subplot(grid[2, 0], sharex=ax1)
    ax2.plot(btc.index, btc['MACD'], label='MACD', color='lightblue')
    ax2.plot(btc.index, btc['MACD_Signal'], label='Signal', color='magenta')
    # Use normal bar chart for histogram with colors
    colors = ['green' if val >= 0 else 'red' for val in btc['MACD_Hist']]
    ax2.bar(btc.index, btc['MACD_Hist'], width=1, label='Histogram', color=colors)
    ax2.set_ylabel('MACD')
    ax2.legend(loc='upper left')
    ax2.grid(True, linestyle='--', alpha=0.3)

    # 3. RSI
    ax3 = fig.add_subplot(grid[3, 0], sharex=ax1)
    ax3.plot(btc.index, btc['RSI_14'], label='RSI (14)', color='yellow')
    ax3.axhline(70, color='red', linestyle='--', alpha=0.5)
    ax3.axhline(30, color='green', linestyle='--', alpha=0.5)
    ax3.set_ylabel('RSI')
    ax3.legend(loc='upper left')
    ax3.grid(True, linestyle='--', alpha=0.3)

    # Format x-axis
    ax3.xaxis.set_major_formatter(mdates.DateFormatter('%Y-%m'))
    plt.xticks(rotation=45)

    plt.tight_layout()

    # Save figure to desktop
    output_path = r'c:\Users\Administrator\Desktop\btc_ema200_analysis.png'
    plt.savefig(output_path, dpi=300, bbox_inches='tight')
    print(f"Chart successfully saved to: {output_path}")

if __name__ == '__main__':
    generate_chart()
