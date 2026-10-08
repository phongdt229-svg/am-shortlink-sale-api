// Package config đọc cấu hình từ biến môi trường (dùng chung cho redirect, api, consumer, migrate).
//
// Mỗi biến có thể đọc từ file qua <TÊN>_FILE (secret mount từ Vault), ví dụ MONGODB_URI_FILE.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	AppEnv   string `envconfig:"APP_ENV" default:"local"`
	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`

	Mongo    Mongo
	Redis    Redis
	Kafka    Kafka
	Redirect Redirect
	API      API
	Consumer Consumer
	Tracking Tracking

	// PII_HASH_SALT phải trùng với Portal (mask.HashPII) để tra cứu tham số pii=hash khớp nhau.
	PIIHashSalt string `envconfig:"PII_HASH_SALT" default:"local-dev-pii-salt-0123456789"`
}

type Mongo struct {
	URI            string        `envconfig:"MONGODB_URI" default:"mongodb://localhost:27018/?directConnection=true"`
	CoreDB         string        `envconfig:"MONGODB_DB_CORE" default:"am_shortlink"`
	ReportDB       string        `envconfig:"MONGODB_DB_REPORT" default:"am_shortlink_report"`
	ConnectTimeout time.Duration `envconfig:"MONGODB_CONNECT_TIMEOUT" default:"10s"`
	MaxTime        time.Duration `envconfig:"MONGODB_MAX_TIME" default:"5s"`
}

type Redis struct {
	// Trống = không dùng Redis (cache chỉ trong RAM, quota đếm trên MongoDB).
	Addr      string `envconfig:"REDIS_ADDR"`
	Password  string `envconfig:"REDIS_PASSWORD"`
	DB        int    `envconfig:"REDIS_DB" default:"0"`
	KeyPrefix string `envconfig:"REDIS_KEY_PREFIX" default:"am-shortlink:"`
}

type Kafka struct {
	// Trống = không dùng Kafka; CLICK_SINK=direct ghi thống kê ngay trong tiến trình.
	Brokers    []string `envconfig:"KAFKA_BROKERS"`
	ClickTopic string   `envconfig:"KAFKA_CLICK_TOPIC" default:"am-shortlink-clicks"`
	LinkTopic  string   `envconfig:"KAFKA_LINK_TOPIC" default:"am-shortlink-links"`
	GroupID    string   `envconfig:"KAFKA_GROUP_ID" default:"am-shortlink-consumer"`
}

type Redirect struct {
	HTTPAddr string `envconfig:"REDIRECT_HTTP_ADDR" default:":8090"`
	// Hỗ trợ GET /{code} khi proxy cũ cắt prefix (giai đoạn chuyển).
	LegacyRootRedirect bool          `envconfig:"REDIRECT_LEGACY_ROOT" default:"true"`
	LRUSize            int           `envconfig:"REDIRECT_LRU_SIZE" default:"200000"`
	LRUTTL             time.Duration `envconfig:"REDIRECT_LRU_TTL" default:"60s"`
	RedisTTL           time.Duration `envconfig:"REDIRECT_REDIS_TTL" default:"1h"`
	NegativeTTL        time.Duration `envconfig:"REDIRECT_NEGATIVE_TTL" default:"30s"`
	// kafka | direct | log. Mặc định: kafka nếu có KAFKA_BROKERS, ngược lại direct.
	ClickSink   string `envconfig:"CLICK_SINK"`
	ClickBuffer int    `envconfig:"CLICK_BUFFER" default:"10000"`
}

type API struct {
	HTTPAddr        string `envconfig:"API_HTTP_ADDR" default:":8091"`
	AppProtocol     string `envconfig:"APP_PROTOCOL" default:"https://"`
	AppHost         string `envconfig:"APP_HOST" default:"fpt.vn"`
	DefaultPrefix   string `envconfig:"DEFAULT_PREFIX" default:"sale"`
	DefaultPrefixV3 string `envconfig:"DEFAULT_PREFIX_V3" default:"lm"`
	// Whitelist IP / CIDR cho /api/* (ALLOW_IP cũ). Trống = cho qua.
	AllowIP []string `envconfig:"ALLOW_IP"`
	// Host hợp lệ trong X-Forwarded-Host (cộng thêm domains.is_active).
	ForwardedHosts []string `envconfig:"FORWARDED_HOSTS" default:"fpt.vn,stag.fpt.vn,sl-api-stag.fpt.vn,sl-api.fpt.vn,fpt.net,fpt.com,am-shortlink-sale-api.local,staging.fpt.vn,localhost"`
	// Domain long_url được phép cho /api/v1/shorten (config shorten.domain_allow cũ).
	DomainAllow     []string `envconfig:"DOMAIN_ALLOW" default:"fpt.vn,c.trackig.site,scalef.com,c.demo.scalef.com,shop-stag.fpt.vn,shop.fpt.vn,shop-stag-v3.fpt.vn,shop-stag-v2.fpt.vn,am-tracking.fpt.vn,am-tracking-dev.fpt.vn,am-tracking-stag.fpt.vn"`
	RandomKeyLength int      `envconfig:"PSEUDO_RANDOM_KEY_LENGTH" default:"6"`
	APIKeyLength    int      `envconfig:"API_KEY_LENGTH" default:"15"`
	// Giới hạn request / IP / giây cho shorten. 0 = tắt (mặc định — giữ đúng hệ cũ, D8 chưa chốt).
	RateLimitPerSecond int  `envconfig:"RATE_LIMIT_PER_SECOND" default:"0"`
	DocsEnabled        bool `envconfig:"API_DOCS_ENABLED" default:"true"`
}

type Consumer struct {
	HTTPAddr            string        `envconfig:"CONSUMER_HTTP_ADDR" default:":8092"`
	SuspiciousThreshold int           `envconfig:"SUSPICIOUS_THRESHOLD" default:"20"`
	SnapshotInterval    time.Duration `envconfig:"SNAPSHOT_INTERVAL" default:"10m"`
	RegistryRefresh     time.Duration `envconfig:"PARAM_REGISTRY_REFRESH" default:"1m"`
}

// Tracking: gọi TrackingApi / FMI cho link của các user cấu hình (D2: async, không hard-code user).
type Tracking struct {
	Users        []string      `envconfig:"TRACKING_USERS"`
	BaseURL      string        `envconfig:"TRACKING_BASE_URL"`
	AuthURL      string        `envconfig:"TRACKING_AUTH_URL"`
	ClientID     string        `envconfig:"TRACKING_CLIENT_ID"`
	ClientSecret string        `envconfig:"TRACKING_CLIENT_SECRET"`
	FMIUsers     []string      `envconfig:"FMI_USERS"`
	FMIBaseURL   string        `envconfig:"FMI_BASE_URL"`
	FMIAPIKey    string        `envconfig:"FMI_API_KEY"`
	Timeout      time.Duration `envconfig:"TRACKING_TIMEOUT" default:"10s"`
}

// Load đọc biến môi trường; hỗ trợ <TÊN>_FILE cho secret.
func Load() (*Config, error) {
	loadDotEnv(".env")
	if err := loadFileVars(); err != nil {
		return nil, err
	}
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return nil, fmt.Errorf("đọc cấu hình: %w", err)
	}
	c.API.AppProtocol = strings.TrimSpace(c.API.AppProtocol)
	c.API.AppHost = strings.Trim(strings.TrimSpace(c.API.AppHost), "/")
	if c.Redirect.ClickSink == "" {
		if len(c.Kafka.Brokers) > 0 {
			c.Redirect.ClickSink = "kafka"
		} else {
			c.Redirect.ClickSink = "direct"
		}
	}
	switch c.Redirect.ClickSink {
	case "kafka", "direct", "log":
	default:
		return nil, fmt.Errorf("CLICK_SINK không hợp lệ: %q (kafka | direct | log)", c.Redirect.ClickSink)
	}
	if c.Redirect.ClickSink == "kafka" && len(c.Kafka.Brokers) == 0 {
		return nil, fmt.Errorf("CLICK_SINK=kafka cần KAFKA_BROKERS")
	}
	return &c, nil
}

func (c *Config) IsProduction() bool { return c.AppEnv == "production" }

// loadDotEnv: đọc file .env (local) — chỉ đặt biến CHƯA có trong môi trường. Không có file → bỏ qua.
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}
}

// loadFileVars: X_FILE=/path → X=<nội dung file> (nếu X chưa đặt).
func loadFileVars() error {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasSuffix(k, "_FILE") || v == "" {
			continue
		}
		name := strings.TrimSuffix(k, "_FILE")
		if os.Getenv(name) != "" {
			continue
		}
		b, err := os.ReadFile(v)
		if err != nil {
			return fmt.Errorf("đọc %s: %w", k, err)
		}
		if err := os.Setenv(name, strings.TrimSpace(string(b))); err != nil {
			return err
		}
	}
	return nil
}
