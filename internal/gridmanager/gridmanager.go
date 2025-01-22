package gridmanager

import (
	"math"

	"github.com/rs/zerolog/log"

	"github.com/josephawallace/ninetyfive/internal/common"
	"github.com/josephawallace/ninetyfive/internal/logger"
)

// MarketDirection enumerations for clarity:
const (
	DirUp      = 1
	DirNeutral = 0
	DirDown    = -1
)

// RsiType enumerations for clarity:
const (
	RsiTypeClassic = iota
	RsiTypeRSX
)

// GridManager holds parameters and per-bar “memory” to replicate Pine Script logic.
type GridManager struct {
	// ----- User-set parameters (from TradingView “Inputs”) -----
	RsiLength       int
	NumberOfGrids   int
	MarketDirection int // 1 = up, 0 = neutral, -1 = down
	NoTradeZonePips int
	AggressionLevel int // 0=low,1=med,2=high
	CurrentRsiType  int // 0=RSI,1=RSX

	// ----- Dynamic state for bar-to-bar logic -----
	lastRsiValue float64 // RSI/RSX value from the previous bar
	currentRsi   float64 // RSI/RSX value for the current bar

	lastSignal      float64 // 1=Buy, -1=Sell, 0=none
	lastSignalIndex int
	signalLine      float64

	// Used in RSI computations
	avgGain      float64
	avgLoss      float64
	prevRawPrice float64

	// Used in RSX computations (mirroring Pine’s variables)
	f8, f10, f28, f30, f38, f40, f48, f50  float64
	f58, f60, f68, f70, f78, f80, f88, f90 float64
	f90_, f0                               float64

	// Grid lines from 1..99
	gridLines []float64

	// Pine’s “Buy” and “Sell” flags for the current bar
	buy  bool
	sell bool

	log logger.Logger
}

// NewGridManager builds a GridManager whose fields match the TradingView script’s defaults/inputs.
func NewGridManager(rsiLength, numberOfGrids int, direction string, ntZone string, aggLevel string, rsiType string, logger logger.Logger) *GridManager {
	gm := &GridManager{}

	// 1) Map the user’s textual inputs to numeric values
	gm.RsiLength = rsiLength
	gm.NumberOfGrids = numberOfGrids + 1 // The script does “+1” internally
	gm.MarketDirection = parseDirection(direction)
	gm.NoTradeZonePips = parseNoTradeZone(ntZone)
	gm.AggressionLevel = parseAggression(aggLevel)
	gm.CurrentRsiType = parseRsiType(rsiType)

	// 2) Initialize RSI / RSX memory
	gm.prevRawPrice = 0
	gm.avgGain = 0
	gm.avgLoss = 0

	// 3) Initialize last signal to none
	gm.lastSignal = 0
	gm.lastSignalIndex = 0
	gm.signalLine = 50.0 // Pine starts at mid-level

	// 4) Create grid lines
	gm.initGridLines()

	// 5) Add logger
	gm.log = logger

	gm.log.Info().Msg("[GridManager] Initialized with RsiLength=%d, Grids=%d, Dir=%s, NTZ=%s, Agg=%s, RsiType=%s",
		rsiLength, numberOfGrids, direction, ntZone, aggLevel, rsiType)

	return gm
}

// parseDirection converts a direction string (“up”, “down”, “neutral”) into an integer.
func parseDirection(dir string) int {
	switch dir {
	case "up":
		return DirUp
	case "down":
		return DirDown
	default:
		return DirNeutral
	}
}

// parseNoTradeZone converts the string representation into half-range integers.
func parseNoTradeZone(nt string) int {
	switch nt {
	case "45-55":
		return 5
	case "40-60":
		return 10
	case "35-65":
		return 15
	case "30-70":
		return 20
	default: // "n/a"
		return 0
	}
}

// parseAggression converts “low”, “med”, “high” into 0,1,2
func parseAggression(agg string) int {
	switch agg {
	case "med":
		return 1
	case "high":
		return 2
	default: // "low"
		return 0
	}
}

// parseRsiType => “rsi” -> 0, “rsx” -> 1
func parseRsiType(t string) int {
	if t == "rsx" {
		return RsiTypeRSX
	}
	return RsiTypeClassic
}

// initGridLines constructs the array of grid values from 1..99
func (gm *GridManager) initGridLines() {
	gm.gridLines = make([]float64, gm.NumberOfGrids)
	if gm.NumberOfGrids < 2 {
		gm.gridLines[0] = 50
		return
	}

	step := 100.0 / float64(gm.NumberOfGrids-1)
	for i := 0; i < gm.NumberOfGrids; i++ {
		gm.gridLines[i] = step * float64(i)
	}
	gm.gridLines[0] = 1
	gm.gridLines[gm.NumberOfGrids-1] = 99
}

// getGridValue safely fetches a grid line
func (gm *GridManager) getGridValue(idx int) float64 {
	if idx < 0 || idx >= len(gm.gridLines) {
		return 0
	}
	return gm.gridLines[idx]
}

// Process is called once per bar with that bar’s close price. Returns the recommended signal.
func (gm *GridManager) Process(price float64) (common.Signal, error) {
	gm.log.Debug().Msg("[GridManager] Processing new bar. Price=%.4f", price)

	// 1) Compute RSI/RSX
	if gm.CurrentRsiType == RsiTypeRSX {
		gm.currentRsi = gm.computeRSX(price)
	} else {
		gm.currentRsi = gm.computeRSI(price)
	}

	if gm.lastRsiValue == 0 {
		// Warm-up bar => store RSI + do-nothing
		gm.lastRsiValue = gm.currentRsi
		gm.log.Debug().Msg("[GridManager] First bar - warming up. CurrentRSI=%.2f => DO_NOTHING.", gm.currentRsi)
		noSig := common.DoNothingSignal
		return noSig, nil
	}

	gm.log.Debug().Msg("[GridManager] RSI/RSX=%.2f (prev=%.2f)", gm.currentRsi, gm.lastRsiValue)

	// 2) Reset buy/sell for this bar
	gm.buy = false
	gm.sell = false

	// 3) Find the buy/sell line indexes
	buyIdx := gm.getBuyLineIndex()
	sellIdx := gm.getSellLineIndex()
	gm.buy = (buyIdx > 0)
	gm.sell = (sellIdx > 0)
	log.Printf("[GridManager] BuyLineIndex=%d, SellLineIndex=%d => buy=%t, sell=%t", buyIdx, sellIdx, gm.buy, gm.sell)

	// 4) Apply aggression filter
	gm.applyAggressionFilter()
	log.Printf("[GridManager] After aggression => buy=%t, sell=%t", gm.buy, gm.sell)

	// 5) Apply no-trade zone filter
	gm.applyNoTradeZoneFilter()
	log.Printf("[GridManager] After no-trade zone => buy=%t, sell=%t", gm.buy, gm.sell)

	// 6) Direction filter
	gm.applyDirectionFilter()
	log.Printf("[GridManager] After direction filter => buy=%t, sell=%t", gm.buy, gm.sell)

	// 7) Determine final signal
	var outSignal common.Signal
	switch {
	case gm.buy:
		gm.lastSignal = 1
		gm.lastSignalIndex = buyIdx
		outSignal = common.BuySignal
	case gm.sell:
		gm.lastSignal = -1
		gm.lastSignalIndex = sellIdx
		outSignal = common.SellSignal
	default:
		outSignal = common.DoNothingSignal
	}

	gm.signalLine = gm.getGridValue(gm.lastSignalIndex)
	log.Printf("[GridManager] signalLine=%.2f, lastSignal=%.0f, lastSignalIndex=%d, finalSignal=%s",
		gm.signalLine, gm.lastSignal, gm.lastSignalIndex, outSignal)

	// 8) Update memory for next iteration
	gm.lastRsiValue = gm.currentRsi

	return outSignal, nil
}

// -------------------------------------------------------------------------------------
//
//	getBuyLineIndex / getSellLineIndex
//
// -------------------------------------------------------------------------------------
func (gm *GridManager) getBuyLineIndex() int {
	idx := 0
	for x := 0; x < gm.NumberOfGrids; x++ {
		lineVal := gm.getGridValue(x)
		// Condition from Pine:
		// if RSI[1]<lineVal && RSI>=lineVal && RSI[1]<=SignalLine[1] => x
		if gm.lastRsiValue < lineVal && gm.currentRsi >= lineVal && gm.lastRsiValue <= gm.signalLine {
			idx = x
		}
		// also => if RSI[1]>99 && RSI<=99 => idx = NoGrids-1
		if gm.lastRsiValue > 99 && gm.currentRsi <= 99 {
			idx = gm.NumberOfGrids - 1
		}
	}
	return idx
}

func (gm *GridManager) getSellLineIndex() int {
	idx := 0
	for x := 0; x < gm.NumberOfGrids; x++ {
		lineVal := gm.getGridValue(x)
		// if RSI[1]>lineVal && RSI<=lineVal && RSI[1]>=SignalLine[1] => x
		if gm.lastRsiValue > lineVal && gm.currentRsi <= lineVal && gm.lastRsiValue >= gm.signalLine {
			idx = x
		}
		// if RSI[1]<1 && RSI>=1 => idx=0
		if gm.lastRsiValue < 1 && gm.currentRsi >= 1 {
			idx = 0
		}
	}
	return idx
}

// -------------------------------------------------------------------------------------
//
//	Filters: Aggression / No-Trade Zone / Direction
//
// -------------------------------------------------------------------------------------
func (gm *GridManager) applyAggressionFilter() {
	// Pine logic:
	// if AGGR>0 => skip same-level trades
	// else => simpler check
	gi := 100.0 / float64(gm.NumberOfGrids-1)

	if gm.AggressionLevel > 0 {
		topIdx := (gm.NumberOfGrids - 1) - gm.AggressionLevel
		botIdx := 1 + gm.AggressionLevel
		topVal := gm.getGridValue(topIdx)
		botVal := gm.getGridValue(botIdx)

		if gm.currentRsi > gm.signalLine && gm.lastRsiValue >= botVal {
			gm.buy = false
		}
		if gm.currentRsi < gm.signalLine && gm.lastRsiValue <= topVal {
			gm.sell = false
		}
	} else {
		// Aggression=0 => simpler
		if gm.lastRsiValue > gm.signalLine-gi {
			gm.buy = false
		}
		if gm.lastRsiValue < gm.signalLine+gi {
			gm.sell = false
		}
	}
}

func (gm *GridManager) applyNoTradeZoneFilter() {
	// if RSI[1] > 50-NTZ && RSI[1] < 50+NTZ => buy=false, sell=false
	lowerBound := 50.0 - float64(gm.NoTradeZonePips)
	upperBound := 50.0 + float64(gm.NoTradeZonePips)
	if gm.lastRsiValue > lowerBound && gm.lastRsiValue < upperBound {
		gm.buy = false
		gm.sell = false
	}
}

func (gm *GridManager) applyDirectionFilter() {
	// if RSI<100 or RSI>1 => skip signals if they go against the direction
	if gm.currentRsi < 100 || gm.currentRsi > 1 {
		gi := 100.0 / float64(gm.NumberOfGrids-1)
		if gm.MarketDirection == DirDown && gm.currentRsi >= gm.signalLine-(2*gi) {
			gm.buy = false
		}
		if gm.MarketDirection == DirUp && gm.currentRsi <= gm.signalLine+(2*gi) {
			gm.sell = false
		}
	}
}

// -------------------------------------------------------------------------------------
//   RSI / RSX Calculation
// -------------------------------------------------------------------------------------

// computeRSI uses a simplified Welles Wilder smoothing approach each bar.
func (gm *GridManager) computeRSI(price float64) float64 {
	if gm.prevRawPrice == 0 {
		gm.prevRawPrice = price
		return 50.0
	}

	delta := price - gm.prevRawPrice
	gm.prevRawPrice = price

	gain := 0.0
	loss := 0.0
	if delta > 0 {
		gain = delta
	} else {
		loss = -delta
	}

	if gm.avgGain == 0 && gm.avgLoss == 0 {
		gm.avgGain = gain
		gm.avgLoss = loss
	} else {
		alpha := 1.0 / float64(gm.RsiLength)
		gm.avgGain = (1-alpha)*gm.avgGain + alpha*gain
		gm.avgLoss = (1-alpha)*gm.avgLoss + alpha*loss
	}

	if gm.avgLoss == 0 {
		return 100
	}
	rs := gm.avgGain / gm.avgLoss
	rsi := 100.0 - (100.0 / (1.0 + rs))
	return clamp(rsi, 0, 100)
}

// computeRSX is the specialized “Juriki-like” RSI. Each bar we feed in the new price.
func (gm *GridManager) computeRSX(price float64) float64 {
	// On the first call, set up initial conditions
	if gm.f8 == 0 && gm.f10 == 0 {
		gm.f8 = 100.0 * price
		gm.f10 = gm.f8
		gm.f90_ = 1
		gm.f88 = float64(gm.RsiLength - 1)
		if gm.f88 < 5 {
			gm.f88 = 5
		}
		return 50.0
	}

	oldF8 := gm.f8
	gm.f8 = 100.0 * price
	v8 := gm.f8 - oldF8

	f18 := 3.0 / float64(gm.RsiLength+2)
	f20 := 1.0 - f18

	gm.f28 = f20*gm.f28 + f18*v8
	gm.f30 = f18*gm.f28 + f20*gm.f30
	vC := 1.5*gm.f28 - 0.5*gm.f30

	gm.f38 = f20*gm.f38 + f18*vC
	gm.f40 = f18*gm.f38 + f20*gm.f40
	v10 := 1.5*gm.f38 - 0.5*gm.f40

	gm.f48 = f20*gm.f48 + f18*v10
	gm.f50 = f18*gm.f48 + f20*gm.f50
	v14 := 1.5*gm.f48 - 0.5*gm.f50

	gm.f58 = f20*gm.f58 + f18*math.Abs(v8)
	gm.f60 = f18*gm.f58 + f20*gm.f60
	v18 := 1.5*gm.f58 - 0.5*gm.f60

	gm.f68 = f20*gm.f68 + f18*v18
	gm.f70 = f18*gm.f68 + f20*gm.f70
	v1C := 1.5*gm.f68 - 0.5*gm.f70

	gm.f78 = f20*gm.f78 + f18*v1C
	gm.f80 = f18*gm.f78 + f20*gm.f80
	v20 := 1.5*gm.f78 - 0.5*gm.f80

	// f90_ logic from Pine
	gm.f90_ = func() float64 {
		if gm.f90_ == 0 {
			return 1
		}
		if gm.f88 <= gm.f90_ {
			return gm.f88 + 1
		}
		return gm.f90_ + 1
	}()

	if gm.f88 == 0 {
		gm.f88 = float64(gm.RsiLength - 1)
		if gm.f88 < 5 {
			gm.f88 = 5
		}
	}

	fTemp0 := 0.0
	if gm.f88 >= gm.f90_ && gm.f8 != oldF8 {
		fTemp0 = 1
	}
	gm.f0 = fTemp0

	if gm.f88 == gm.f90_ && gm.f0 == 0 {
		gm.f90 = 0
	} else {
		gm.f90 = gm.f90_
	}

	rsxVal := 50.0
	if gm.f88 < gm.f90 && v20 > 0 {
		rsxVal = (v14/v20 + 1.0) * 50.0
	}
	return clamp(rsxVal, 0, 100)
}

// clamp bounds a value between min and max
func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
