package common

type Signal string

const (
	BuySignal       Signal = "BUY"
	SellSignal      Signal = "SELL"
	DoNothingSignal Signal = "DO_NOTHING"
)
