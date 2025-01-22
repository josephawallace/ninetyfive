package main

import (
	"context"
	"time"

	"cloud.google.com/go/logging"
	secretmanager "cloud.google.com/go/secretmanager/apiv1beta2"

	"github.com/josephawallace/ninetyfive/configs"
	"github.com/josephawallace/ninetyfive/internal/common"
	"github.com/josephawallace/ninetyfive/internal/gridmanager"
	"github.com/josephawallace/ninetyfive/internal/jupiter"
	"github.com/josephawallace/ninetyfive/internal/logger"
)

func main() {
	ctx := context.Background()

	// Initialize the GCP Secret Manager
	sm, err := secretmanager.NewClient(ctx)
	if err != nil {
		panic(err)
	}
	defer sm.Close()

	// Initialize the configuration loaded from the YAML
	cfg, err := configs.NewConfig(ctx, sm)
	if err != nil {
		panic(err)
	}

	// Conditionally create a logging client for Google Cloud Logging for production environments
	var lc *logging.Client
	if cfg.Environment == configs.ProductionEnvironment {
		lc, err = logging.NewClient(ctx, cfg.GcpProjectId)
		if err != nil {
			panic(err)
		}
	}

	// Initialize our custom Jupiter client that essentially wraps other Jupiter libs and exposes a few specialty
	// functions for our purposes
	j, err := jupiter.NewJupiter(cfg)
	if err != nil {
		panic(err)
	}

	// Initialize our custom logger that intelligently uses either `zerolog` or `gcp.logging`
	log := logger.NewLogger(lc)

	// Initialize the Grid Manager responsible for generating BUY/SELL/DO_NOTHING signals based on the grid strategy
	gm := gridmanager.NewGridManager(7, 10, "neutral", "35-65", "low", "rsx", log)
	log.Info().Msg("setup successfully completed initializing system configuration, logging, Secret Manager, and Jupiter Client")

	// Enter the main loop for feeding price data into the Grid Manager
	for {
		// Sleep at the top of the loop to allow a log and a `continue` statement for errors while maintaining the
		// configured data interval
		time.Sleep(time.Duration(cfg.IntervalSeconds) * time.Second)

		// Retrieve the price for the quote asset, to be used as the next data point in our grid strategy
		var price float64
		price, err = j.GetPrice(cfg.QuoteCurrency)
		if err != nil {
			log.Error().Err(err).Msg("failed to get quote currency price")
			continue
		}
		log.Info().Msg("quote currency price - $%f", price)

		// Receive a signal from the Grid Manager to dictate the bot's action
		var signal common.Signal
		signal, err = gm.Process(price)
		if err != nil {
			log.Error().Err(err).Msg("failed to process interval")
			continue
		}
		log.Info().Msg("%s signal received", signal)

		// Swap the configured fixed amount of the assets - since this is an LP and not an orderbook, there aren't
		// technically buy/sell order, but instead only swaps - the order of the parameters to the `SubmitSwap`
		// function dictate the order type
		var txId string
		switch signal {
		case common.BuySignal:
			txId, err = j.SubmitSwap(ctx, cfg.BaseCurrency, cfg.QuoteCurrency, cfg.BuyOrderSize)
			if err != nil {
				log.Error().Err(err).Msg("failed to submit swap")
				continue
			}
		case common.SellSignal:
			txId, err = j.SubmitSwap(ctx, cfg.QuoteCurrency, cfg.BaseCurrency, cfg.SellOrderSize)
			if err != nil {
				log.Error().Err(err).Msg("failed to submit swap")
				continue
			}
		default:
			log.Info().Msg("no action taken this interval")
			continue
		}

		log.Info().Msg("submitted swap %s", txId)
		go j.MonitorTx(ctx, txId, log)
	}
}
