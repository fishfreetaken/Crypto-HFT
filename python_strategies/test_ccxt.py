import traceback
import asyncio
import ccxt.async_support as ccxt

async def test():
    e = ccxt.binance()
    try:
        m = await e.load_markets()
        print('OK')
    except Exception as ex:
        traceback.print_exc()
    finally:
        await e.close()

asyncio.run(test())
