# 趋势反转对冲策略（TRH）实现计划

> **策略类型**：`trend_reversion_hedge`
> **日期**：2026-02-28（v3，A型极速化）
> **策略定位**：A型极速对冲（≤15min）· B型长期趋势跟随 · 速率感知 · 不均等资金分配

---

## 一、核心设计原则：速率决定方向

加密市场的价格大幅波动分为两种本质不同的形态，决定了截然不同的操作方向：

| 信号类型 | 触发特征 | 本质 | 主力腿方向 |
|----------|----------|------|------------|
| **A 型：闪涨/闪崩** | 短时间（≤15 分钟）内偏移 ≥ 8% | 短期恐慌/FOMO 过激，超买超卖，动能衰竭 | **逆势（均值回归）** |
| **B 型：宏观趋势** | 长时间（≥ 6-12 小时）持续单边移动 ≥ 8% | 结构性趋势，主力资金持续介入，动能尚存 | **顺势（趋势跟随）** |

**关键洞见**：
- **快速大幅波动**（高速率）→ 价格被过度拉伸，回归均值概率高 → 主力腿反向押注
- **缓慢持续趋势**（低速率）→ 趋势动能尚存，延续概率高 → 主力腿顺势押注
- **对冲腿永远方向相反**，在判断错误时提供安全垫

---

## 二、速率分级算法

### 2.1 速率计算

```
velocity = abs(price_change_pct) / elapsed_time_in_minutes
```

### 2.2 双窗口扫描

每个 tick 同时执行两个独立的扫描窗口：

**快窗口（Flash Window）**
- 回溯：`trh_fast_window_ticks`（默认 180 tick = 15分钟 @ 5s采样）
- 速率门槛：`trh_fast_vel_threshold`（默认 0.50 %/min）
- 幅度门槛：`trh_fast_move_threshold`（默认 7.5%）
- 触发信号：A 型（闪涨/闪崩），主力腿**逆势**

**慢窗口（Trend Window）**
- 回溯：`trh_slow_window_ticks`（默认 8640 tick = 12小时）
- 速率上限：`trh_slow_vel_max`（默认 0.018 %/min，即 12h 移动 13% 以内）
- 幅度门槛：`trh_slow_move_threshold`（默认 8%）
- 额外要求：`ER > trh_er_threshold`（默认 0.45），确认单边性
- 触发信号：B 型（宏观趋势），主力腿**顺势**

### 2.3 信号优先级

若两个窗口同时触发（罕见），优先 A 型（闪涨/闪崩），因为它包含在趋势内部，属于更近期的短期信号。

---

## 三、状态机（含模式分支）

```
IDLE（空闲）
  │
  ├─ [快窗口触发 A型] ─▶  DETECTED_A（闪崩/闪涨识别）
  │                           │  [速率开始衰减，等待入场时机]
  │                           ▼
  │                       DUAL_HOLD_A（A型双腿持仓，极速模式）
  │                           │
  │                           ├── [止盈/止损/超时≤15min]
  │                           └── ──▶ CLOSING_A → IDLE（直接回到空闲，无冷却）
  │                                              ↑ 立刻重新扫描下一个闪崩信号
  │
  └─ [慢窗口触发 B型] ─▶  DETECTED_B（宏观趋势识别）
                              │  [慢速稳定确认：30-60分钟内动能减弱]
                              ▼
                          DUAL_HOLD_B（B型双腿持仓，长线模式）
                              │
                              ├── [整体止盈/止损]  ──▶ CLOSING_B → COOLDOWN（1h）→ IDLE
                              ├── [单腿超额止盈]   ──▶ SOLO_HOLD → CLOSING_B → COOLDOWN → IDLE
                              └── [持仓超时72h]    ──▶ CLOSING_B → COOLDOWN → IDLE
```

**A型与B型核心区别**：A型平仓后**不进入冷却期**，立即回到 IDLE 重新扫描，保持对下一次闪崩事件的持续响应能力。

---

## 四、分阶段检测逻辑

### 阶段一：A型 闪涨/闪崩检测与极速入场

**触发条件（同时满足）**：
1. 快窗口内 `abs(move) >= trh_fast_move_threshold`（7.5%）
2. `velocity >= trh_fast_vel_threshold`（0.50 %/min）
3. Kalman 速度绝对值**刚刚开始衰减**（当前速度 < 前两个 tick 的平均速度），意味着动能正在见顶

**入场时机（A型不做长时间稳定等待，快速入场）**：
- 触发信号后，只需观察 `trh_fast_entry_ticks`（默认 6 tick = 30秒）内 Kalman 速度开始下降
- **30秒内确认速率顶部 → 立即入场**，不等待完整稳定期
- 逻辑：闪崩均值回归的最佳入场窗口非常短暂，等到完全"稳定"反而错过最优入场点

**A型最长持仓：15分钟（`trh_max_hold_min_a = 15`）**
- 无论盈亏，15分钟强制平仓双腿
- 平仓后**直接回到 IDLE**，不进入冷却期，立即重新扫描下一次闪崩
- 依据：闪崩均值回归的市场窗口在15分钟内基本完成，超过15分钟则市场结构已变

### 阶段二：B型 宏观趋势检测

**触发条件（同时满足）**：
1. 慢窗口内 `abs(move) >= trh_slow_move_threshold`（8%）
2. `velocity <= trh_slow_vel_max`（0.018 %/min）
3. 趋势期 `ER > trh_er_threshold`（0.45），确认走势线性单向
4. 价格在回溯窗口内**没有单日反弹超过总移动量的 38.2%**（排除已经反弹的情况）

**稳定确认（B型专用，较慢）**：
- 窗口：`trh_slow_stable_ticks`（默认 360 tick = 30分钟）
- 要求同时满足：
  - ATR < `trh_stability_vol_thr`（0.4%）
  - 区间 `(high-low)/mid < trh_stability_range_pct`（0.8%）
  - ER 已下降至 `< trh_er_stable_max`（0.25），趋势动能暂时耗散
  - Kalman 速度绝对值 `< trh_kalman_vel_stable`（0.0001）

---

## 五、资金不均等分配算法

### 5.1 A型：闪涨/闪崩（主力逆势）

```
A型主力方向 = 与闪涨/闪崩方向相反

if 闪涨（快速拉升）:
    主力做空（押注回落）× major_ratio_A
    对冲做多（安全垫）  × (1 - major_ratio_A)

if 闪崩（快速下砸）:
    主力做多（押注反弹）× major_ratio_A
    对冲做空（安全垫）  × (1 - major_ratio_A)
```

**A型比例分级**（闪崩越快越激进）：

| 15分钟内涨跌幅 | major_ratio_A | 逻辑 |
|----------------|---------------|------|
| 7.5% ～ 10%    | 0.65          | 中等过激，适度押注回归 |
| 10% ～ 15%     | 0.70          | 明显过激，加重逆向 |
| ≥ 15%          | 0.75          | 极端插针，强烈押注回归（上限） |

**A型杠杆**（持仓极短，可承受较高杠杆）：
- 主力腿：5x（极速模式，15分钟内快进快出，用较高杠杆放大回归收益）
- 对冲腿：3x（安全垫，防止判断失误）

**A型止盈/止损（快而紧）**：
- 整体止盈：+4%（15分钟内能拿到4%已是优质信号，不贪）
- 整体止损：-3%（闪崩若继续发展，快速止损，等下一次机会）
- 单腿超额止盈：+8%（单腿涨到8%，果断锁定，剩余腿设跟踪止损后平仓）
- **超时15分钟：强制全平，无论盈亏**

### 5.2 B型：宏观趋势（主力顺势）

```
B型主力方向 = 与宏观趋势相同

if 趋势向下（12小时持续下跌）:
    主力做空（顺势）× major_ratio_B
    对冲做多（防护）× (1 - major_ratio_B)

if 趋势向上（12小时持续上涨）:
    主力做多（顺势）× major_ratio_B
    对冲做空（防护）× (1 - major_ratio_B)
```

**B型比例分级**（趋势越强但仍保守，因趋势随时可能反转）：

| 12小时涨跌幅 | major_ratio_B | 逻辑 |
|--------------|---------------|------|
| 8% ～ 12%    | 0.60          | 中等趋势，保守跟进 |
| 12% ～ 18%   | 0.63          | 强趋势，适度加重 |
| ≥ 18%        | 0.65          | 极强趋势（上限，避免追高追低） |

**B型杠杆**：
- 主力腿：3x（顺势稍高杠杆，加速收益）
- 对冲腿：2x（逆势对冲需低杠杆，否则消耗过快）

---

## 六、退出机制（多维止盈止损）

### 6.1 A型退出机制（极速，≤15分钟）

A型持仓的核心原则：**时间是最大风险，超时即出，不恋战**。

| 退出条件 | 阈值 | 优先级 | 动作 |
|----------|------|--------|------|
| 整体止盈 | +4% | 2 | 全平 → 直接回 IDLE |
| 单腿超额止盈 | 主力腿 +8% | 1（最优先） | 平盈利腿，剩余腿立即跟踪止损（0.5%回撤触发）后全平 → IDLE |
| 整体止损 | -3% | 2 | 全平 → 直接回 IDLE |
| **超时 15 分钟** | 900秒 | **最终兜底** | **无论盈亏，强制全平 → IDLE** |

> **A型不进入 SOLO_HOLD**：由于持仓窗口极短，单腿止盈后不保留剩余腿，直接全部平仓锁定结果，立即重新扫描。

**A型平仓后行为**：
```
closeBothA() → state = trhIdle（无冷却，直接继续扫描快窗口）
```

### 6.2 B型退出机制（长线，≤72小时）

| 退出条件 | 阈值 | 动作 |
|----------|------|------|
| 整体止盈 | +10% | 全平 → COOLDOWN（1h）→ IDLE |
| 整体止损 | -7% | 全平 → COOLDOWN（1h）→ IDLE |
| 单腿超额止盈 | 主力腿 +15% | 平盈利腿，利润20%追加剩余腿，进入 SOLO_HOLD |
| 单腿独立止损 | 主力腿 -5% / 对冲腿 -7% | 平该腿 → SOLO_HOLD |
| 超时 72 小时 | 259200秒 | 全平 → COOLDOWN（1h）→ IDLE |

**B型 SOLO_HOLD 管理**：
- 剩余腿启用追踪止损（`trh_solo_trailing_stop = 2.5%`）
- 剩余腿设独立止盈：原始止盈目标的剩余空间

---

## 七、数据结构设计

### 7.1 新枚举和结构体（`strategy_trh.go`）

```go
type trhState int8

const (
    trhIdle         trhState = 0
    trhDetectedA    trhState = 1  // 检测到 A型（闪涨/闪崩）
    trhDetectedB    trhState = 2  // 检测到 B型（宏观趋势）
    trhDualHold     trhState = 3  // 双腿持仓中
    trhSoloHold     trhState = 4  // 一腿已平，剩余腿独立
    trhCooldown     trhState = 5  // 冷却期
)

type trhMode int8

const (
    trhModeNone         trhMode = 0
    trhModeFlashRevert  trhMode = 1  // A型：逆势主力（均值回归）
    trhModeTrendFollow  trhMode = 2  // B型：顺势主力（趋势跟随）
)

type dualPortfolio struct {
    state         trhState
    mode          trhMode   // 当前信号模式

    // 趋势元数据
    trendDir      posDir    // 检测到的原始价格运动方向
    trendMovePct  float64   // 检测到的价格变化幅度（绝对值）
    velocity      float64   // 检测到的平均速率（%/min）
    majorRatio    float64   // 实际使用的主力腿比例

    // 主力腿：A型=逆势，B型=顺势
    majorLeg      portfolio
    majorCapital  float64
    majorDir      posDir    // 主力腿实际方向（与 trendDir 在A型相反，B型相同）

    // 对冲腿：永远与主力腿方向相反
    hedgeLeg      portfolio
    hedgeCapital  float64

    // 时间管理
    entryTime     time.Time
    cooldownUntil time.Time

    // 稳定性检测滚动状态
    stabilityCount int      // 已连续满足稳定条件的 tick 数
    stabilityHigh  float64
    stabilityLow   float64
    peakVelocity   float64  // A型用：检测到的峰值速率，用于判断速率衰减
}
```

### 7.2 `Strategy` 结构体新增字段

```go
trhDual       dualPortfolio  // TRH 专用双腿持仓状态
trhFastBuf    *RingBuffer    // 快窗口价格缓冲
trhSlowBuf    *RingBuffer    // 慢窗口价格缓冲
trhStableBuf  *RingBuffer    // 稳定性检测缓冲
```

---

## 八、新增配置参数（Config 结构体末尾追加）

```go
// 趋势反转对冲策略（strategy_type = "trend_reversion_hedge"）
// ----- A型（闪涨/闪崩）检测参数 -----
TRHFastWindowTicks    int     `json:"trh_fast_window_ticks"`    // 快窗口长度（15min→180 ticks @ 5s）
TRHFastMoveThreshold  float64 `json:"trh_fast_move_threshold"`  // 快窗口触发最小幅度（7.5%）
TRHFastVelThreshold   float64 `json:"trh_fast_vel_threshold"`   // 触发最小速率（0.50 %/min）
TRHFastEntryTicks     int     `json:"trh_fast_entry_ticks"`     // 入场等待 tick 数（6 ticks = 30s）
TRHLegExcessProfitA   float64 `json:"trh_leg_excess_profit_a"`  // A型单腿超额止盈（8%）
TRHTotalTakeProfitA   float64 `json:"trh_total_take_profit_a"`  // A型整体止盈（4%）
TRHTotalStopLossA     float64 `json:"trh_total_stop_loss_a"`    // A型整体止损（3%）
TRHMaxHoldSecA        int     `json:"trh_max_hold_sec_a"`       // A型最长持仓秒数（900 = 15min）
TRHMajorLeverageA     float64 `json:"trh_major_leverage_a"`     // A型主力腿杠杆（5x）
TRHHedgeLeverageA     float64 `json:"trh_hedge_leverage_a"`     // A型对冲腿杠杆（3x）

// ----- B型（宏观趋势）检测参数 -----
TRHSlowWindowTicks    int     `json:"trh_slow_window_ticks"`    // 慢窗口长度（12h→8640 ticks）
TRHSlowMoveThreshold  float64 `json:"trh_slow_move_threshold"`  // 慢窗口触发最小幅度
TRHSlowVelMax         float64 `json:"trh_slow_vel_max"`         // 触发最大速率上限（%/min）
TRHERThreshold        float64 `json:"trh_er_threshold"`         // 趋势期 ER 门槛
TRHSlowStableTicks    int     `json:"trh_slow_stable_ticks"`    // B型稳定确认所需 tick 数（30min）
TRHStabilityVolThr    float64 `json:"trh_stability_vol_thr"`    // 稳定期最大波动率
TRHStabilityRangePct  float64 `json:"trh_stability_range_pct"` // 稳定期最大价格区间比
TRHERStableMax        float64 `json:"trh_er_stable_max"`        // 稳定期 ER 上限（动能耗散）
TRHKalmanVelStable    float64 `json:"trh_kalman_vel_stable"`    // 稳定期 Kalman 速度门槛
TRHLegExcessProfitB   float64 `json:"trh_leg_excess_profit_b"`  // B型单腿超额止盈
TRHMajorStopLossB     float64 `json:"trh_major_stop_loss_b"`    // B型主力腿止损
TRHMaxHoldHoursB      int     `json:"trh_max_hold_hours_b"`     // B型最长持仓时间
TRHMajorLeverageB     float64 `json:"trh_major_leverage_b"`     // B型主力腿杠杆
TRHHedgeLeverageB     float64 `json:"trh_hedge_leverage_b"`     // B型对冲腿杠杆

// ----- B型公共参数 -----
TRHTotalTakeProfitB    float64 `json:"trh_total_take_profit_b"`   // B型整体止盈（10%）
TRHTotalStopLossB      float64 `json:"trh_total_stop_loss_b"`     // B型整体止损（7%）
TRHHedgeStopLoss       float64 `json:"trh_hedge_stop_loss"`       // 对冲腿止损（B型通用，7%）
TRHSoloTrailingStop    float64 `json:"trh_solo_trailing_stop"`    // B型 SOLO_HOLD 追踪止损（2.5%）
TRHProfitReinvestRatio float64 `json:"trh_profit_reinvest_ratio"` // B型单腿止盈后利润追加比例（20%）
TRHCooldownSecB        int     `json:"trh_cooldown_sec_b"`        // B型平仓后冷却秒数（3600=1h）
// A型平仓后无冷却，直接回 IDLE
```

---

## 九、config.json 新策略入口（双条目：A型+B型）

```json
{
    "_comment": "【A型-闪崩极速回归】15分钟内急涨急跌后逆势对冲，最长持仓15分钟，快进快出，无冷却。",
    "name": "闪崩回归",
    "strategy_type": "trend_reversion_hedge",
    "initial_capital": 1000.0,
    "leverage": 1.0,
    "trade_fee": 0.0005,
    "stop_loss": 0.0,
    "take_profit": 0.0,
    "cooldown_sec": 0,
    "trh_fast_window_ticks": 180,
    "trh_fast_move_threshold": 0.075,
    "trh_fast_vel_threshold": 0.50,
    "trh_fast_entry_ticks": 6,
    "trh_slow_window_ticks": 0,
    "trh_slow_move_threshold": 0.0,
    "trh_slow_vel_max": 0.0,
    "trh_er_threshold": 0.0,
    "trh_major_leverage_a": 5.0,
    "trh_hedge_leverage_a": 3.0,
    "trh_total_take_profit_a": 0.04,
    "trh_total_stop_loss_a": 0.03,
    "trh_leg_excess_profit_a": 0.08,
    "trh_max_hold_sec_a": 900,
    "kalman_q_pos": 0.0005,
    "kalman_q_vel": 0.00015,
    "kalman_r": 0.00003,
    "kalman_vel_thresh": 0.0
},
{
    "_comment": "【B型-宏观趋势跟随】12小时级别持续单边趋势后顺势对冲，主力顺向跟进，对冲腿防护。",
    "name": "趋势跟随对冲",
    "strategy_type": "trend_reversion_hedge",
    "initial_capital": 1000.0,
    "leverage": 1.0,
    "trade_fee": 0.0005,
    "stop_loss": 0.0,
    "take_profit": 0.0,
    "cooldown_sec": 0,
    "trh_fast_window_ticks": 0,
    "trh_fast_move_threshold": 0.0,
    "trh_fast_vel_threshold": 0.0,
    "trh_slow_window_ticks": 8640,
    "trh_slow_move_threshold": 0.08,
    "trh_slow_vel_max": 0.018,
    "trh_er_threshold": 0.45,
    "trh_slow_stable_ticks": 360,
    "trh_stability_vol_thr": 0.004,
    "trh_stability_range_pct": 0.008,
    "trh_er_stable_max": 0.25,
    "trh_kalman_vel_stable": 0.0001,
    "trh_major_leverage_b": 3.0,
    "trh_hedge_leverage_b": 2.0,
    "trh_total_take_profit_b": 0.10,
    "trh_total_stop_loss_b": 0.07,
    "trh_leg_excess_profit_b": 0.15,
    "trh_major_stop_loss_b": 0.05,
    "trh_max_hold_hours_b": 72,
    "trh_hedge_stop_loss": 0.07,
    "trh_solo_trailing_stop": 0.025,
    "trh_profit_reinvest_ratio": 0.20,
    "trh_cooldown_sec": 3600,
    "kalman_q_pos": 0.0002,
    "kalman_q_vel": 0.00005,
    "kalman_r": 0.00030,
    "kalman_vel_thresh": 0.0
}
```

---

## 十、需要新增/修改的文件

| 文件 | 类型 | 改动说明 |
|------|------|----------|
| `strategy_trh.go` | **新建** | 全部 TRH 策略逻辑，含 A/B 两型检测、双腿管理、退出控制 |
| `config.go` | **修改** | `Config` 结构体末尾追加 TRH 专用字段 |
| `strategy.go` | **修改** | `onPrice()` switch 追加 case；`newStrategy()` 初始化 TRH Ring Buffer |
| `config.json` | **修改** | `strategies` 追加两条 TRH 配置入口（A型+B型各一条） |

> `portfolio.go` 无需修改，`dualPortfolio` 内部复用现有的 `portfolio` 实例和方法。

---

## 十一、strategy_trh.go 核心函数结构

```
onPriceTRH(tick, endTime)
  │
  ├── feedTRHBuffers(tick)              // 更新快/慢/稳定窗口 RingBuffer
  │
  └── switch trhDual.state
        │
        ├── trhIdle
        │     ├── tryDetectA(tick)     // 快窗口：速率≥0.5%/min + 幅度≥7.5%
        │     │     └── [触发] → state=trhDetectedA，记录 peakVelocity
        │     └── tryDetectB(tick)     // 慢窗口：幅度≥8% + 速率≤0.018 + ER>0.45
        │           └── [触发] → state=trhDetectedB
        │
        ├── trhDetectedA
        │     └── checkEntryA(tick)   // 等待30秒（6 ticks）内速率开始下降
        │           └── [kalmanVel < 前tick vel] → enterDual(trhModeFlashRevert)
        │                                          → state=trhDualHold
        │
        ├── trhDetectedB
        │     └── checkStabilityB(tick) // ATR + 区间 + ER + Kalman 持续30min
        │           └── [全部满足] → enterDual(trhModeTrendFollow)
        │                           → state=trhDualHold
        │
        ├── trhDualHold（A型分支）
        │     └── manageDualHoldA(tick)
        │           ├── 检查 time.Since(entryTime) >= 900s → closeBothA() → IDLE（无冷却）
        │           ├── calcTotalEquity() → 检查 +4% / -3% → closeBothA() → IDLE
        │           └── 检查主力腿 +8% → closeBothA()（直接全平）→ IDLE
        │
        ├── trhDualHold（B型分支）
        │     └── manageDualHoldB(tick)
        │           ├── 检查 time.Since(entryTime) >= 259200s → closeBothB() → COOLDOWN
        │           ├── calcTotalEquity() → 检查 +10% / -7% → closeBothB() → COOLDOWN
        │           ├── 检查主力腿 +15% → closeLeg(major) → SOLO_HOLD
        │           └── 检查单腿独立止损 → closeLeg() → SOLO_HOLD
        │
        ├── trhSoloHold（仅 B型进入）
        │     └── manageSoloHold(tick)
        │           ├── 剩余腿 portfolio.checkStops()（含追踪止损 2.5%）
        │           └── [触发 or 超时] → closeBothB() → COOLDOWN
        │
        └── trhCooldown（仅 B型进入）
              └── time.Now().After(cooldownUntil) → state=trhIdle


enterDual(tick, mode)
  ├── majorDir = trendDir 的反向（A型）或 trendDir（B型）
  ├── majorRatio = 幅度分级表查值
  ├── majorCapital = initial_capital * majorRatio
  ├── hedgeCapital = initial_capital * (1 - majorRatio)
  ├── majorLeg.openPosVolAdjusted(cfg, majorDir, ...)
  └── hedgeLeg.openPosVolAdjusted(cfg, -majorDir, ...)


closeBothA(tick, reason)           // A型专用：无冷却，直接回 IDLE
  ├── majorLeg.closePos()（若持仓）
  ├── hedgeLeg.closePos()（若持仓）
  ├── printTRHReport(mode=A)
  └── state = trhIdle

closeBothB(tick, reason)           // B型专用：设冷却期
  ├── majorLeg.closePos()（若持仓）
  ├── hedgeLeg.closePos()（若持仓）
  ├── printTRHReport(mode=B)
  ├── cooldownUntil = time.Now().Add(TRHCooldownSecB)
  └── state = trhCooldown
```

---

## 十二、两型策略对比总览

| 维度 | A型（闪崩极速回归） | B型（趋势跟随对冲） |
|------|---------------------|---------------------|
| 信号窗口 | 15分钟 | 12小时 |
| 价格变化 | ≥ 7.5% | ≥ 8% |
| 速率特征 | ≥ 0.50 %/min（快） | ≤ 0.018 %/min（慢） |
| ER 要求 | 无（速率隐含单边性） | > 0.45（必须线性） |
| 入场等待 | 30秒（速率开始衰减即入） | 30分钟（多维稳定确认） |
| 主力腿方向 | **逆势**（均值回归） | **顺势**（延续押注） |
| 主力腿比例 | 65%～75% | 60%～65% |
| 主力腿杠杆 | **5x**（快进快出） | 3x |
| 对冲腿杠杆 | 3x | 2x |
| 整体止盈目标 | **+4%** | +10% |
| 整体止损上限 | **-3%** | -7% |
| 单腿超额止盈 | **+8%** → 全平 | +15% → SOLO_HOLD |
| **最长持仓时间** | **15分钟（硬性强制）** | 72小时 |
| 超时行为 | 无论盈亏全平 | 全平 |
| **平仓后行为** | **直接回 IDLE（无冷却）** | COOLDOWN 1小时 |
| 策略节奏 | 高频，可反复触发 | 低频，每次耗时数天 |

---

## 十三、风险项与对策

| 风险 | 场景 | 对策 |
|------|------|------|
| **A型误入趋势** | 闪崩是大趋势的一部分而非过激反应，主力逆势被持续打穿 | 主力腿止损 4.5%（A型较紧），总止损 6% 快速出局 |
| **B型趋势反转** | 顺势建仓后市场急转反向 | 对冲腿先盈利，总止损 7% 兜底；超时72h强平 |
| **双腿同向亏损** | 判断失误，两腿净敞口集中在同一方向 | 主力/对冲方向严格相反的系统约束；整体止损为最后防线 |
| **A型假衰减** | 速率短暂下降后继续闪崩，-3%止损触发 | 整体止损快速出局；15min超时兜底；可回测调整止损阈值 |
| **A型错过反弹** | -3%止损后市场立即反弹 | 无冷却设计确保下一次闪崩信号可立即入场，弥补损失 |
| **B型假稳定** | 短暂横盘后趋势继续 | 多维指标（ATR+ER+Kalman）联合验证30分钟，避免过早入场 |
| **流动性成本** | 两腿×开平共4笔手续费侵蚀 | 低杠杆（2-3x）控制仓位；止盈目标（8-10%）远大于手续费（约0.2%） |
| **资金长期锁定** | 行情无方向，两腿盈亏相抵，净收益停滞 | A型24h、B型72h超时强平，冷却后重新等待信号 |

---

## 十四、实现顺序

1. **`config.go`**：追加 TRH 字段到 `Config` 结构体（建议放最末尾注释块）
2. **`strategy_trh.go`**：
   - 定义 `trhState`、`trhMode`、`dualPortfolio`
   - 实现 `onPriceTRH()`、`feedTRHBuffers()`
   - 实现 A型：`tryDetectA()`、`checkStabilityA()`
   - 实现 B型：`tryDetectB()`、`checkStabilityB()`
   - 实现公共：`enterDual()`、`manageDualHold()`、`manageSoloHold()`、`closeBoth()`
   - 实现 `printTRHReport()`（含模式、两腿分别盈亏）
3. **`strategy.go`**：
   - `onPrice()` switch 中追加 `"trend_reversion_hedge"` case
   - `newStrategy()` 中初始化 `trhFastBuf`、`trhSlowBuf`、`trhStableBuf`（根据配置参数决定大小）
4. **`config.json`**：追加 A型"闪崩回归"和 B型"趋势跟随对冲"两条配置
5. **验证场景**：
   - A型测试：找 BTC 15分钟内插针 10% 的历史片段（如 2024-08 闪崩），追踪检测→入场→回归→止盈流程
   - B型测试：找 BTC 12小时连续下跌 12% 后横盘的历史片段，追踪趋势识别→稳定等待→顺势双腿流程

---

---

## 十五、对照策略5（瀑布连环）的调整项

> 对照 `strategy_waterfall.go` 与现有代码架构逐条审查，TRH 计划需做以下修正：

### 15.1 A型与瀑布策略的时序关系（逻辑冲突修正）

**问题**：瀑布策略检测的是**瀑布下跌的起点**（连续 5 个 tick 均下跌 + 速率 ≥ 0.025%/tick，约 25 秒），
此时做空顺势；TRH A型检测的是**同一段下跌的结果**（15分钟内下跌 ≥ 7.5%），此时做多反转。
两个策略在时间上是**先后关系**，而非冲突：

```
瀑布触发（t=0, 连续5跌）→ 瀑布持仓追空（持3分钟）→ 瀑布平仓
     ↓
（若下跌已累积 7.5%+）TRH A型触发 → 双腿建仓（逆势主力做多）→ 持最多15分钟
```

**修正**：TRH A型入场增加前置条件——**当前快窗口内的最近 N 个 tick 不能全部下跌**（即瀑布尚未平息时不入场）。
具体条件：`连续下跌 tick 数 < trh_fast_entry_ticks`（6 tick），确保瀑布动能已经停止。
改写入场逻辑：
```
触发 A型信号后，等待：
  1. 最近 3 个 tick 至少有 1 个 tick 不再下跌（有企稳迹象）
  2. Kalman 速度绝对值较峰值下降
→ 满足两者，立即入场（不再等 30 秒固定时间）
```

### 15.2 A型入场确认逻辑简化（参照瀑布的简洁实现）

**问题**：瀑布入场逻辑极简：`allDown && avgTickDropPct >= WFMinVelPct && kVelOK`，一行判断。
TRH A型当前计划的"30秒速率衰减"描述过于模糊，不利于实现。

**修正（对齐瀑布写法风格）**：
```go
// A型入场确认（替换"30秒速率衰减"描述）
recentUp := 0
for i := 0; i < 3; i++ {  // 检查最近3个tick
    if trhFastBuf.Get(i) >= trhFastBuf.Get(i+1) {
        recentUp++
    }
}
entryOK := recentUp >= 1  // 至少1个tick不再下跌（企稳信号）
kVelDecay := abs(kVelPct) < abs(prevKVelPct)  // 速率绝对值在收缩
// → entryOK && kVelDecay → 立即入场
```
将 `trh_fast_entry_ticks` 字段重新定义为"入场确认所需的企稳 tick 数"（默认 1）。

### 15.3 双腿 Leverage 传递问题（实现层关键问题）

**问题**：瀑布调用 `s.p.openPosVolAdjusted(s.cfg, dirShort, tick, 0.015, &s.trades)`，
杠杆来自 `s.cfg.Leverage`。TRH 的主力腿和对冲腿需要**不同杠杆**（A型：5x vs 3x），
但 `openPosVolAdjusted` 只接受一个 cfg。

**修正**：在 `enterDual()` 中为每条腿创建临时 cfg 副本，仅覆盖 Leverage 字段：
```go
func (s *Strategy) enterDual(tick Tick, mode trhMode) {
    majorCfg := s.cfg
    hedgeCfg := s.cfg
    if mode == trhModeFlashRevert {
        majorCfg.Leverage = s.cfg.TRHMajorLeverageA
        hedgeCfg.Leverage = s.cfg.TRHHedgeLeverageA
    } else {
        majorCfg.Leverage = s.cfg.TRHMajorLeverageB
        hedgeCfg.Leverage = s.cfg.TRHHedgeLeverageB
    }
    s.trhDual.majorLeg.cash = majorCapital
    s.trhDual.majorLeg.openPosVolAdjusted(majorCfg, majorDir, tick, volPct, &s.trades)
    s.trhDual.hedgeLeg.cash = hedgeCapital
    s.trhDual.hedgeLeg.openPosVolAdjusted(hedgeCfg, -majorDir, tick, volPct, &s.trades)
}
```

### 15.4 forceLiquidate 需要 TRH 专用分支

**问题**：`strategy.go` 中 `forceLiquidate` 只平仓 `s.p`，TRH 的双腿存在 `s.trhDual` 中，
到期或中断退出时两腿不会被清算，造成资金丢失。

**修正**：在 `strategy.go` 的 `forceLiquidate` 中增加 TRH 分支：
```go
func (s *Strategy) forceLiquidate(tick Tick, reason string) {
    if s.cfg.Disabled { return }
    if s.cfg.StrategyType == "trend_reversion_hedge" {
        if s.trhDual.majorLeg.inPosition() {
            s.trhDual.majorLeg.closePos(s.cfg, tick, reason, &s.trades)
        }
        if s.trhDual.hedgeLeg.inPosition() {
            s.trhDual.hedgeLeg.closePos(s.cfg, tick, reason, &s.trades)
        }
        return
    }
    if s.p.inPosition() {
        s.p.closePos(s.cfg, tick, reason, &s.trades)
    }
}
```

### 15.5 printReport / totalEquity 需要 TRH 专用展示

**问题**：`printAllReports` 调用 `s.p.totalEquity()`，TRH 不使用 `s.p`（cash=0），
导致 TRH 权益显示为 $0，汇总报告错误。

**修正**：
- TRH 的 `Strategy.p.cash` 初始化为 0（不使用）
- 在 `Strategy.printReport()` 中增加 TRH 专用分支，合并两腿权益：
```go
if s.cfg.StrategyType == "trend_reversion_hedge" {
    majorEq := s.trhDual.majorLeg.totalEquity(majorCfg, tick)
    hedgeEq  := s.trhDual.hedgeLeg.totalEquity(hedgeCfg, tick)
    totalEq  := majorEq + hedgeEq
    // ... 打印双腿分别盈亏 + 合并总盈亏
    return
}
```
- `printAllReports` 的汇总表中，TRH 行显示双腿合并权益

### 15.6 newStrategy 初始化：warmupNeed 的计算

**问题**：瀑布的 warmupNeed 是 `WFConsecutiveTicks + 10`（~15）。
B型 TRH 慢窗口需要 8640 ticks（12小时），预热时间极长。

**确认**：当前 `lookback_hours: 30` 提供 30h 历史 = 30×720 = 21600 ticks，足够覆盖 B型 warmup。
但 `newStrategy()` 的 `warmupNeed` 逻辑需要明确：
```go
case "trend_reversion_hedge":
    warmup = cfg.TRHFastWindowTicks  // A型实例
    if cfg.TRHSlowWindowTicks > warmup {
        warmup = cfg.TRHSlowWindowTicks  // B型实例
    }
    if warmup == 0 { warmup = 10 }
```
Ring Buffer 大小：`trhFastBuf = NewRingBuffer(cfg.TRHFastWindowTicks + 10)`，
`trhSlowBuf = NewRingBuffer(cfg.TRHSlowWindowTicks + 10)`，独立分配。

### 15.7 A型与死猫做空（策略4）的信号协调

对照 `strategy_dcb.go`：死猫做空检测"急跌→弱反弹→做空"，TRH A型检测"急跌→做多（反转）"。

- DCB 是 **急跌 + 弱反弹失败** → 做空（续跌）
- TRH A型 是 **急跌刚结束** → 做多（反转）

两者是同一事件的**不同时机切入**：
- 若急跌后出现小反弹 → DCB 会先标记为"弱反弹"，等反弹失败再做空
- TRH A型 在急跌刚发生时就已入场做多

不冲突（各自独立运行），但需知道这两个策略在同一市场结构下可能方向相反。
无需额外修改，属于策略矩阵内的自然对冲。

---

### 汇总：相比 v3 新增的修正项

| 编号 | 问题 | 修正位置 |
|------|------|----------|
| 15.1 | A型需等瀑布平息（有企稳 tick）才能入场 | `strategy_trh.go` → `tryDetectA()` 入场前置条件 |
| 15.2 | 入场确认逻辑改为"最近3 tick 有1个企稳" | `strategy_trh.go` → 替换 `checkEntryA()` |
| 15.3 | 双腿不同杠杆通过临时 cfg 副本传递 | `strategy_trh.go` → `enterDual()` |
| 15.4 | `forceLiquidate` 增加 TRH 双腿清算分支 | `strategy.go` → `forceLiquidate()` |
| 15.5 | `printReport` / `totalEquity` 增加 TRH 双腿合并展示 | `strategy.go` / `strategy_trh.go` |
| 15.6 | `newStrategy()` 中 TRH warmupNeed 与 RingBuffer 分配逻辑 | `strategy.go` → `newStrategy()` |
| 15.7 | A型/DCB 方向冲突分析（确认不需额外改动） | 文档说明，无代码改动 |

---

---

## 十六、Kalman 滞后性分析与 A型无滞后入场方案

### 16.1 Kalman 滞后性的数学根源

对照 `indicators.go` 的 `kalmanFilter.step()` 实现：

```
速度更新增益 K1 = P10p / (P00p + R)
位置更新增益 K0 = P00p / (P00p + R)

在稳态时，K1 ≈ K0² / 2  →  K1 << K0
```

这意味着：
- **位置估计** 响应观测的速度受 R 控制（R 小 → 快响应）
- **速度估计** 响应比位置估计再慢约 1/K0 倍 —— 这是 Kalman 速度的固有滞后

| 策略 | kalman_r | 速度滞后约 |
|------|----------|-----------|
| 高频剥头皮 | 0.00001 | ~1-2 tick（5-10秒）|
| 瀑布连环 | 0.00002 | ~2-3 tick（10-15秒）|
| K滤波趋势 | 0.00050 | ~15-20 tick（75-100秒）|

**结论**：即使 kalman_r 极小（0.00001），速度估计仍有 1-2 tick 的固有滞后。
在闪崩场景中，价格在 15 分钟内剧烈运动后开始反转，**Kalman 速度在价格真实触底时仍指向下跌方向**，
等 Kalman 速度"转正"时，最佳入场点已经过去 10-25 秒。

### 16.2 各阶段对 Kalman 的合理使用边界

```
A型信号检测（15min回溯）：  ← 直接用 RingBuffer 原始价格，零滞后，无需 Kalman
A型入场确认（价格企稳）：   ← 直接对比相邻 tick 原始价格，零滞后
                                ↑ 核心入场阶段：完全不用 Kalman
───────────────────────────────────────────────────────────
A型持仓中止盈止损检查：     ← 用 tick.Bid1 / tick.Ask1 原始盘口价，portfolio.checkStops() 已是零滞后
A型超时强平（900秒计时）：  ← time.Since()，零滞后

B型信号检测（12h回溯）：    ← 直接用 RingBuffer 原始价格计算 ER、ATR，零滞后
B型稳定确认（30min）：      ← ATR + 区间计算，零滞后
                                ↑ 只在以下场景保留 Kalman：
B型趋势动能辅助判断：       ← Kalman 速度作为辅助过滤器（非主逻辑）
B型持仓中位置管理：         ← Kalman 平滑后的位置估计，滞后可接受（B型以天为单位）
```

### 16.3 A型无滞后入场方案（替代 Kalman 速度判断）

**完全使用原始价格的三层零滞后逻辑**：

**第一层：信号检测（原有，只用 RingBuffer）**
```go
// 使用 trhFastBuf（180 tick 环形缓冲）直接计算原始价格变化
oldest := trhFastBuf.Get(s.cfg.TRHFastWindowTicks - 1)  // 15分钟前的价格
newest := trhFastBuf.Get(0)                               // 当前价格
rawMove := (newest - oldest) / oldest                      // 原始涨跌幅

// 原始速率 = 原始涨跌幅 / 时间（分钟），零滞后
velocity := math.Abs(rawMove) / float64(s.cfg.TRHFastWindowTicks) * 12.0  // 12 = 60s/5s_interval

triggered := math.Abs(rawMove) >= s.cfg.TRHFastMoveThreshold && velocity >= s.cfg.TRHFastVelThreshold
// 触发方向记录：rawMove < 0 → trendDir = dirShort（下跌），反之为 dirLong
```

**第二层：入场确认（对比相邻 tick 原始价格，零滞后）**
```go
// 对闪崩（trendDir = dirShort）：等待价格第一次往上走
// 对闪涨（trendDir = dirLong）：等待价格第一次往下走
firstReversal := false
if trendDir == dirShort {
    // 闪崩后：当前 tick 价格 > 前一个 tick 价格 = 底部反弹第一根阳线
    firstReversal = trhFastBuf.Get(0) > trhFastBuf.Get(1)
} else {
    // 闪涨后：当前 tick 价格 < 前一个 tick 价格 = 顶部回落第一根阴线
    firstReversal = trhFastBuf.Get(0) < trhFastBuf.Get(1)
}

// 同时检查：当前不在连续下跌中（瀑布未平息保护，参照策略5）
consecutiveDown := 0
for i := 0; i < 3; i++ {
    if trhFastBuf.Get(i) < trhFastBuf.Get(i+1) {
        consecutiveDown++
    }
}
waterfallEnded := consecutiveDown < 3  // 最近3个tick并非全都在跌

// 入场条件 = 第一个反转tick + 瀑布已平息
if firstReversal && waterfallEnded {
    enterDual(tick, trhModeFlashRevert)
}
```

**第三层：持仓中管理（portfolio 内置盘口价格，零滞后）**
```go
// portfolio.checkStops() 已使用 tick.Bid1 / tick.Ask1，无滞后
// A型超时：time.Since(entryTime).Seconds() >= float64(s.cfg.TRHMaxHoldSecA)
// 以上均不需要 Kalman
```

### 16.4 B型保留 Kalman（滞后可接受）

B型以小时/天为单位运作，Kalman 的 10-25 秒滞后完全可以忽略。Kalman 在 B型中的作用：

- **稳定确认辅助**：`abs(kVelPct) < TRHKalmanVelStable` 作为趋势动能耗散的辅助信号
  （与 ATR、ER 等多维指标共同判断，非唯一决策依据）
- **B型 kalman_r 建议**：保持 0.00030（慢速过滤，减少宏观趋势检测中的噪音）

### 16.5 对配置和代码结构的影响

**config.json 调整**（A型）：
```json
"kalman_q_pos": 0.0,
"kalman_q_vel": 0.0,
"kalman_r": 0.0,          ← A型实例完全禁用 Kalman（KalmanR=0 时现有代码跳过 Kalman 计算）
"kalman_vel_thresh": 0.0
```

**`tryDetectA()` 和 `checkEntryA()` 函数不调用 `s.kf.step()`**：
- 完全依赖 `trhFastBuf` 的原始价格数据
- 移除 `trh_fast_entry_ticks` 配置项（已无意义）
- 入场判断由"第一个反转 tick + 3 tick 内有企稳"替代

**更新汇总：修正项 15.2 和 15.1 的代码描述**

原 15.2 方案使用 `kVelDecay`（Kalman 速度衰减），现替换为：
```go
firstReversal && waterfallEnded  // 原始价格比较，零滞后
```

---

### 最终 A型入场信号路径（无 Kalman）

```
每个 tick 进入 onPriceTRH()
  │
  ├── feedTRHBuffers(tick.Prc)   // 只更新 trhFastBuf（原始价格环形缓冲）
  │
  └── state = trhIdle?
        │
        ├── tryDetectA():
        │     rawMove = (buf[0] - buf[179]) / buf[179]   // 原始15min涨跌幅
        │     velocity = abs(rawMove) / 15.0              // 原始速率（%/min）
        │     if abs(rawMove) >= 7.5% && velocity >= 0.5%/min:
        │         trendDir = sign(rawMove), state = trhDetectedA
        │
        └── state = trhDetectedA?
              │
              └── checkEntryA():
                    firstReversal = buf[0] vs buf[1]（一次比较，零滞后）
                    waterfallEnded = not all_3_ticks_down（三次比较，零滞后）
                    if firstReversal && waterfallEnded:
                        enterDual(trhModeFlashRevert)  → 立即入场
```

*计划 v5 完成：A型全面移除 Kalman，改用原始价格零滞后检测，B型保留 Kalman 辅助，可进入编码阶段。*
