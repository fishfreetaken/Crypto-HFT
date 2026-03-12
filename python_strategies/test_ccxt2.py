import traceback
import asyncio
import ccxt.async_support as ccxt

async def test():
    e = ccxt.binance()
    try:
        m = await e.load_markets()
        print('OK')
    except Exception as ex:
        with open('real_err.txt', 'w') as err_file:
            err_file.write(traceback.format_exc())
    finally:
        await e.close()

asyncio.run(test())
