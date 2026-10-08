# am-shortlink-service (Go + MongoDB)

Viết lại `am-shortlink-sale-api` (PHP 5.5 / Lumen / MySQL) theo [docs/PLAN_SERVICE_API.md](docs/PLAN_SERVICE_API.md).
Dùng **chung MongoDB** với Portal (`am-shortlink-sale-portal`): Portal đọc trực tiếp `am_shortlink` + `am_shortlink_report`,
hai bên nối qua hợp đồng schema trong [migrations/schema.go](migrations/schema.go).

## Binary

| Lệnh | Vai trò | Cổng |
|------|---------|------|
| `cmd/redirect` | `GET /{prefix}/{code}` → 302 (và `/{code}` khi proxy cắt prefix). Không ghi DB đồng bộ; phát ClickEvent | 8090 |
| `cmd/api` | API đối tác v1/v2/v3, **giữ nguyên input / output** của hệ PHP | 8091 |
| `cmd/consumer` | Đọc Kafka → `clicks`, `stats_*`, `link_params`…; job `total_links_snapshot` | 8092 (health) |
| `cmd/migrate` | Tạo collection + index (idempotent), counters, prefix / param_registry mặc định | — |
| `cmd/apikey` | Cấp / xoay / thu hồi API key (thay `php artisan shortlink:api-key`) | — |

## Chạy local

```bash
# 1. MongoDB dùng chung với Portal (cổng 27018) — có sẵn dữ liệu seed của Portal
docker compose -f ../am-shortlink-sale-portal/deploy/docker-compose.yml up -d mongo
# (tuỳ chọn) Redis + Kafka của Service
docker compose -f deploy/docker-compose.yml up -d            # Redis :6381
docker compose -f deploy/docker-compose.yml --profile kafka up -d   # + Kafka :9094

cp .env.example .env
go run ./cmd/migrate
go run ./cmd/apikey -user partner_a      # in API key một lần
go run ./cmd/api &
go run ./cmd/redirect &
# có Kafka: đặt KAFKA_BROKERS=localhost:9094 rồi chạy thêm go run ./cmd/consumer
```

Không có Kafka → `CLICK_SINK=direct`: redirect / api tự ghi thống kê ngay trong tiến trình (dev).

```bash
curl -s -X POST localhost:8091/api/v2/shorten -H 'Content-Type: application/json' \
  -d '{"key":"<api_key>","url":"https://fpt.vn/internet?utm_source=zalo"}'
curl -si localhost:8090/sale/<ending>
```

## Tương thích API (giữ nguyên input / output)

- Envelope `{"error":0|1,"error_description":...,"data":...}`, lỗi nghiệp vụ **HTTP 200 + error = 1**.
- JSON mã hoá như `json_encode` của PHP: `/` → `\/`, tiếng Việt → `\uXXXX`, **giữ thứ tự khoá**.
- Input như `$request->input()`: query + form (`urls[0][path]=…`) + JSON body.
- Thông báo lỗi tiếng Việt nguyên văn, cùng thứ tự kiểm tra (`$validator->errors()->first()`).
- Hành vi lỗi / rủi ro của hệ cũ được **giữ nguyên** cho tới khi chốt ở §9 của plan (D4, D6, D7, D8, D10…):
  v1 delete/restore không kiểm chủ, v2 search tra mọi user, v2 shorten-multi dùng lại link trùng long_url của
  mọi user, update-shorten-multi ghi đè chủ link, cache-clear mở cho mọi key, rate limit tắt (`RATE_LIMIT_PER_SECOND=0`).

Khác biệt không tránh được (ghi nhận để so golden test):
- `long_url_hash` là sha1 (MySQL dùng crc32); `clicks` không tính bot (định nghĩa §5.2).
- Ảnh QR sinh bằng thư viện Go → base64 khác byte với endroid/qr-code (cùng nội dung QR).
- Lỗi 500 không kèm stack trace (D9).
- Kiểu số của MySQL (PDO có thể trả `"12"` dạng chuỗi) — cần xác nhận bằng golden test từ prod.

## Phạm vi đã làm / chưa làm

Đã làm: redirect + 404/500 HTML, link cache LRU → Redis → Mongo, click pipeline (Kafka / direct), consumer ghi
`clicks` + `stats_*` daily/monthly + `link_params` + `param_values` + `click_facets` theo định nghĩa §5.2,
API v1 (shorten, qrcode, delete, restore, link_avail_check), v2 (shorten, shorten-multi, update-shorten-multi,
report, search, update-links-ctv-identifier, cache-*, campaign, template), v3 (shorten, shorten-multi),
quota API, whitelist IP, X-Forwarded-Host, TrackingApi / FMI bất đồng bộ, CLI API key, migrate schema.

Chưa làm (phase sau, proxy vẫn trỏ về PHP): báo cáo legacy `/api/v2/report-total*`, `report-click-*`,
`report-overview-users`, `report-total-detail`; API báo cáo `/api/v4/*`; export; GeoIP; tool backfill MySQL → Mongo
(`DATA_MIGRATION_PLAN.md`); job đối soát / rebuild `stats_*`; OpenAPI spec + golden test; OpenTelemetry.
