import sys

def transform_strategy():
    with open('pyramiding_15m.py', 'r', encoding='utf-8') as f:
        code = f.read()

    code = code.replace('run_pyramiding_15m_strategy', 'run_pyramiding_1m_strategy')
    code = code.replace('15M-Level', '1M-Level')
    code = code.replace("interval='15m'", "interval='1m'")
    code = code.replace("period='60d'", "period='7d'")
    code = code.replace('15M周期均线', '1M周期均线')
    code = code.replace('cooldown_candles = 24', 'cooldown_candles = 60')
    code = code.replace('trailing_pct=0.015', 'trailing_pct=0.005')
    code = code.replace('pyramid_step_pct=0.0075', 'pyramid_step_pct=0.0025')
    code = code.replace('15M信号', '1M信号')
    code = code.replace('15M宽幅', '1M宽幅')
    code = code.replace('15M级别', '1M级别')
    code = code.replace('15M回测', '1M回测')
    code = code.replace('15M 回测', '1M 回测')
    code = code.replace('15分钟', '1分钟')
    code = code.replace('pyramiding_15M_result.png', 'pyramiding_1m_result.png')
    code = code.replace('15M Timeframe', '1M Timeframe')

    with open('pyramiding_1m.py', 'w', encoding='utf-8') as f:
        f.write(code)

def transform_optimizer():
    with open('pyramiding_15m_optimizer_top50.py', 'r', encoding='utf-8') as f:
        code = f.read()

    code = code.replace('from pyramiding_15m import run_pyramiding_15m_strategy', 'from pyramiding_1m import run_pyramiding_1m_strategy')
    code = code.replace('run_pyramiding_15m_strategy', 'run_pyramiding_1m_strategy')
    code = code.replace("period = '60d'", "period = '7d'")
    code = code.replace("interval = '15m'", "interval = '1m'")
    code = code.replace("trailing_pcts = [0.01, 0.015, 0.02]", "trailing_pcts = [0.003, 0.005, 0.008]")
    code = code.replace("pyramid_step_pcts = [0.005, 0.0075, 0.01]", "pyramid_step_pcts = [0.002, 0.004, 0.006]")
    code = code.replace("15M", "1M")
    code = code.replace("15分钟", "1分钟")
    code = code.replace("top50_15m_report.md", "top50_1m_report.md")

    with open('pyramiding_1m_optimizer_top50.py', 'w', encoding='utf-8') as f:
        f.write(code)

if __name__ == '__main__':
    transform_strategy()
    transform_optimizer()
    print("Transformation complete! pyramiding_1m.py and pyramiding_1m_optimizer_top50.py generated.")
