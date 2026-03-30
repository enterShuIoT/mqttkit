package mqttkit

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"
)

// Options configures the MQTT client. Zero values are adjusted by normalize().
type Options struct {
	BrokerURLs []string

	ClientID string
	Username string
	Password string
	TLS      *tls.Config

	ConnectRetry         time.Duration
	ConnectTimeout       time.Duration
	MaxReconnectInterval time.Duration
	// AutoReconnect defaults to true when nil (stable behavior aligned with common deployments).
	AutoReconnect *bool
	CleanSession  bool

	PublishQueueSize int

	// Locker is optional; Client does not call it. Use in handlers with WithLock / ConsumerPartitionKey
	// when multiple gateway instances share MQTT subscriptions to prevent duplicate consumption.
	Locker DistributedLocker

	Logger Logger
}

func (o *Options) normalize() error {
	urls := expandBrokerURLs(o.BrokerURLs)
	if len(urls) == 0 {
		return fmt.Errorf("%w: empty BrokerURLs", ErrInvalidOption)
	}
	o.BrokerURLs = urls
	if o.ConnectRetry <= 0 {
		o.ConnectRetry = 5 * time.Second
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 30 * time.Second
	}
	if o.MaxReconnectInterval <= 0 {
		o.MaxReconnectInterval = 120 * time.Second
	}
	if o.PublishQueueSize <= 0 {
		o.PublishQueueSize = 8192
	}
	if o.Logger == nil {
		o.Logger = nopLogger{}
	}
	if o.AutoReconnect == nil {
		v := true
		o.AutoReconnect = &v
	}
	return nil
}

func expandBrokerURLs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	var out []string
	for _, s := range in {
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// TLSConfigFromPEM loads a CA bundle for MQTTS server verification.
func TLSConfigFromPEM(caFile string) (*tls.Config, error) {
	pemData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("mqttkit: no certificates parsed from %s", caFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
