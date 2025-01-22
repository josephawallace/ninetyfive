package configs

import (
	"context"
	"fmt"
	"strconv"

	secretmanager "cloud.google.com/go/secretmanager/apiv1beta2"
	"cloud.google.com/go/secretmanager/apiv1beta2/secretmanagerpb"
	"github.com/spf13/viper"
)

const (
	ProductionEnvironment = "production"
)

// Config defines the parameters for the application and is sourced via a YAML file and environment variables
type Config struct {
	BaseCurrency             string  `mapstructure:"base_currency"`
	BuyOrderSize             float64 `mapstructure:"buy_order_size"`
	CommitmentTimeoutSeconds int     `mapstructure:"commitment_timeout_seconds"`
	Environment              string  `mapstructure:"environment"`
	GcpProjectId             string  `mapstructure:"gcp_project_id"`
	IntervalSeconds          int     `mapstructure:"interval_seconds"`
	MaxRetriesTxMonitor      int     `mapstructure:"max_retries_tx_monitor"`
	QuoteCurrency            string  `mapstructure:"quote_currency"`
	SellOrderSize            float64 `mapstructure:"sell_order_size"`
	SmSecretKeyName          string  `mapstructure:"sm_secret_key_name"`
	SmSecretKeyVersion       int     `mapstructure:"sm_secret_key_version"`

	secrets map[string]string
	sm      *secretmanager.Client
}

// NewConfig generated a configuration object
func NewConfig(ctx context.Context, sm *secretmanager.Client) (*Config, error) {
	// Source the YAML file
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./configs")

	// Source environment variables prefixed by "NF_"
	viper.SetEnvPrefix("nf")
	viper.AutomaticEnv()

	// Read from the sources
	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	// Unmarshal into the struct for easier handling
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	cfg.sm = sm // Attach the secret manager

	// Cache the secret key in a map for quicker access during trading
	cfg.secrets = make(map[string]string)
	sk, err := cfg.getSecret(ctx, cfg.SmSecretKeyName, cfg.SmSecretKeyVersion)
	if err != nil {
		return nil, err
	}
	cfg.secrets[cfg.SmSecretKeyName] = sk

	// Return a filled config for consistent parameters across the application
	return &cfg, nil
}

// SecretKey returns the private key for the Solana wallet
func (c *Config) SecretKey() (string, error) {
	sk, ok := c.secrets[c.SmSecretKeyName]
	if !ok {
		return "", fmt.Errorf("secret key not found")
	}
	return sk, nil
}

// getSecret fetches a secret from the Secret Manager using its shorthand name and version (not the full path of the
// secret)
func (c *Config) getSecret(ctx context.Context, name string, version int) (string, error) {
	path := "projects/" + c.GcpProjectId + "/secrets/" + name + "/versions/" + strconv.Itoa(version)
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: path,
	}

	res, err := c.sm.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", err
	}

	return string(res.Payload.Data), nil
}
