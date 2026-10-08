# Kế hoạch môi trường (dev / staging / production) & observability (Kafka log + OpenTelemetry)

> **Trạng thái:** Đề xuất (draft) · **Ngày lập:** 2026-10-06
> **Thuộc dự án:** dùng chung cho Project 1 [`PLAN_SERVICE_API.md`](PLAN_SERVICE_API.md) và Project 2 [`PLAN_PORTAL_REPORT.md`](PLAN_PORTAL_REPORT.md) (tổng quan [`REWRITE_PLAN.md`](REWRITE_PLAN.md)) · **Checklist:** [`REWRITE_CHECKLIST.md`](REWRITE_CHECKLIST.md) mục J, P
> **Áp dụng cho:** `redirect-svc`, `api-svc`, `consumer`, `migrate` (Go — Service) và `portal-api` (Go) + `portal-web` (Next.js) — Portal
> **Chuẩn hạ tầng tham chiếu:** project `fptvn-web` (`C:\xampp7\htdocs\fptvn-web`) — dùng chung cụm Kafka `isc-kafka0x`, OTel Collector `otel-collector.fpt.net`, quy ước Redis `*-redis-cache-{env}` (xem §1.1)

---

## 1. Hiện trạng (đọc từ code hệ PHP)

| Hạng mục | Hiện tại | Vấn đề |
|----------|----------|--------|
| Môi trường | Trên server **chỉ có staging và production**; **chưa có môi trường dev** — `.env.Development` chỉ dùng chạy local (XAMPP). 3 file `.env.Development`, `.env.Staging`, `.env.Production` | Dev phải test thẳng trên staging; ⚠️ **Cả 3 file đang được commit vào git** (chứa DB password, API key FMI, MaxMind license, client secret...) — vi phạm hard rule "never commit secrets" |
| Domain | Production `fpt.vn/sale`, `fpt.vn/lm` · Staging `staging.fpt.vn/sale`, `staging.fpt.vn/lm` · Development dùng chung giá trị production | Dev sinh link mang domain production |
| CI/CD | GitLab, template `isc/cicd-config/ci-template/gitflow` (PHP), image Harbor, deploy K8s | Không có bước test bắt buộc |
| Log Kafka | Monolog `KafkaHandler` → `KafkaPush` (php-rdkafka); env `KAFKA_LOG_BROKERS`, `KAFKA_LOG_TOPIC` (vd `dev-dkol-web-logs`); xem trên Kibana | Mỗi log **tạo producer mới + flush đồng bộ tới 1s** trong request; `.env.Production` **không có** `KAFKA_LOG_*` (cần xác nhận có inject qua CI không) |
| Định dạng log | `{log_type: "logs" \| "tracking_utm", environment, service, data (chuỗi JSON), url}` | Không có `trace_id`, level, latency; `data` là chuỗi |
| OpenTelemetry | `OpenTelemetryManualMiddleware` (SDK PHP) + env `OTEL_ENABLED`, `OTEL_SERVICE_NAMESPACE`, `OTEL_SERVICE_NAME`, `OTEL_ENDPOINT_TRACES`, `OTEL_ENDPOINT_LOG`, `OTEL_SAMPLER`, `OTEL_SAMPLER_RATIO` (chỉ staging/prod) | `OtelTracer` **hard-code** `http://otel-collector.fpt.net/v1/traces`; không trace DB / HTTP ra ngoài; không có metrics |

### 1.1 Chuẩn đang dùng ở `fptvn-web` (áp dụng lại cho hệ mới)

| Hạng mục | Giá trị / cách làm ở `fptvn-web` | File tham chiếu |
|----------|----------------------------------|-----------------|
| Kafka brokers | `isc-kafka01:9092,isc-kafka02:9092,isc-kafka03:9092` — **cùng 1 cụm cho staging và production**, PLAINTEXT (không SASL/TLS) | `.env.staging`, `.env.production` |
| Tên topic log | staging `stag-fptvn-web-logs`, production `fptvn-web-logs` (**prod không có tiền tố**), dev theo mẫu `dev-…-logs` | như trên, comment trong `KafkaPush` |
| Biến Kafka | `KAFKA_LOG_BROKERS`, `KAFKA_LOG_TOPIC` | |
| Đẩy log Kafka | Gom log vào buffer trong request, **flush 1 lần sau khi trả response** (`register_shutdown_function`, timeout 500ms); bỏ qua log của request file tĩnh | `app/Helpers/KafkaConnect/KafkaPush.php`, `app/Logging/KafkaHander.php` |
| Định dạng message | `{log_type: logs \| tracking_utm, environment, service, data, url}` — giống hệt shortlink PHP | `KafkaPush::prepareMsgData` |
| OTel Collector | OTLP/HTTP **`http://otel-collector.fpt.net/v1/traces`** và **`/v1/logs`** — cùng endpoint cho mọi môi trường | `app/Packages/Otel/OpentelemetryManualProvider.php` |
| Biến OTel | `OTEL_ENABLED=true`, `OTEL_SERVICE_NAMESPACE`, `OTEL_SERVICE_NAME`, `OTEL_ENDPOINT_TRACES`, `OTEL_ENDPOINT_LOG`, `OTEL_SAMPLER` (`always` \| `ratio`), `OTEL_SAMPLER_RATIO` | `.env.*` |
| Sampling | local/staging `always`; production `ratio` **0.05**; bọc `ParentBased` | |
| Resource | `service.namespace` (staging `fptvn`, prod `FPTVN` — không thống nhất hoa/thường), `service.name`, `service.instance.id` = `HOSTNAME`, `service.version` = `APP_VERSION`, `deployment.environment` = `APP_ENV` | |
| Processor | `BatchSpanProcessor`, `BatchLogRecordProcessor` (không dùng Simple vì chặn request ~0,19s/span) | |
| Log → OTel | Chỉ log mức **error** gửi qua OTLP logs; log thường đi Kafka | `injectLogConfig()` |
| Metrics | Chỉ xuất ra **stdout** (chưa đẩy metrics lên Collector) | |
| Trace DB / HTTP ra ngoài | Span cho mỗi query (`QueryExecuted`); span HTTP client gắn `peer.service` dạng `{service}.{namespace}` và header `traceparent` W3C | `app/Packages/Otel/MyHttpTracerHelper.php` |
| Redis | Host theo môi trường: dev `fptvn-redis-cache-dev`, staging `fptvn-redis-cache-staging`, prod `fptvn-redis-cache`; port `6379`, DB `0`, standalone (`cluster: false`), **không mật khẩu**; dùng cho cache + session (`CACHE_DRIVER=redis`, `SESSION_DRIVER=redis`); client `predis` | `.env.*`, `config/database.php` |
| Cache | Bật/tắt + TTL qua env: `CACHE_ENABLED`, `CACHE_TIME=1800`, `CACHE_HTML*`; Redis lỗi → bỏ qua cache, không làm hỏng request | `modules/Frontend/Helpers/CacheRedis.php` |

**Lưu ý — không chép lại lỗi của `fptvn-web`:**
- `LogsExporter` đang dùng `$transport` (endpoint traces) thay vì `$transportLog` → log OTLP bị gửi nhầm sang `/v1/traces`.
- Closure `MyOpentelemetryLogHandle` dùng biến `$loggerProvider` chưa khai báo.
- `service.namespace` khác hoa/thường giữa staging và prod → khó lọc trên backend trace.
- `CacheRedis` gọi `PING` trước **mỗi** lệnh get/set (gấp đôi round-trip) và dùng `SET` + `EXPIRE` rời nhau (không atomic) → Go dùng pool kết nối + `SET key val EX ttl`.
- Redis không mật khẩu, repo commit `.env.*` (cùng vấn đề E5).

---

## 2. Thiết kế môi trường

### 2.1 Bảng môi trường

| | **dev** | **staging** | **production** |
|---|---------|-------------|----------------|
| Mục đích | **Mới — trước đây không có.** Dev tích hợp hằng ngày, thử nghiệm, không chiếm staging | Kiểm thử trước release, UAT, dry-run migrate, load test | Phục vụ người dùng thật |
| Nhánh / trigger (gitflow) | `develop` → tự deploy | `release/*`, `hotfix/*` → tự deploy + smoke test | Tag `vX.Y.Z` từ `main` → **duyệt tay** |
| Domain redirect | `dev.fpt.vn/sale`, `dev.fpt.vn/lm` *(hoặc host nội bộ — E6)* | `staging.fpt.vn/sale`, `staging.fpt.vn/lm` | `fpt.vn/sale`, `fpt.vn/lm` |
| Domain API | `sl-api-dev…` | `sl-api-stag.fpt.vn` | `sl-api.fpt.vn` |
| Portal | `portal-dev…` | `portal-stag…` | `portal…` |
| Dữ liệu | Seed giả (script `make seed`) | Bản sao prod **đã ẩn danh** (mask IP, SĐT CTV, email) | Thật |
| MongoDB | 1 node (replica set 1 member để có change stream) | Replica set 3 node (cấu hình giống prod, nhỏ hơn) | Replica set 3 node + backup + PITR |
| Redis | `am-shortlink-redis-cache-dev` (đặt tên theo quy ước `fptvn-web`) | `am-shortlink-redis-cache-staging` | `am-shortlink-redis-cache` (cân nhắc replica/Sentinel vì redirect phụ thuộc cache — E11) |
| Kafka | Cụm `isc-kafka01..03:9092` (xác nhận dev có dùng chung không — E8), tiền tố `dev-` | Cụm `isc-kafka01..03:9092`, tiền tố `stag-` | **Cùng cụm** `isc-kafka01..03:9092`, **không tiền tố** (theo chuẩn `fptvn-web`) |
| OTel Collector | `http://otel-collector.fpt.net` (OTLP/HTTP) | `http://otel-collector.fpt.net` | `http://otel-collector.fpt.net` |
| Tích hợp ngoài (TrackingApi, FMI, GeoIP) | Mock / sandbox | Sandbox (FMI non-prod) | Thật |
| Log level | `debug` | `info` | `info` (redirect thành công: lấy mẫu) |
| Trace sampling | `always` | `always` | `ratio` 0.05 (như `fptvn-web`) + tail 100% lỗi/chậm nếu Collector hỗ trợ (E9) |
| Replica pod | 1 mỗi service | 2 | Theo HPA (redirect ≥ 3) |
| Quyền truy cập | Dev team | Dev + QA + PO | Ops; dev chỉ đọc log/trace |

### 2.2 Môi trường local

`docker compose up` dựng đủ: MongoDB (replica set 1 node), Redis, Kafka (Redpanda cho nhẹ), OTel Collector, Jaeger (xem trace), Kafka UI (xem log/topic). Lệnh `make dev`, `make seed`, `make test`. Không cần kết nối hạ tầng chung để chạy.

### 2.3 Quản lý cấu hình & secret

- **12-factor**: mọi cấu hình đọc từ biến môi trường; service **validate lúc khởi động** và dừng ngay nếu thiếu/sai (fail fast).
- **Không commit file `.env` của staging/production.** Repo chỉ có `.env.example` (không giá trị thật) và `config/{dev,staging,prod}.yaml` cho giá trị **không bí mật** (domain, prefix, sampling...).
- **Secret** (DB URI, Kafka SASL, key OpenTelemetry, API key FMI, MaxMind, JWT/session key) lưu ở **HashiCorp Vault** của ISC, nạp vào pod qua CI template / helm (§2.4b, E4); rotate định kỳ.
- **Key Kafka & OpenTelemetry (chủ dự án cấp):** mỗi môi trường một bộ key riêng (dev / staging / production); nhập thẳng vào **Vault** (`isc-project/<PROJECT_FULLNAME>/<env>/am-shortlink`, xem §2.4b); tạm thời có thể dùng GitLab CI Variables (Masked + Protected) — **không** gửi qua chat, không ghi vào tài liệu, không commit. Local dùng `.env.local` (đã `.gitignore`). Service log ra *có/không có* key, không bao giờ log giá trị.
- **Build một lần, deploy nhiều nơi**: cùng 1 image cho dev → staging → prod, chỉ khác cấu hình.
- Portal Next.js: chỉ biến `NEXT_PUBLIC_*` không bí mật ra trình duyệt; API key / session secret chỉ ở server (BFF).

### 2.4 Danh mục biến môi trường (Go)

**Cấu hình Kafka log đã được cấp cho shortlink:**

```env
KAFKA_LOG_BROKERS="isc-kafka01:9092,isc-kafka02:9092,isc-kafka03:9092"
KAFKA_LOG_TOPIC=stag-am-shortlink-logs   # staging (đã cấp)
# production: am-shortlink-logs (theo quy ước, chờ xác nhận) · dev: dev-am-shortlink-logs (chờ xác nhận)
```

**Cấu hình OpenTelemetry đã được cấp cho shortlink** (không chứa secret):

```env
OTEL_ENABLED=true
OTEL_SERVICE_NAMESPACE=am-shortlink
OTEL_SERVICE_NAME=am-shortlink-sale-api
OTEL_ENDPOINT_TRACES=http://otel-collector.fpt.net/v1/traces
OTEL_ENDPOINT_LOG=http://otel-collector.fpt.net/v1/logs
OTEL_SAMPLER=always        # dev / staging; production: ratio
OTEL_SAMPLER_RATIO=0.1     # chỉ dùng khi OTEL_SAMPLER=ratio; production đề xuất 0.05 (E3)
```

| Biến mới | Thay cho (PHP) | Ghi chú |
|----------|----------------|---------|
| `APP_ENV` = `dev` \| `staging` \| `production` | `APP_ENV` | Dùng cho `deployment.environment` |
| `APP_HOST` | phần host của `APP_ADDRESS` | `fpt.vn` / `staging.fpt.vn` |
| `APP_PROTOCOL` | `APP_PROTOCOL` | |
| `DEFAULT_PREFIX`, `DEFAULT_PREFIX_V3` | `APP_ADDRESS`, `APP_ADDRESS_V3` | `sale`, `lm` |
| `MONGO_URI`, `MONGO_DB` | `DB_*` | secret |
| `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_DB` | `CACHE_DRIVER` | Giữ đúng tên biến như `fptvn-web`; host `am-shortlink-redis-cache[-dev\|-staging]`, port `6379`, DB `0`; đặt mật khẩu (khuyến nghị, khác `fptvn-web`) |
| `CACHE_ENABLED`, `REDIRECT_CACHE_TTL`, `REPORT_CACHE_TTL`, `REDIS_KEY_PREFIX` | `SETTING_REDIRECT_CACHE`, `SETTING_REPORT_CACHE_TTL` | TTL redirect 3600s, report 60s; key prefix `am-shortlink:` để không đụng hệ khác nếu dùng chung Redis |
| `KAFKA_LOG_BROKERS` (giữ tên cũ) | `KAFKA_LOG_BROKERS` | `isc-kafka01:9092,isc-kafka02:9092,isc-kafka03:9092`; dùng chung cho log + event |
| `KAFKA_SASL_MECHANISM`, `KAFKA_SASL_USERNAME`, `KAFKA_SASL_PASSWORD`, `KAFKA_SECURITY_PROTOCOL` (`SASL_PLAINTEXT`\|`SASL_SSL`), `KAFKA_CA_CERT` | — | **Key Kafka do chủ dự án cấp** (secret). Thiếu → service chạy PLAINTEXT như `fptvn-web` |
| `KAFKA_TOPIC_PREFIX` | — | `dev-` / `stag-` / **rỗng** (prod) — áp cho topic event và consumer group |
| `KAFKA_LOG_TOPIC` | `KAFKA_LOG_TOPIC` | `dev-am-shortlink-logs` / `stag-am-shortlink-logs` / `am-shortlink-logs` |
| `LOG_LEVEL`, `LOG_KAFKA_ENABLED`, `LOG_REDIRECT_SAMPLE_RATIO` | — | |
| `OTEL_ENABLED`, `OTEL_SERVICE_NAMESPACE`, `OTEL_SERVICE_NAME`, `OTEL_ENDPOINT_TRACES`, `OTEL_ENDPOINT_LOG`, `OTEL_SAMPLER`, `OTEL_SAMPLER_RATIO` | giữ nguyên tên | **Giữ đúng bộ biến như `fptvn-web`** để CI/Ops dùng chung quy ước; service Go map sang SDK (`otlptracehttp.WithEndpointURL(...)`) |
| `OTEL_EXPORTER_OTLP_HEADERS` | — | **Hiện không cần** — Collector `otel-collector.fpt.net` không yêu cầu key (cấu hình đã cấp không có header xác thực). Để sẵn biến nếu nền tảng bật xác thực sau này |
| `ALLOW_IP`, `TRACKING_USERNAMES`, `FMI_BASE_URL`, `FMI_API_KEY`, `GEOIP_DB_PATH`, `REPORT_CACHE_TTL`... | tương ứng | `USERNAME` đổi tên cho rõ nghĩa |

### 2.4b Nạp secret từ Vault (HashiCorp Vault của ISC)

**Hiện trạng:** cả `am-shortlink-sale-api` và `fptvn-web` khai báo Vault trong `env.sh`:

```bash
export VAULT_PROJECT_PATH=$(echo "isc-project/${PROJECT_FULLNAME}" | tr -s "/")
export VAULT_PROJECT_ROLE=$(echo "isc-project-${PROJECT_FULLNAME}" | tr -s "-")
```

Hai biến này được CI template `isc/cicd-config/ci-template/gitflow` dùng để lấy secret (cơ chế nằm trong template / helm config `cmt-helm-config`, không có trong repo → xác nhận với team CI/CD — E12).

**Nguyên tắc cho hệ mới:**
- **Ứng dụng không tự gọi Vault API.** Nền tảng nạp secret thành **biến môi trường** hoặc **file** (Vault Agent Injector / CSI / CI template) → service Go và Portal chỉ đọc env. Đổi cách nạp không phải sửa code.
- Config loader hỗ trợ thêm dạng `<TÊN>_FILE` (vd `MONGO_URI_FILE=/vault/secrets/mongo-uri`) để đọc secret từ file Vault Agent ghi ra; có cả `TÊN` và `TÊN_FILE` → ưu tiên file.
- Khởi động: kiểm đủ secret bắt buộc, thiếu → dừng và báo **tên** biến thiếu (không bao giờ in giá trị). Log `secrets loaded: [MONGO_URI, REDIS_PASSWORD, ...]`.
- Rotate secret: đổi trong Vault → rollout lại pod (hoặc Vault Agent tự render lại file + service reload kết nối).

**Bố trí secret trong Vault** (đề xuất, mỗi môi trường một nhánh):

```
isc-project/<PROJECT_FULLNAME>/
├── dev/am-shortlink/
├── staging/am-shortlink/
└── production/am-shortlink/
     ├── MONGO_URI
     ├── REDIS_PASSWORD
     ├── KAFKA_SASL_USERNAME / KAFKA_SASL_PASSWORD   (nếu cụm bật SASL)
     ├── FMI_API_KEY, TRACKING_API_KEY
     ├── MAXMIND_LICENSE_KEY
     └── MYSQL_REPLICA_DSN, CDC_MYSQL_PASSWORD   (chỉ trong giai đoạn migrate)
```

Portal dùng nhánh riêng `isc-project/<PROJECT_FULLNAME>/<env>/am-shortlink-portal/`: `portal-api` → `MONGODB_URI` (user `portal_api`), `PORTAL_JWT_SIGNING_KEY`, `PII_HASH_SALT`, `REDIS_PASSWORD` (Redis cache riêng của Portal); `portal-web` → `PORTAL_SESSION_SECRET`, OIDC client secret (nếu SSO). `portal-web` **không** có thông tin kết nối MongoDB.

**Không đưa vào Vault** (không bí mật, để ở `config/{env}.yaml` / env thường): `KAFKA_LOG_BROKERS`, `KAFKA_LOG_TOPIC`, `OTEL_*`, `APP_HOST`, prefix, TTL cache, sampling.

**Local:** không truy cập Vault; dùng `.env.local` (đã `.gitignore`) với giá trị dev.

### 2.5 Luồng phát hành

```
feature/* ──MR (lint, unit test, Sonar)──► develop ──auto──► dev
release/x.y ──auto + smoke + e2e──► staging ──QA/UAT duyệt──► tag vX.Y.Z ──duyệt tay──► production (canary)
hotfix/* ──► staging ──► production
```
- Script tạo index / migration Mongo chạy như **K8s Job trước deploy**, idempotent.
- Feature flag (env/config) cho các thay đổi hành vi đã chốt ở §9 `PLAN_SERVICE_API.md`, bật dần theo môi trường.
- Staging **không bao giờ** kết nối DB / Redis / API production. Kafka dùng **chung cụm** với production nên cách ly bằng **tên topic + consumer group có tiền tố môi trường**; consumer staging tuyệt đối không subscribe topic không tiền tố.

---

## 3. Log qua Kafka

### 3.1 Kiến trúc

```
Go service ──slog JSON──┬──► stdout (luôn có, K8s thu thập)
                        └──► Kafka async handler ──► topic [dev-|stag-]am-shortlink-logs ──► pipeline hiện có ──► Elasticsearch/Kibana
                                 (buffer + batch, không chặn request)
```

**Nguyên tắc:** ghi log **không bao giờ chặn request** (shortlink PHP flush đồng bộ tới 1s; `fptvn-web` đã cải thiện bằng buffer + flush sau response; Go làm tốt hơn bằng 1 producer dùng chung, gửi batch nền). Buffer đầy → bỏ log mức `debug`/`info` trước, đếm vào metric `log_kafka_dropped_total`; `error` luôn ghi stdout.

Phương án (chốt E2):

| | A. App đẩy thẳng Kafka *(khuyến nghị giai đoạn đầu)* | B. stdout → Fluent Bit → Kafka | C. OTLP logs → OTel Collector → Kafka |
|---|---|---|---|
| Ưu | Giữ đúng format Kibana đang đọc; không phụ thuộc nền tảng | App đơn giản nhất | Một pipeline cho log + trace |
| Nhược | App giữ kết nối Kafka | Cần DaemonSet Fluent Bit | Collector cần kafka exporter, đổi format |

### 3.2 Topic

| Topic | Nội dung | Producer config | Retention |
|-------|----------|-----------------|-----------|
| `[pfx]am-shortlink-logs` | Log ứng dụng, access log API (kể cả `log_type=tracking_utm`, giữ chung topic như `fptvn-web`) | `acks=1`, `lz4`, `linger=50ms` | 3–7 ngày (log còn ở ES) |
| `[pfx]am-shortlink-click-events` | `ClickEvent` (nghiệp vụ, **không phải log**) | `acks=all`, idempotent, `zstd` | 7 ngày, replay được |
| `[pfx]am-shortlink-link-events` | `LinkCreated/Updated/Deleted` | `acks=all`, idempotent | 7 ngày |
| `[pfx]am-shortlink-cdc.*` | Debezium (giai đoạn migrate) | do Debezium | theo `DATA_MIGRATION_PLAN.md` |

`[pfx]` = `dev-` / `stag-` / rỗng (prod), theo quy ước `fptvn-web`. Consumer group cũng mang tiền tố: `[pfx]am-shortlink-consumer`. Topic do team Kafka/Ops tạo trước (xác nhận auto-create — E8).

Log và event nghiệp vụ **tách topic** vì khác yêu cầu tin cậy và retention.

### 3.3 Định dạng message

Giữ các trường cũ để pipeline Kibana không vỡ, thêm trường chuẩn:

```json
{
  "log_type": "logs",
  "environment": "production",
  "service": "am-shortlink-sale-api",
  "service_version": "1.4.0",
  "timestamp": "2026-10-06T08:15:30.123Z",
  "level": "info",
  "message": "shorten created",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7",
  "request_id": "01J9...",
  "url": "api/v3/shorten",
  "method": "POST",
  "status": 200,
  "latency_ms": 12,
  "client_ip": "113.161.x.x",
  "user": "partner_a",
  "api_version": "v3",
  "prefix": "lm",
  "data": "{\"ending\":\"abc12\",\"is_new\":true}",
  "error": null
}
```

### 3.4 Quy tắc nội dung log

- **Cấm log:** `key` (API key), mật khẩu, session/cookie, header `Authorization`.
- **Che (mask):** SĐT (`fullname`, `phonenumber` trong query tracking, `ctv_identifier` dạng SĐT) → `090****052`; IP ở log mức info → 2 octet cuối; query string của `long_url` chỉ giữ tên tham số với các key nhạy cảm.
- Access log redirect (lưu lượng rất lớn): prod lấy mẫu theo `LOG_REDIRECT_SAMPLE_RATIO` (vd 1%), **100%** với 4xx/5xx; dữ liệu click đầy đủ đã nằm ở `click-events`.
- Mỗi lỗi gọi API ngoài (TrackingApi, FMI) log kèm `trace_id`, mã lỗi, thời gian, **không** kèm payload chứa SĐT.

---

## 4. OpenTelemetry

### 4.1 Phạm vi instrument

| Thành phần | Thư viện |
|------------|----------|
| HTTP server (chi) | `otelhttp` / `otelchi` — span theo **route template** (`/{prefix}/{code}`, `/api/v2/shorten`), không theo URL thật |
| MongoDB | `otelmongo` (contrib) — tắt ghi nội dung câu lệnh ở prod |
| Redis | `redisotel` (go-redis) — traces + metrics |
| Kafka | `kotel` (franz-go) — truyền context qua header message, consumer nối trace với producer |
| HTTP client ra ngoài (TrackingApi, FMI, GeoIP) | `otelhttp.Transport` + `peer.service` dạng `{service}.{namespace}` như `fptvn-web` |
| Log | `slog` handler tự gắn `trace_id`, `span_id` |
| Runtime | `otel/contrib/instrumentation/runtime` (GC, goroutine, memory) |
| Portal `portal-web` (Next.js) | `instrumentation.ts` + `@vercel/otel` (hoặc `@opentelemetry/sdk-node`): span server-side, truyền `traceparent` khi gọi `portal-api` |
| Portal `portal-api` (Go) | `otelhttp` (chi) + `otelmongo` như Service; không trace Redis / Kafka (không dùng) |

Context propagation **W3C `traceparent`** xuyên suốt: Service: api-svc → Mongo/Redis/Kafka → consumer → TrackingApi/FMI; Portal: `portal-web` → `portal-api` → MongoDB. Response trả header `traceresponse` + `X-Request-Id` (đưa vào body lỗi để hỗ trợ tra cứu).

### 4.2 Resource attributes

`service.name` (`am-shortlink-sale-api` — **đã cấp**, dùng cho api-svc để giữ liền mạch trace với hệ PHP; `am-shortlink-redirect`, `am-shortlink-consumer`, `am-shortlink-portal-web`, `am-shortlink-portal-api` — xin cấp thêm), `service.namespace` = `am-shortlink` (chữ thường ở mọi môi trường — tránh lỗi `fptvn`/`FPTVN`), `service.instance.id` = `HOSTNAME` (tên pod), `service.version` = git tag, `deployment.environment` = `dev`/`staging`/`production`.

### 4.3 Exporter & sampling

- OTLP/**HTTP** (như `fptvn-web`) → `http://otel-collector.fpt.net/v1/traces` và `/v1/logs`; Go dùng `otlptracehttp` / `otlploghttp` (HTTP thường, `WithInsecure`). Endpoint lấy từ env, **không hard-code**.
- Log gửi qua OTLP chỉ mức **error** (giống `fptvn-web`); log còn lại đi Kafka.
- Metrics: `fptvn-web` mới xuất stdout → hỏi team nền tảng Collector có nhận `/v1/metrics` không (E9); chưa có thì mở `/metrics` Prometheus.
- Dùng **Batch** span/log processor (không Simple) — rút kinh nghiệm `fptvn-web`.
- Sampling: `ParentBased` + dev/staging `always`, prod `ratio` **0.05** (chuẩn `fptvn-web`). Tail sampling giữ 100% lỗi/chậm chỉ khi Collector hỗ trợ (E9).
- SDK lỗi / Collector chết → service vẫn chạy (export bất đồng bộ, có giới hạn hàng đợi).

### 4.4 Metrics

| Nhóm | Metric |
|------|--------|
| HTTP (RED) | `http.server.request.duration` theo route, method, status; số request; tỉ lệ lỗi |
| Redirect | `shortlink.redirect.total{result=hit\|miss\|not_found\|disabled, prefix}`, `shortlink.cache.hit_ratio`, `shortlink.redirect.duration` |
| Shorten | `shortlink.links.created{api_version, prefix, is_new}`, `shortlink.quota.exceeded`, `shortlink.code.collision_retry` |
| Kafka | `messaging.publish.duration`, lỗi produce, `log_kafka_dropped_total`, **consumer lag** theo partition |
| Consumer / báo cáo | `stats.aggregation.delay_seconds`, số event lỗi / DLQ, kết quả job đối soát |
| Tích hợp ngoài | Thời gian / lỗi TrackingApi, FMI, GeoIP |
| Hạ tầng | Mongo op duration, pool; Redis latency; Go runtime |
| Migrate | rows/s, CDC lag, `migration_errors` |

### 4.5 Dashboard & cảnh báo (production)

| Cảnh báo | Ngưỡng gợi ý | Mức |
|----------|--------------|-----|
| Redirect 5xx | > 0,5% trong 5 phút | P1 |
| Redirect p99 | > 50ms trong 10 phút | P2 |
| Cache hit ratio | < 90% trong 15 phút | P3 |
| Consumer lag click-events | > 60s | P2 |
| Kafka produce lỗi / log bị drop | > 0 kéo dài 5 phút | P3 |
| TrackingApi / FMI lỗi | > 10% trong 10 phút | P3 |
| Job đối soát lệch | Bất kỳ | P2 |
| CDC lag (giai đoạn migrate) | > 60s | P2 |

Dashboard: Tổng quan dịch vụ (RED), Redirect, Shorten/API theo đối tác, Kafka & consumer, Báo cáo/đối soát, Migrate. Staging có cùng dashboard nhưng không gửi cảnh báo trực.

---

## 5. Lộ trình gắn với plan Service (`PLAN_SERVICE_API.md` §6) và Portal (`PLAN_PORTAL_REPORT.md` §8)

| Phase | Việc |
|-------|------|
| 0 | Chốt E1–E12; tạo nhánh secret trong Vault cho dev/staging/production; xin cấp Redis `am-shortlink-redis-cache-*`, topic Kafka `[pfx]am-shortlink-*`; **gỡ `.env.Staging`, `.env.Production` khỏi git và rotate secret của hệ PHP hiện tại** (không chờ hệ mới) |
| 1 | Docker compose local; config loader + validate; slog + Kafka async handler; OTel SDK (traces + metrics) cho HTTP/Mongo/Redis; pipeline CI 3 môi trường; **dựng mới môi trường dev** (namespace K8s, Mongo/Redis, topic `dev-`, domain, OTel Collector endpoint) |
| 2 | Instrument Kafka (`kotel`), consumer, HTTP client ra ngoài; metrics redirect; dashboard Redirect |
| 3–4 | Metrics shorten / báo cáo; dashboard theo đối tác; dựng staging giống prod |
| 5 | OTel cho `portal-web` (Next.js) và `portal-api` (Go); trace đầu-cuối trình duyệt → `portal-web` → `portal-api` → MongoDB |
| 6 | Dashboard migrate + CDC; cảnh báo prod; runbook trực |

---

## 6. Quyết định cần chốt

| # | Câu hỏi | Đề xuất |
|---|---------|---------|
| E1 | Mapping nhánh gitflow ↔ môi trường của template `isc/cicd-config` | Như §2.5, xác nhận với team CI/CD |
| E2 | Đường đi log: app → Kafka, Fluent Bit, hay OTel Collector | A (app → Kafka, giữ format Kibana) giai đoạn đầu |
| E3 | Tỉ lệ sampling trace & log redirect ở prod | Trace `ratio` 0.05 (chuẩn `fptvn-web`); log redirect 1% + 100% lỗi |
| E4 | Nơi lưu secret | ✅ **HashiCorp Vault** của ISC (`VAULT_PROJECT_PATH=isc-project/${PROJECT_FULLNAME}`, `VAULT_PROJECT_ROLE`), nạp qua CI template / helm → env hoặc file; không file `.env` trong repo |
| E5 | Xử lý file `.env.*` đang commit trong repo hiện tại | Gỡ khỏi git, thêm `.gitignore`, **rotate toàn bộ secret** đã lộ |
| E6 | Môi trường dev (mới) đặt ở đâu và dùng domain nào; ai cấp hạ tầng (namespace K8s, Mongo, Redis, Kafka) | Namespace K8s riêng trên cluster non-prod; host nội bộ / `dev.fpt.vn` (không dùng domain prod) |
| E7 | Retention log theo môi trường (Kafka, Elasticsearch) | dev 3 ngày, staging 7 ngày, prod 30 ngày (ES) |
| E8 | Cụm Kafka `isc-kafka01..03` có dùng cho dev không; ai tạo topic (auto-create bật/tắt); topic event nghiệp vụ (`click-events`) đặt trên cụm này hay cần cụm riêng | Hỏi team Kafka/Ops; event nghiệp vụ cần `acks=all`, replication ≥ 3 | — ✅ Topic log staging **đã cấp**: `stag-am-shortlink-logs`; còn chờ: topic prod / dev, topic event nghiệp vụ |
| E9 | OTel Collector `otel-collector.fpt.net` có nhận metrics (`/v1/metrics`), có tail sampling không; xem trace ở đâu | Hỏi team nền tảng |
| E10 | Tên service / namespace trên Kibana và backend trace | ✅ **Đã chốt** `OTEL_SERVICE_NAMESPACE=am-shortlink`, `OTEL_SERVICE_NAME=am-shortlink-sale-api` (api-svc). Còn lại: xin tên cho redirect / consumer / portal theo mẫu `am-shortlink-*` |
| E11 | Redis riêng cho shortlink hay dùng chung `fptvn-redis-cache-*`; có đặt mật khẩu, có replica không | **Redis riêng** `am-shortlink-redis-cache[-dev\|-staging]` (redirect phụ thuộc cache, tránh bị hệ khác flush/đầy bộ nhớ); bật mật khẩu; prod có replica |
| E12 | Cơ chế Vault cụ thể của template ISC (inject env lúc deploy, Vault Agent sidecar hay CSI); đường dẫn con theo môi trường; ai có quyền ghi secret | Hỏi team CI/CD; áp dụng cấu trúc §2.4b |
