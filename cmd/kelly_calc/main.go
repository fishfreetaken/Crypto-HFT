package main

import (
	"flag"
	"fmt"
	"os"
)

// CalculateKelly calculates the optimal fraction of bankroll to wager (risk).
// winRate: Probability of a winning trade (0.0 to 1.0)
// riskRewardRatio: Average profit divided by average loss (e.g., win $200 / lose $100 = 2.0)
func CalculateKelly(winRate, riskRewardRatio float64) float64 {
	if riskRewardRatio <= 0 {
		return 0 // Avoid division by zero or negative ratio
	}
	lossRate := 1.0 - winRate
	kellyPct := winRate - (lossRate / riskRewardRatio)

	if kellyPct < 0 {
		return 0 // The strategy has negative expected value
	}
	return kellyPct
}

func main() {
	winRatePtr := flag.Float64("win", 0.55, "Win rate (0.0 to 1.0), e.g., 0.55 for 55%")
	rrRatioPtr := flag.Float64("rr", 2.0, "Risk/Reward ratio (Avg Profit / Avg Loss), e.g., 2.0")
	capitalPtr := flag.Float64("cap", 10000.0, "Total account capital (default: 10000)")
	fractionPtr := flag.Float64("frac", 0.5, "Kelly Fraction (e.g., 0.5 for Half-Kelly, 1.0 for Full-Kelly)")
	stopLossPtr := flag.Float64("sl", 0.05, "Optional: Stop loss percentage (0.0 to 1.0, e.g., 0.05 for 5%). Used to calculate Total Position Size.")

	flag.Parse()

	if *winRatePtr < 0 || *winRatePtr > 1 {
		fmt.Println("Error: Win rate must be between 0.0 and 1.0")
		os.Exit(1)
	}

	// 1. Calculate Kelly Percentage
	fullKelly := CalculateKelly(*winRatePtr, *rrRatioPtr)

	// 2. Adjust with Kelly Fraction (Half-Kelly is strongly recommended in trading)
	adjKelly := fullKelly * *fractionPtr

	// 3. Amount of capital to actually RISK
	amountToRisk := *capitalPtr * adjKelly

	fmt.Println("==================================================")
	fmt.Printf("💵 Kelly Criterion Position Sizing Calculator\n")
	fmt.Println("==================================================")
	fmt.Printf("Win Rate (p)          : %.2f%%\n", *winRatePtr*100)
	fmt.Printf("Risk/Reward Ratio (b) : %.2f : 1\n", *rrRatioPtr)
	fmt.Printf("Total Capital         : $%.2f\n", *capitalPtr)
	fmt.Printf("Kelly Fraction        : %.2f (1.0 = Full, 0.5 = Half)\n", *fractionPtr)
	fmt.Println("--------------------------------------------------")

	if fullKelly <= 0 {
		fmt.Println("⚠️  WARNING: Mathematical expectancy is negative or zero.")
		fmt.Println("   Do NOT trade this system. The Kelly percentage is 0%.")
	} else {
		fmt.Printf("Full Kelly Risk       : %.2f%% ($%.2f)\n", fullKelly*100, fullKelly**capitalPtr)
		fmt.Printf("Adjusted Kelly Risk   : %.2f%% ($%.2f)\n", adjKelly*100, amountToRisk)
		fmt.Println("--------------------------------------------------")

		if *stopLossPtr > 0 {
			// Total Position Size = Amount at Risk / Stop Loss %
			totalPositionValue := amountToRisk / *stopLossPtr

			// Leverage Needed = Total Position Value / Total Capital
			leverageNeeded := totalPositionValue / *capitalPtr

			fmt.Printf("Stop Loss Threshold   : %.2f%%\n", *stopLossPtr*100)
			fmt.Printf("📌 TOTAL POSITION SIZE: $%.2f\n", totalPositionValue)

			if leverageNeeded > 1 {
				fmt.Printf("🔥 Required Leverage  : %.2fx (Margin required: $%.2f)\n", leverageNeeded, *capitalPtr)
			} else {
				fmt.Printf("🟢 Required Leverage  : %.2fx (No margin needed)\n", leverageNeeded)
			}
		}

		fmt.Println("==================================================")
		fmt.Println("💡 Tip: Real-world distributions have 'Fat Tails'.")
		fmt.Println("   Professional algorithmic traders rarely exceed Half-Kelly (0.5)")
	}
}
