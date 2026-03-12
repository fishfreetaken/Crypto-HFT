with open('pyramiding_15m_optimizer_top50.py', 'r', encoding='utf-8') as f:
    content = f.read()

content = content.replace("period = '60d'", "period = '2d'")
content = content.replace("top50_15m_report.md", "top50_15m_report_24h.md")
content = content.replace("近 60d", "近两天（昨晚至今）")

with open('pyramiding_15m_optimizer_recent_24h.py', 'w', encoding='utf-8') as f:
    f.write(content)

print("Generated pyramiding_15m_optimizer_recent_24h.py and changed period to 2d")
