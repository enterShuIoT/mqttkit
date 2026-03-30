package mqttkit

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"
)

// Options 配置 MQTT 客户端。零值在 normalize 中会被替换为合理的默认连接参数、队列长度等。
type Options struct {
	// BrokerURLs Broker 地址列表，例如 "tcp://host:1883"、"ssl://host:8883"。
	// 也可在单元素中使用英文逗号分隔多个地址以兼容旧配置习惯。
	BrokerURLs []string

	ClientID string
	Username string
	Password string
	// TLS 非 nil 时交给 Paho（常用于 MQTTS）；可与 TLSConfigFromPEM 配合。
	TLS *tls.Config

	// ConnectRetry Paho 自动重连时的重试间隔，normalize 后默认 5s。
	ConnectRetry time.Duration
	// ConnectTimeout 初次 Connect 等待 token 的超时，默认 30s。
	ConnectTimeout time.Duration
	// MaxReconnectInterval Paho 重连退避上限，默认 120s。
	MaxReconnectInterval time.Duration
	// AutoReconnect 为 nil 时 normalize 视为 true（与常见部署一致，打开自动重连）。
	AutoReconnect *bool
	// CleanSession 是否干净会话，交给 Paho。
	CleanSession bool

	// PublishQueueSize 未连接时异步 Publish 的离线队列最大条数，默认 8192。
	PublishQueueSize int

	// CmdQueueCap 发往 runLoop 的命令通道容量（Subscribe/Unsubscribe/Publish）。
	// 过小易在突发时阻塞调用方；过大占用内存。默认 2048。
	CmdQueueCap int

	// Logger 诊断日志；nil 时使用空实现（不输出）。
	Logger Logger
}

// normalize 校验并填充 Options 默认值。
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
	if o.CmdQueueCap <= 0 {
		o.CmdQueueCap = 2048
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

// expandBrokerURLs 展开切片中逗号分隔的 URL，并 trim 空白。
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

// TLSConfigFromPEM 从 PEM 文件加载 CA 证书池，用于校验 MQTTS 服务端证书。
// 返回的 tls.Config 已设置 MinVersion 为 TLS 1.2。仅服务端校验场景可自行扩展 InsecureSkipVerify 等字段。
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
