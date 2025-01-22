package jupiter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
	jl "github.com/ilkamo/jupiter-go/jupiter"
	sl "github.com/ilkamo/jupiter-go/solana"

	"github.com/josephawallace/ninetyfive/configs"
	"github.com/josephawallace/ninetyfive/internal/logger"
)

const (
	rpcEndpoint   = "https://api.mainnet-beta.solana.com"
	wsEndpoint    = "wss://api.mainnet-beta.solana.com"
	priceEndpoint = "https://api.jup.ag/price/v2"
)

// PriceData models the object returned from Jupiter for pricing on a particular asset
type PriceData struct {
	Id    string `json:"id"`
	Type  string `json:"type"`
	Price string `json:"price"`
}

// GetPriceResponse models the response from using Jupiter's pricing endpoint
type GetPriceResponse struct {
	Data map[string]PriceData `mapstructure:"data"`
}

// Jupiter is a custom wrapper for interacting with various Jupiter and Solana services
type Jupiter struct {
	cfg *configs.Config
	sc  sl.Client
	smn sl.Monitor
	jc  *jl.ClientWithResponses
	pk  *solana.PublicKey
}

// NewJupiter creates a new custom Jupiter object
func NewJupiter(cfg *configs.Config) (*Jupiter, error) {
	// Build a Solana wallet using the secret key in the config
	sk, err := cfg.SecretKey()
	if err != nil {
		return nil, err
	}
	wallet, err := sl.NewWalletFromPrivateKeyBase58(sk)
	if err != nil {
		return nil, err
	}
	pk := wallet.PublicKey() // Save the public key for attaching to the Jupiter struct

	// Initialize the Solana client responsible for submitting transactions on-chain
	sc, err := sl.NewClient(wallet, rpcEndpoint)
	if err != nil {
		return nil, err
	}

	// Initialize the Jupiter client responsible for creating swap transactions
	jc, err := jl.NewClientWithResponses(jl.DefaultAPIURL)
	if err != nil {
		return nil, err
	}

	// Initialize the Solana Monitor client to watch transactions and track their statuses
	smn, err := sl.NewMonitor(wsEndpoint)
	if err != nil {
		return nil, err
	}

	// Return the Jupiter wrapper for interacting with Solana and Jupiter APIs
	return &Jupiter{
		cfg: cfg,
		sc:  sc,
		smn: smn,
		jc:  jc,
		pk:  &pk,
	}, nil
}

// SubmitSwap interacts with Jupiter to "place an order" given the parameters - it strives for high order success
func (j *Jupiter) SubmitSwap(ctx context.Context, baseCurrency string, quoteCurrency string, amount float64) (string, error) {
	// 1) Get a quote from Jupiter that can be used to form a swap request
	// Convert the input amount to use the asset's most basic unit
	unitAmount, err := j.convertToUnitAmount(baseCurrency, amount)
	if err != nil {
		return "", err
	}
	// Configure options for the quote - most of which are to manage slippage to ensure swaps are accepted
	autoSlippage := true
	dynamicSlippageToggle := true
	preferLiquidDexes := true
	// Get the quote from Jupiter
	getQuoteResponse, err := j.jc.GetQuoteWithResponse(ctx, &jl.GetQuoteParams{
		InputMint:         baseCurrency,
		OutputMint:        quoteCurrency,
		Amount:            unitAmount,
		AutoSlippage:      &autoSlippage,
		DynamicSlippage:   &dynamicSlippageToggle,
		PreferLiquidDexes: &preferLiquidDexes,
	})
	if err != nil {
		return "", err
	}
	if getQuoteResponse.JSON200 == nil {
		return "", fmt.Errorf("could not get quote with error: %s", string(getQuoteResponse.Body))
	}
	quote := *getQuoteResponse.JSON200

	// 2) Get a swap transaction based on the quote that can be signed and broadcast to the network
	// Configure options to follow recommendations for highest success probability
	prioritizationFeeLamports := jl.SwapRequest_PrioritizationFeeLamports{}
	if err = prioritizationFeeLamports.UnmarshalJSON([]byte(`"auto"`)); err != nil {
		return "", err
	}
	dynamicComputeUnitLimit := true
	maxBps := 500
	minBps := 0
	dynamicSlippage := struct {
		MaxBps *int `json:"maxBps,omitempty"`
		MinBps *int `json:"minBps,omitempty"`
	}{
		MaxBps: &maxBps,
		MinBps: &minBps,
	}
	// Get the swap transaction from Jupiter
	postSwapResponse, err := j.jc.PostSwapWithResponse(ctx, jl.PostSwapJSONRequestBody{
		UserPublicKey:             j.pk.String(),
		QuoteResponse:             quote,
		DynamicComputeUnitLimit:   &dynamicComputeUnitLimit,
		PrioritizationFeeLamports: &prioritizationFeeLamports,
		DynamicSlippage:           &dynamicSlippage,
	})
	if err != nil {
		return "", err
	}
	if postSwapResponse.JSON200 == nil {
		return "", fmt.Errorf("could not get swap response with error: %s", string(postSwapResponse.Body))
	}
	swap := *postSwapResponse.JSON200

	// Sign and send the transaction to the network
	txId, err := j.sc.SendTransactionOnChain(ctx, swap.SwapTransaction)
	if err != nil {
		return "", err
	}

	// Return the transaction ID for monitoring
	return string(txId), nil
}

// GetPrice returns the dollar (USDC) price of a given currency
func (j *Jupiter) GetPrice(currency string) (float64, error) {
	prices, err := j.getPrices([]string{currency})
	if err != nil {
		return 0, err
	}
	priceData, ok := prices[currency]
	if !ok {
		return 0, fmt.Errorf("no prices for %s", currency)
	}
	return strconv.ParseFloat(priceData.Price, 64)
}

// MonitorTx follows a submitted transaction through its commitment status for logging/tracking orders
func (j *Jupiter) MonitorTx(ctx context.Context, txId string, log logger.Logger) {
	var (
		res    sl.MonitorResponse
		err    error
		stages = []sl.CommitmentStatus{
			sl.CommitmentProcessed,
			sl.CommitmentConfirmed,
			sl.CommitmentFinalized,
		}
	)

	ctx, cancel := context.WithTimeout(ctx, time.Second*time.Duration(j.cfg.CommitmentTimeoutSeconds))
	defer cancel()

	count := 0
	stageIndex := 0
	for count < j.cfg.MaxRetriesTxMonitor {
		// Give time between retries to allow for transaction propagation
		time.Sleep(5 * time.Second)
		// Count tries at the top of the loop to allow using `continue` for errors
		count++

		// Check if the transaction has reached the current stage evaluated
		if res, err = j.smn.WaitForCommitmentStatus(ctx, sl.TxID(txId), stages[stageIndex]); err != nil {
			continue
		}
		if res.InstructionErr != nil {
			continue
		}

		// Progress to the next stage on success - stop if all stages have been validated
		stageIndex++
		if stageIndex >= len(stages) {
			break
		}
	}

	// Alert that the commitment status was not able to be confirmed as successful
	if count >= j.cfg.MaxRetriesTxMonitor {
		log.Error().Msg("could not get commitment status after %d retries for %s", j.cfg.MaxRetriesTxMonitor, txId)
		return
	}
	// Alert that the commitment status was confirmed as successful and finalized
	log.Info().Msg("commitment status is finalized for transaction %s", txId)
}

// getPrices interacts with the Jupiter pricing endpoint to retrieve pricing data for selected assets
func (j *Jupiter) getPrices(tokenAddresses []string) (map[string]PriceData, error) {
	params := url.Values{}
	params.Add("ids", strings.Join(tokenAddresses, ","))

	u := priceEndpoint + "?" + params.Encode()
	res, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var getPriceResponse GetPriceResponse
	err = json.Unmarshal(body, &getPriceResponse)
	if err != nil {
		return nil, err
	}

	return getPriceResponse.Data, nil
}

// convertToUnitAmount converts a fractional token amount to its base unit representation
func (j *Jupiter) convertToUnitAmount(currency string, amount float64) (int64, error) {
	decimals, err := j.getDecimals([]string{currency})
	if err != nil {
		return 0, err
	}
	unitMultiplier := math.Pow(10, float64(decimals[currency]))
	return int64(amount * unitMultiplier), nil
}

// getDecimals returns the precision available for given assets
func (j *Jupiter) getDecimals(tokenAddresses []string) (map[string]int, error) {
	// Confirmed through manual testing that the pricing endpoint returns the price with full precision, so it can be
	// used to derive the precision value
	prices, err := j.getPrices(tokenAddresses)
	if err != nil {
		return nil, err
	}

	decimals := make(map[string]int)
	for token, priceData := range prices {
		priceParts := strings.Split(priceData.Price, ".")
		if len(priceParts) != 2 {
			decimals[token] = 0
			continue
		}
		decimals[token] = len(priceParts[1])
	}

	return decimals, nil
}
