# Project 1 — `am-shortlink-service` (Service API: Go + MongoDB)

> **Trạng thái:** Đề xuất (draft) · **Ngày lập:** 2026-10-06
> **Hệ hiện tại:** `am-shortlink-sale-api` — PHP 5.5 / Lumen 5.1 / MySQL (Polr fork)
> **Thuộc chương trình:** [`REWRITE_PLAN.md`](REWRITE_PLAN.md) (tổng quan 2 project) · Project 2: [`PLAN_PORTAL_REPORT.md`](PLAN_PORTAL_REPORT.md)
> **Phạm vi project này:** redirect, API v1/v2/v3 (tương thích), API báo cáo v4 cho đối tác, click pipeline & thống kê (dữ liệu MongoDB mà Portal đọc trực tiếp), migrate dữ liệu
> **Cập nhật 2026-10-07:** Portal (Project 2) có backend Go riêng **kết nối trực tiếp MongoDB** — Service **không** cung cấp API cho Portal; hai bên nối qua **hợp đồng schema MongoDB** (§4, §5.4, §5.4b, `migrations/`)
> **Checklist nghiệm thu đi kèm:** [`REWRITE_CHECKLIST.md`](REWRITE_CHECKLIST.md) (các mục gắn nhãn **Service**)
> **Tài liệu liên quan:** [`SCALING.md`](SCALING.md), KB `am-shortlink-knowledge/`

---

## 1. Bối cảnh & mục tiêu

### Vì sao làm lại
- Stack đã hết vòng đời: PHP 5.5, Lumen 5.1, nhiều thư viện không còn được hỗ trợ; khó tuyển người và vá bảo mật.
- Quy mô ~**30 triệu link** và bảng `clicks` tăng mỗi lần redirect: đã tối ưu 4 phase (xem `SCALING.md`) nhưng giới hạn kiến trúc vẫn còn — ghi click đồng bộ, report quét bảng thô, cache driver `file`.
- Schema không tái tạo được từ repo (migration dừng ở 2017; nhiều cột/bảng tạo tay trên DB).
- Nhiều hành vi rủi ro tích luỹ (xem §9) và admin UI Blade cũ đã bị tắt phần lớn.

### Mục tiêu
| # | Mục tiêu | Đo bằng |
|---|----------|---------|
| G1 | Redirect nhanh, chịu tải lớn | p99 < 20ms, cache hit > 95%, scale ngang |
| G2 | Tương thích ngược API cho client hiện tại | Golden test replay request thật: diff = 0 (trừ mục đã chốt sửa) |
| G3 | Report nhanh trên dữ liệu lớn | Mọi endpoint report < 1s |
| G4 | Cung cấp dữ liệu MongoDB ổn định cho Portal báo cáo (Project 2 đọc trực tiếp) | Hợp đồng schema (`migrations/` có version) chốt cuối Phase 1; `stats_*`, `link_params`, `clicks` làm giàu ghi đúng định nghĩa §5.2 |
| G5 | Không mất dữ liệu khi chuyển hệ | Đối chiếu số link/click khớp 100% |

### Ngoài phạm vi (giai đoạn này)
- Thay đổi hợp đồng API cho client (trừ các bản vá bảo mật đã chốt).
- Data warehouse / BI tuỳ biến (cân nhắc ClickHouse sau).

---

## 2. Hiện trạng cần thay thế (tóm tắt)

| Hạng mục | Hiện tại |
|----------|----------|
| Endpoint | ~50 route: redirect `/{short_url}`, `/api/v1/*` (shorten, delete, restore, qrcode, admin), `/api/v2/*` (shorten, multi, update-multi, report*, search, campaign, template, cache), `/api/v3/*` (shorten, multi với prefix theo user) |
| Dữ liệu | `links`, `clicks`, `users`, `campaigns`, `domains`, `templates` |
| Auth | API key qua tham số `key`; session cho admin; whitelist IP `ALLOW_IP`; kiểm `X-Forwarded-Host` |
| Tích hợp | Kafka (log), OpenTelemetry, MaxMind GeoIP, TrackingApi, FMI API, QR (Google Chart) |
| Response | `{"error":0|1,"error_description":"...","data":...}` |
| Prefix | **2 prefix**: `/sale` (`APP_ADDRESS=fpt.vn/sale`, v1/v2) và `/lm` (`APP_ADDRESS_V3=fpt.vn/lm`, v3). v3 chọn theo `users.prefix` (hiện chỉ có giá trị `sale` hoặc `lm`) → `APP_ADDRESS_V3` → `APP_ADDRESS`. Staging: `staging.fpt.vn/sale`, `staging.fpt.vn/lm`. Route app chỉ là `/{short_url}` → proxy cắt prefix; **hai prefix dùng chung một không gian mã** (mã tạo ở `/lm` cũng mở được qua `/sale`). Link **không lưu** prefix đã cấp |

Chi tiết từng hành vi: xem [`REWRITE_CHECKLIST.md`](REWRITE_CHECKLIST.md).

---

## 3. Kiến trúc mục tiêu

```
                  ┌──────────── Nginx / LB (route theo path) ─────────────┐
 Browser ──GET /{sale|lm}/{code}──► redirect-svc (Go) ──► LRU RAM ─► Redis ─miss─► MongoDB
                                    │ publish ClickEvent (async)
                                    ▼
                                  Kafka ──► click-consumer (Go) ──► Mongo: clicks (time-series)
                                                                 ├─► stats_* link/campaign/ctv/owner ($inc theo lô)
                                                                 ├─► links.clicks ($inc theo lô)
                                                                 └─► TrackingApi / FMI (async, retry)
 API client ──/api/v1|v2|v3/*──► api-svc (Go) ──► MongoDB + Redis
 Portal báo cáo (Project 2: Next.js ──BFF──► portal-api Go) ──đọc trực tiếp──► MongoDB (không qua api-svc)
```

### Nguyên tắc
1. **Redirect là đường nóng**: không ghi DB đồng bộ, không gọi API ngoài đồng bộ.
2. **Một repo Go, ba binary**: `redirect`, `api`, `consumer` — deploy & scale độc lập.
3. **Report đọc dữ liệu tổng hợp trước**, không quét click thô.
4. **Tương thích ngược**: giữ path, tham số, định dạng response và thông báo lỗi tiếng Việt.
5. **Cấu hình thay hard-code** (danh sách domain, user tracking, prefix...).
6. **Prefix là dữ liệu cấu hình, không phải code**: danh sách prefix hợp lệ lưu trong collection `prefixes` (`sale`, `lm`, thêm mới không cần deploy). Redirect-svc tự nhận `/{prefix}/{code}` (không phụ thuộc proxy cắt path); prefix lạ → 404.

### Prefix `/sale` và `/lm`

| Hạng mục | Thiết kế |
|----------|----------|
| Redirect | Route `GET /{prefix}/{code}` với `prefix ∈ prefixes`; vẫn hỗ trợ `GET /{code}` nếu proxy cũ cắt prefix (giai đoạn chuyển). Mặc định giữ **không gian mã chung** (D23) |
| Sinh link | v1/v2 → prefix mặc định `sale`; v3 → `users.prefix` → prefix mặc định v3 (`lm`) → `sale` (giữ nguyên thứ tự hiện tại) |
| Lưu trữ | Link mới lưu `prefix` đã cấp (để hiển thị, báo cáo); host **không** lưu vào dữ liệu, ghép theo môi trường (`fpt.vn` / `staging.fpt.vn`) |
| Cấu hình | Env `APP_HOST`, `DEFAULT_PREFIX=sale`, `DEFAULT_PREFIX_V3=lm`; danh sách prefix trong `prefixes` |
| Proxy / Ingress | Cả `/sale/*` và `/lm/*` trỏ về redirect-svc; `/api/*` trỏ về api-svc |
| Portal (Project 2) | Hiển thị short URL đúng prefix; lọc báo cáo theo prefix. Quản lý prefix/user là phạm vi mở rộng của Portal |
| Báo cáo | Thêm chiều lọc `prefix` (§5.3) |

### Stack
| Lớp | Lựa chọn |
|-----|----------|
| Backend | Go 1.23+, `chi`, `mongo-go-driver v2`, `go-redis v9`, `franz-go` (Kafka), `slog`, OpenTelemetry SDK |
| Validate / config | `go-playground/validator`, `envconfig` |
| Test | `testing` + `testify`, `testcontainers-go` (Mongo/Redis/Kafka), k6 (load) |
| DB | MongoDB 7+ replica set 3 node (time-series collection cho click) |
| Cache / rate limit | Redis 7 |
| Hạ tầng | Docker, GitLab CI (template gitflow `isc/cicd-config`, lint `golangci-lint`, test, Sonar, image Harbor), K8s |
| Môi trường | **dev (mới — trước đây không có)**, staging, production — chi tiết [`ENV_OBSERVABILITY_PLAN.md`](ENV_OBSERVABILITY_PLAN.md) §2 |
| Log | `slog` JSON → stdout + Kafka async (`[dev-|stag-]am-shortlink-logs` trên cụm `isc-kafka01..03`, giữ format Kibana hiện tại) · Cache: Redis `am-shortlink-redis-cache[-dev|-staging]` — §3 file trên |
| Tracing / metrics | OpenTelemetry SDK (HTTP, Mongo, Redis, Kafka, HTTP client) → OTLP → OTel Collector — §4 file trên |

### Cấu trúc repo đề xuất
```
am-shortlink-service/
├── cmd/
│   ├── redirect/        # main redirect service
│   ├── api/             # main API service
│   ├── consumer/        # Kafka click consumer
│   └── migrate/         # tool backfill MySQL -> Mongo
├── internal/
│   ├── config/
│   ├── domain/          # link, user, campaign, click (entity + rule)
│   ├── store/mongo/     # repository
│   ├── cache/           # LRU + Redis
│   ├── http/
│   │   ├── middleware/  # apikey, quota, checkip, forwardedhost, ratelimit, otel
│   │   ├── v1/ v2/ v3/  # handler giữ tương thích
│   │   ├── v4/          # API báo cáo cho đối tác (gọi bằng `key`)
│   │   └── response/    # {error, error_description, data}
│   ├── shortcode/       # sinh mã, validate ending
│   ├── analytics/       # geoip, device, aggregation
│   ├── tracking/        # TrackingApi, FMI client
│   └── events/          # Kafka producer/consumer
├── api/openapi/         # OpenAPI 3.1 (nguồn sự thật của hợp đồng API) — xem §3.1
│   ├── openapi.yaml     # file gốc, $ref sang các file con
│   ├── paths/           # v1/, v2/, v3/, v4/, redirect
│   └── components/      # schemas, responses, parameters, securitySchemes
├── migrations/          # tạo collection + index Mongo
├── test/golden/         # request/response mẫu lấy từ prod
└── deploy/

```


### 3.1 Tài liệu API — OpenAPI / Swagger

**Nguyên tắc: contract-first.** File OpenAPI 3.1 trong `api/openapi/` là **nguồn sự thật** của hợp đồng API đối tác; code server, tài liệu và collection test đều sinh hoặc kiểm từ file này. (Portal không dùng API của Service — hợp đồng với Portal là schema MongoDB, xem §4.) Sửa API = sửa spec trước, review trong MR.

| Hạng mục | Thiết kế |
|----------|----------|
| Chuẩn | OpenAPI **3.1**, tách file theo nhóm (`paths/v1`, `v2`, `v3`, `v4`, `redirect`), `components/` dùng chung |
| Phạm vi | **Mọi** endpoint: legacy v1/v2/v3 (mô tả đúng hành vi hiện tại để giữ tương thích), API báo cáo `/api/v4/reports/*` cho đối tác (§5.6), redirect `GET /{prefix}/{code}` |
| Schema dùng chung | `Envelope` `{error: 0\|1, error_description, data}`; `ShortenResult` `{short_url, ending, is_new}`; `Paginated<T>` (cấu trúc paginator Laravel); `ErrorData`; enum `prefix` (`sale`, `lm`), `type` (`all`, `click`) |
| Mô tả lỗi | Liệt kê **nguyên văn** thông báo lỗi tiếng Việt và ghi chú "lỗi nghiệp vụ trả HTTP 200 + `error: 1`" cho legacy; v4 dùng HTTP status chuẩn + `application/problem+json` |
| Xác thực | `securitySchemes`: `apiKeyParam` — tham số **`key`** (body / query) cho **mọi API đối tác v1–v4, giữ như cũ**; ghi chú whitelist IP (`ALLOW_IP`) — xem §3.2 |
| Ví dụ | Mỗi operation có `examples` request/response lấy từ golden test (đã ẩn danh) |
| Sinh code Go | `oapi-codegen` → kiểu request/response + interface handler (`chi`); CI fail nếu code lệch spec |
| Validate runtime | Middleware `kin-openapi` validate request theo spec (bật ở dev/staging; prod chỉ log cảnh báo cho legacy để không đổi hành vi) |
| Swagger UI | `GET /docs` (Swagger UI hoặc Redoc) + `GET /openapi.yaml` trên api-svc — **bật ở dev/staging; production tắt hoặc chỉ cho IP nội bộ/đăng nhập** |
| Lint & breaking change | `spectral lint` (ruleset nội bộ: đặt tên, có example, có mô tả lỗi) + `oasdiff breaking` so với nhánh `main` → MR phá vỡ hợp đồng bị chặn trừ khi tăng version |
| Collection test | Sinh collection **Insomnia / Postman** từ spec (import trực tiếp `openapi.yaml`); bản hiện tại [`insomnia/am-shortlink-sale-api.insomnia.json`](insomnia/am-shortlink-sale-api.insomnia.json) dùng làm đầu vào viết spec legacy |
| Phát hành tài liệu | CI publish HTML (Redoc) theo version cho đối tác; changelog API theo version |

### 3.2 Xác thực — đã chốt

| Kênh | Cách xác thực | Ghi chú |
|------|---------------|---------|
| **API cho đối tác** — v1, v2, v3 **và** v4 báo cáo | **Giữ như cũ: tham số `key`** (body POST; query string với GET) → SHA-256 → tra `api_keys.key_hash` (`active = true`) → lấy `users` theo `user_id` (`active = 1`, `api_active = 1`), quota `users.api_quota`, whitelist `ALLOW_IP` | Không đổi cách gọi cho đối tác; không bắt buộc header mới. Key **chỉ** lưu dạng hash trong `api_keys` (M5), `users` không giữ key; đối tác vẫn gửi key gốc như trước. Kết quả tra cứu cache LRU/Redis theo hash (TTL ngắn, xoá khi thu hồi / xoay key) |
| **Portal** (Project 2) | Không gọi Service. Đăng nhập do **`portal-api` (Go) của Portal** xử lý: đọc `users` trong MongoDB (bcrypt `password_hash`, `portal_access`, `role`, `viewer_accounts`), tự cấp JWT cho BFF | API key **không** dùng để đăng nhập Portal; phiên Portal **không** dùng được cho API đối tác. Service chỉ cần giữ đúng các trường trên trong `users` |
| Redirect `/{prefix}/{code}` | Không xác thực | |

Endpoint `/api/v4/reports/*` chỉ dành cho đối tác, xác thực bằng `key`; quyền dữ liệu tính theo người dùng sở hữu key (§5.7).

Thứ tự viết spec: **legacy v1/v2/v3 trước** (Phase 0–1, từ code PHP + golden test) → dùng làm hợp đồng cho golden test → v4 báo cáo cho đối tác (Phase 4).

---

## 4. Thiết kế dữ liệu MongoDB

| Collection | Trường chính | Index |
|------------|--------------|-------|
| `links` | `_id` (= id cũ, int64; id mới từ `counters`), `code`, `domain_id`, `long_url`, `long_url_hash` (sha1/xxhash), `owner_id`, `owner_username`, `campaign_id`, `campaign_code`, `ctv_raw`, `ctv_id`, `status` (`active`/`disabled`/`deleted`), `is_custom`, `is_api`, `prefix`, `expires_at`, `clicks`, `ip`, `created_at`, `updated_at` | unique `code` (hoặc `(domain_id, code)`), `(owner_username, long_url_hash)`, `(owner_username, created_at)`, `(campaign_code, created_at)`, `(ctv_id, created_at)` |
| `clicks` | time-series: `ts`, `meta{link_id, owner, campaign_code, ctv_id}`, `ip`, `country`, `province`, `device`, `os`, `browser`, `in_app`, `referer`, `referer_host`, `source_group`, `access_prefix`, `link_prefix`, `link_api_version`, `dest_host`, `utm_*`, `hour`, `weekday`, `is_bot`, `is_suspicious`, `is_repeat`, `user_agent`, `event_id` (xem §5.5 R6b) | `(meta.owner, ts)`, `(meta.campaign_code, ts)`, `(meta.ctv_id, ts)`, `(meta.link_id, ts)`; TTL theo retention |
| `stats_*` (link, campaign, ctv, owner, system — theo ngày / tháng) | xem §5.4 | xem §5.4 |
| `users` | `_id` (= id cũ), `username`, `email`, `password_hash`, `role`, `active`, `api_active`, `api_quota`, `prefix`, `random_key_length`, `is_expires`, `expires_value` | unique `username` |
| `api_keys` | `user_id`, `key_hash` (SHA-256 của key gốc), `active`, `created_at`, `rotated_at`, `last_used_at` | unique `key_hash`, `user_id` |
| `campaigns` | `_id` (= id cũ), `name`, `code`, `created_by`, timestamps | unique `name`, `code` |
| `domains`, `templates` | như hiện tại | — |
| `prefixes` | `_id` (`sale`, `lm`), `is_default`, `is_default_v3`, `active`, `description` | — |

Ghi chú:
- `status` thay cặp cờ `is_disabled` / `is_deleted`; xoá luôn là soft delete, **không cascade** xoá click.
- Dedupe dùng hash mạnh + so khớp `long_url` đầy đủ (bỏ phụ thuộc crc32).
- Sinh mã: random base62 theo độ dài cấu hình, insert và retry khi gặp duplicate key (không đọc-trước-ghi).
- API key: **nguồn duy nhất là `api_keys`** — `users` không có trường `api_key`. Migrate: `users.api_key` (MySQL, bản rõ) → hash SHA-256 → một bản ghi `api_keys` / user (bỏ qua user có `api_key` rỗng). Key là chuỗi ngẫu nhiên đủ dài nên dùng SHA-256 (tra cứu được theo index), không cần bcrypt.
- Quota API: đếm bằng Redis (sliding window), không `COUNT` trên `links`.
- Các collection thống kê (`stats_*`) xem §5.

---

## 5. Hệ thống báo cáo

### 5.1 Hiện trạng & vấn đề

| Báo cáo hiện có | Cách làm hiện tại | Vấn đề |
|-----------------|-------------------|--------|
| `report`, `report-total`, `report-total-daily`, `report-total-range` | JOIN `links` × `clicks`, group theo `campaign_code` | Quét bảng click thô, chậm dần theo dữ liệu |
| `report-click-analytics` | summary / timeline / by_referer / by_device / by_country / by_ip, lọc creator + campaign + ngày ≤ 3 tháng | 6 truy vấn aggregate mỗi lần gọi; cache 60s |
| `report-click-list` | Danh sách từng click, phân trang | OK nhưng không export được |
| `report-overview-users` | Số link / link mới / click theo user + tổng | Group trên toàn bảng |
| `report-total-detail` (CTV) | Tìm link theo `phone`/`hash` bằng `LIKE '%...%'` trên `long_url`, 2 tầng quét (~1s → **~15s**), tối đa 5 link | Không dùng được index; sát timeout 30s |
| CTV | `ctv_identifier` = `utm_extra_ctv` trong long_url (thường là SĐT hoặc hash) | Link cũ chưa backfill đủ; không có danh mục CTV |

**Hướng mới:** mọi báo cáo đọc từ **bảng tổng hợp theo ngày** do consumer cập nhật gần thời gian thực (độ trễ < 1 phút), chỉ đọc click thô cho "danh sách click" và export chi tiết.

### 5.2 Định nghĩa chỉ số (chốt ở Phase 0, dùng thống nhất cho API + Portal)

| Chỉ số | Định nghĩa |
|--------|-----------|
| `total_links` | Số link (không tính `deleted`) thuộc phạm vi lọc, tạo đến hết kỳ |
| `new_links` | Số link tạo **trong kỳ** |
| `active_links` | Số link có ≥ 1 click trong kỳ |
| `clicks` | Tổng lượt redirect thành công trong kỳ (**không** tính bot) |
| `unique_clicks` | Số khách duy nhất / link / ngày, khách = hash(`ip` + `user_agent`); cộng dồn theo ngày |
| `bot_clicks` | Lượt click bị nhận là bot (theo DeviceHelper), tách riêng, không cộng vào `clicks` |
| `suspicious_clicks` | Click từ IP vượt ngưỡng (vd > 20 click / link / giờ) — dùng để soát gian lận CTV |
| `ctr_per_link` | `clicks / active_links` (trung bình click mỗi link có hoạt động) |
| Ngày | Theo múi giờ **Asia/Ho_Chi_Minh**; `to_date` bao gồm trọn ngày cuối |

### 5.3 Chiều báo cáo

- **Tài khoản (account)** — `owner_username` của link (user / đối tác gọi API).
- **Chiến dịch (campaign)** — `campaign_code` (+ `campaign_id` nếu có trong bảng `campaigns`).
- **CTV** — `ctv_id` = `ctv_identifier` chuẩn hoá (SĐT về dạng `0xxxxxxxxx`, hash giữ nguyên). Một CTV có thể xuất hiện ở nhiều tài khoản / chiến dịch.
- **Link** — từng short link.
- **Thời gian** — ngày / tuần / tháng; giờ trong ngày (heatmap).
- **Prefix** — `sale` / `lm` (link mới: prefix đã cấp; link cũ: suy từ `users.prefix` của người tạo, xem M11 trong `DATA_MIGRATION_PLAN.md`).
- **Phân rã** — thiết bị, nguồn (`referer_host`), quốc gia, IP.

Phân cấp drill-down: **Tài khoản → Chiến dịch → CTV → Link → Click**.

### 5.4 Mô hình dữ liệu thống kê (MongoDB)

Consumer nhận `ClickEvent` và `LinkCreated/LinkUpdated/LinkDeleted`, ghi bằng `bulkWrite` `$inc` (upsert) vào các collection:

| Collection | Khoá (unique) | Trường |
|------------|---------------|--------|
| `stats_link_daily` | `(link_id, date)` | `owner`, `campaign_code`, `ctv_id`, `prefix`, `clicks`, `by_prefix{}` (click theo prefix truy cập), `unique_clicks`, `bot_clicks`, `suspicious_clicks`, `by_hour[24]`, `by_device{}`, `by_referer{}` (top-N + `other`), `by_country{}` |
| `stats_campaign_daily` | `(owner, campaign_code, date)` | `clicks`, `unique_clicks`, `bot_clicks`, `new_links`, `active_links`*, `by_device{}`, `by_referer{}`, `by_country{}`, `by_hour[24]` |
| `stats_ctv_daily` | `(ctv_id, owner, campaign_code, date)` | `clicks`, `unique_clicks`, `bot_clicks`, `suspicious_clicks`, `new_links`, `active_links`* |
| `stats_owner_daily` | `(owner, date)` | `clicks`, `unique_clicks`, `bot_clicks`, `new_links`, `active_links`*, `total_links_snapshot` |
| `stats_system_daily` | `(date)` | như trên, toàn hệ thống |
| `stats_*_monthly` | `(…, month)` | Rollup tháng (job đêm) cho báo cáo dài hạn |
| `click_uniques` | `(link_id, date, visitor_hash)` | Phục vụ đếm unique; TTL 2 ngày (chỉ cần trong ngày) |
| `ctvs` *(tuỳ chọn)* | `ctv_id` | `name`, `phone`, `owner`, `status`, `note` — danh mục CTV, import từ hệ nguồn nếu có |

\* `active_links`: tăng khi link có click đầu tiên trong ngày (phát hiện qua upsert `stats_link_daily` lần đầu).

Index thêm trên `links`: `(ctv_id, created_at)`, `(owner_username, campaign_code, created_at)`.
Backfill: tính lại toàn bộ `stats_*` từ dữ liệu click đã migrate; có job **rebuild** theo khoảng ngày để sửa sai lệch.

### 5.4b Tham số URL (query params) — lưu riêng để báo cáo

Mục tiêu: báo cáo theo **bất kỳ tham số nào** trên long URL (`utm_source`, `utm_medium`, `utm_campaign`, `utm_content`, `utm_term`, `utm_extra_ctv`, `ref`, `promo`, `locationid`, `guestid`...), không chỉ UTM, và thêm tham số mới **không cần sửa code**.

**Database riêng cho báo cáo:** toàn bộ dữ liệu phục vụ báo cáo (`stats_*`, `link_params`, `stats_param_*`, `click_facets`, `saved_reports`, export) đặt trong database **`am_shortlink_report`**, tách khỏi database nghiệp vụ **`am_shortlink`** (`links`, `users`, `clicks`...). Cùng cụm MongoDB ở giai đoạn đầu; tách được sang cụm riêng khi tải báo cáo lớn mà không ảnh hưởng redirect / API. Quyền ghi `am_shortlink_report` chỉ cấp cho consumer + job; api-svc chỉ đọc.

| Collection (`am_shortlink_report`) | Khoá | Trường | Ghi chú |
|------------------------------------|------|--------|---------|
| `param_registry` | `key` | `key`, `label` (tên hiển thị), `tracked` (bool), `pii` (`none` / `hash` / `drop`), `normalize` (`lower`, `trim`, `phone`), `max_values`, `status` (`active` / `high_cardinality` / `disabled`), `created_by`, `created_at` | **Danh mục tham số được theo dõi** — admin bật/tắt trên Portal |
| `link_params` | `(link_id, key)` | `link_id`, `owner`, `campaign_code`, `ctv_id`, `key`, `value` (đã chuẩn hoá), `value_raw`, `link_created_at` | Một dòng / tham số / link — tách từ `long_url` **lúc tạo / sửa link**. Index: `(owner, key, value)`, `(key, value)`, `(campaign_code, key, value)`, `link_id` |
| `stats_param_daily` | `(owner, campaign_code, key, value, date)` | `clicks`, `unique_clicks`, `bot_clicks`, `suspicious_clicks`, `new_links`, `active_links`, `by_device{}`, `by_source_group{}` | Consumer `$inc` cho **mọi tham số `tracked`** của link khi có click |
| `stats_param_monthly` | `(owner, campaign_code, key, value, month)` | như trên | Rollup đêm |
| `param_values` | `(owner, key, value)` | `first_seen`, `last_seen`, `links`, `clicks_total` | Facets cho dropdown giá trị tham số (theo quyền) |

**Luồng xử lý:**
1. Tạo / sửa link → parse query string của `long_url` (URL-decode, xử lý tham số lặp `a=1&a=2` thành nhiều giá trị, bỏ fragment `#`) → ghi `link_params` cho **mọi** tham số (kể cả chưa `tracked`, để bật sau vẫn có dữ liệu link) → phát `LinkCreated/Updated`.
2. Click → consumer lấy danh sách tham số `tracked` của link (cache Redis/LRU theo `link_id`) → `$inc` `stats_param_daily` cho từng `(key, value)`.
3. Admin bật theo dõi tham số mới → job **backfill**: tính `stats_param_daily` từ `clicks` thô trong thời hạn còn giữ (≤ N tháng, M3); số liệu cũ hơn không có.
4. Đổi `long_url` của link → cập nhật `link_params`; click **trước** thời điểm sửa giữ theo tham số cũ (ghi rõ trong tooltip).

**Bảo vệ:**
- **PII:** tham số chứa dữ liệu cá nhân trong URL tracking hiện tại (`phonenumber`, `fullname`, `guestid`, `email`...) đăng ký `pii = hash` (lưu SHA-256 có salt, chỉ tra cứu chính xác) hoặc `drop` (không lưu) — **không bao giờ** lưu bản rõ vào báo cáo. `utm_extra_ctv` chuẩn hoá SĐT, hiển thị che.
- **Bùng nổ giá trị:** tham số có > `max_values` giá trị khác nhau / tài khoản / tháng (vd `gclid`, `fbclid`, token) → tự chuyển `high_cardinality`: chỉ giữ top N + `other` trong stats, vẫn lọc chính xác được qua `link_params`. Mặc định **không theo dõi** `gclid`, `fbclid`, `ttclid`, `_ga`, `zarsrc`.
- Giới hạn: tối đa 50 tham số / link, key ≤ 64 ký tự, value ≤ 256 ký tự (dài hơn → cắt + hash).

**Tham số theo dõi mặc định:** `utm_source`, `utm_medium`, `utm_campaign`, `utm_content`, `utm_term`, `utm_extra_ctv` (D30 chốt danh sách bổ sung với nghiệp vụ).

### 5.5 Danh mục báo cáo

#### R1. Tổng quan hệ thống *(admin)*
- KPI: tổng link, link mới, click, unique, bot, so với kỳ trước (%).
- Timeline click / link mới theo ngày.
- Top 10 tài khoản, top 10 chiến dịch, top 10 CTV, top 10 link.
- Phân rã thiết bị / nguồn / quốc gia; heatmap giờ × thứ.

#### R2. Báo cáo theo tài khoản
- **Danh sách tài khoản** *(admin)*: mỗi dòng `username`, `total_links`, `new_links`, `active_links`, `clicks`, `unique_clicks`, `bot_clicks`, so kỳ trước; sắp xếp, tìm kiếm, phân trang. Thay `report-overview-users`.
- **Chi tiết tài khoản** *(admin hoặc chính chủ)*: KPI, timeline, danh sách chiến dịch của tài khoản, top CTV, top link, phân rã.

#### R3. Báo cáo theo chiến dịch
- **Danh sách chiến dịch** (lọc theo tài khoản, khoảng ngày): `campaign_code`, `name`, `new_links`, `active_links`, `clicks`, `unique_clicks`, số CTV tham gia, ngày bắt đầu / click cuối. Thay `report-total`, `report-total-range`.
- **Chi tiết chiến dịch**: KPI, timeline ngày (thay `report-total-daily`), bảng xếp hạng CTV trong chiến dịch, top link, phân rã thiết bị / nguồn / quốc gia / giờ. Thay `report-click-analytics`.
- **So sánh chiến dịch**: chọn 2–5 chiến dịch, vẽ chồng timeline + bảng chỉ số.

#### R4. Báo cáo theo CTV
- **Bảng xếp hạng CTV** (lọc tài khoản, chiến dịch, ngày): `ctv_id`, tên (nếu có danh mục), `new_links`, `active_links`, `clicks`, `unique_clicks`, `suspicious_clicks`, hạng; export để **đối soát / trả thưởng**.
- **Chi tiết CTV**: KPI, timeline, danh sách link của CTV kèm click, chiến dịch tham gia, cảnh báo bất thường.
- **Tra cứu CTV theo SĐT / hash**: tra index `ctv_id` (< 100ms) thay cho `report-total-detail` quét `LIKE` (~15s); giữ endpoint cũ, đổi phần ruột.
- **CTV chưa định danh**: số link / click không có `ctv_identifier` để nghiệp vụ xử lý.

#### R5. Báo cáo theo link
- Chi tiết link: KPI, timeline, phân rã, 100 click gần nhất.
- Top link theo click / tăng trưởng; link "chết" (không click N ngày).

#### R6. Nhật ký click
- Danh sách click thô theo bộ lọc (tài khoản, chiến dịch, CTV, link, ngày ≤ 3 tháng), phân trang con trỏ. Thay `report-click-list`.
- IP hiển thị dạng che (`113.161.x.x`) với user thường; đầy đủ với admin.

#### R6b. Phân tích lượt click (Click Explorer) — truy vấn linh hoạt

Phục vụ màn hình `PLAN_PORTAL_REPORT.md` §4.1: lọc theo nhiều tiêu chí, nhóm theo 1–3 chiều, trọng tâm theo **tài khoản**.

**Dữ liệu click cần bổ sung lúc ghi** (consumer làm giàu `ClickEvent`, không làm chậm redirect):

| Trường | Nguồn |
|--------|-------|
| `meta.owner`, `meta.campaign_code`, `meta.ctv_id`, `meta.link_id` | Từ link (cache) |
| `link_prefix` (prefix cấp), `access_prefix` (prefix trong URL truy cập) | Link / path redirect |
| `link_api_version` (`v1`/`v2`/`v3`/`portal`), `link_is_custom`, `link_created_at` | Link |
| `dest_host` (lưu trên `links`); **mọi tham số URL** → `link_params` + `stats_param_*` (§5.4b) | Tách từ `long_url` **lúc tạo / sửa link** |
| `device`, `os`, `browser`, `in_app` (Zalo / Facebook / TikTok...) | Parse user-agent (thư viện `uap-go`) |
| `source_group` | `referer_host` → nhóm nguồn theo bảng cấu hình (P-D7) |
| `country`, `province` | GeoIP City (nếu bật) |
| `hour`, `weekday` (giờ VN) | Từ `ts` |
| `is_bot`, `is_suspicious`, `is_repeat` | Luật phát hiện §5.2 |

**Chiến lược truy vấn:**

| Loại truy vấn | Nguồn dữ liệu | Giới hạn |
|---------------|---------------|----------|
| Nhóm theo các chiều **đã tổng hợp sẵn** (tài khoản, chiến dịch, CTV, link, ngày, thiết bị, nguồn, quốc gia, giờ) | `stats_*_daily` mở rộng thêm `by_os`, `by_browser`, `by_source_group`, `by_utm_source`, `by_access_prefix` | ≤ 12 tháng, < 1s |
| Kết hợp tự do nhiều chiều / lọc UTM, OS, khung giờ, IP | Aggregation trên `clicks` time-series (lọc `meta.owner` + `ts` trước, dùng index) | ≤ 3 tháng, timeout 10s, có thể huỷ |
| Facets (giá trị cho dropdown) | Collection `click_facets` cập nhật theo ngày (distinct theo tài khoản) | < 300ms |

Index bổ sung trên `clicks`: `(meta.owner, ts)`, `(meta.campaign_code, ts)`, `(meta.ctv_id, ts)`, `(meta.link_id, ts)`. Nếu khối lượng / độ phức tạp vượt khả năng Mongo (p95 > 5s ở 3 tháng) → chuyển phần truy vấn tự do sang **ClickHouse** (D28).

**Bảo vệ hệ thống:** giới hạn số chiều (≤ 3), số nhóm trả về (top 1000 + `other`), timeout, hàng đợi truy vấn nặng theo người dùng, cache kết quả 60s theo hash tham số.

#### R7. Chất lượng traffic & chống gian lận
- Top IP / tỉ lệ click lặp theo CTV và chiến dịch.
- Danh sách `suspicious_clicks` kèm lý do (IP lặp, bot UA, burst theo giây).
- Tỉ lệ bot theo nguồn.

#### R8. Xuất dữ liệu
- Export CSV / XLSX cho R2, R3, R4, R5, R6 — chạy **job nền**, lưu file tạm (TTL 7 ngày), thông báo khi xong; giới hạn 1 triệu dòng / file.

#### R9. Báo cáo định kỳ *(giai đoạn 2)*
- Gửi email / webhook hằng ngày / tuần: tóm tắt theo tài khoản hoặc chiến dịch.

### 5.6 API báo cáo

- **Giữ nguyên** các endpoint cũ (`/api/v2/report*`, `report-click-*`, `report-overview-users`, `report-total-detail`) với cùng request/response, nhưng đọc từ `stats_*`.
- **Thêm nhóm mới cho đối tác** (REST, GET; gọi bằng **`key` như cũ** — §3.2). Portal **không** dùng các endpoint này: `portal-api` (Project 2) tự truy vấn MongoDB với cùng định nghĩa chỉ số §5.2 và cùng quy tắc phân quyền §5.7 (`PLAN_PORTAL_REPORT.md` §3.1, §4):

| Endpoint | Báo cáo |
|----------|---------|
| `GET /api/v4/reports/overview` | R1 |
| `GET /api/v4/reports/accounts`, `/accounts/{username}` | R2 |
| `GET /api/v4/reports/campaigns`, `/campaigns/{code}`, `/campaigns/compare?codes=` | R3 |
| `GET /api/v4/reports/ctvs`, `/ctvs/{ctv_id}`, `/ctvs/lookup?phone=&hash=`, `/ctvs/unidentified` | R4 |
| `GET /api/v4/reports/links/{code}`, `/links/top` | R5 |
| `GET /api/v4/reports/clicks` | R6 (lọc đủ tiêu chí R6b, phân trang con trỏ) |
| `POST /api/v4/reports/clicks/query` | R6b — body: `filters` (include / exclude từng tiêu chí), `group_by` (1–3 chiều), `metrics`, `granularity`, `compare`, `sort`, `limit` |
| `GET /api/v4/reports/clicks/facets?field=&q=` | R6b — giá trị cho bộ lọc (theo quyền); `field=param.<key>` cho tham số URL |
| `GET /api/v4/reports/params`, `/params/{key}` | Báo cáo theo tham số URL: danh sách giá trị của một key + clicks, unique, links, so kỳ trước; group thêm theo ngày / chiến dịch / CTV (§5.4b) |
| `GET /api/v4/reports/traffic-quality` | R7 |
| `POST /api/v4/exports`, `GET /api/v4/exports/{id}` | R8 (export cho đối tác; export của Portal do `portal-api` làm) |

**Dữ liệu Service phải cung cấp cho Portal** (Portal đọc trực tiếp MongoDB — `PLAN_PORTAL_REPORT.md` §3.1, §7):

| Dữ liệu | Yêu cầu |
|---------|---------|
| `users` | Thêm trường `portal_access` (bool), `role` (`admin` / `user` / `viewer`), `viewer_accounts` (mảng username) — D22; giữ `password_hash` bcrypt |
| `links`, `campaigns`, `prefixes`, `clicks` | Đúng schema §4; `clicks` có đủ trường làm giàu §5.5 R6b |
| `stats_*`, `link_params`, `stats_param_*`, `param_values`, `click_facets`, `ctvs` | Đúng schema §5.4, §5.4b, ghi theo định nghĩa §5.2, trễ < 1 phút |
| `param_registry` | Portal (admin) ghi bật/tắt; job backfill của Service theo dõi thay đổi (change stream hoặc poll), chạy backfill, cập nhật `backfill.{status, progress, from}` |
| Quyền DB | Tạo user MongoDB `portal_api`: chỉ đọc `am_shortlink` + dữ liệu thống kê `am_shortlink_report`; ghi `param_registry` và collection `portal_*` |
| Thay đổi schema | Có version trong `migrations/`; đổi tên / xoá trường, đổi kiểu, đổi index Portal dùng → báo Portal trong MR, giữ tương thích ≥ 1 release |

Tham số chung: `from`, `to` (≤ 12 tháng với dữ liệu tổng hợp, ≤ 3 tháng với click thô), `owner`, `campaign`, `ctv`, `granularity` (`day|week|month`), `compare=previous`, `page`/`cursor`, `sort`.

### 5.7 Phân quyền dữ liệu báo cáo

| Vai trò | Phạm vi |
|---------|---------|
| `admin` | Toàn hệ thống, mọi tài khoản |
| `user` / đối tác API | Chỉ dữ liệu có `owner = chính mình`; tham số `owner`/`creator` khác bị từ chối (sửa D6) |
| `viewer` *(mới, tuỳ chọn)* | Chỉ xem báo cáo của các tài khoản được gán (cho quản lý kinh doanh) |

### 5.8 Hiệu năng & độ chính xác

- Mục tiêu: báo cáo tổng hợp < 500ms, nhật ký click < 1s, tra cứu CTV < 100ms.
- Cache Redis 60s theo hash tham số (giữ hành vi `SETTING_REPORT_CACHE_TTL`); dữ liệu "hôm nay" có thể trễ ≤ 1 phút.
- Consumer idempotent theo `event_id` (không cộng trùng khi replay).
- Job đối soát đêm: so `stats_link_daily` với đếm từ `clicks` cho ngày hôm trước; lệch → tự rebuild + alert.

### 5.9 Màn hình Portal cho báo cáo

Thuộc **Project 2** — xem [`PLAN_PORTAL_REPORT.md`](PLAN_PORTAL_REPORT.md) §4. Portal có backend Go riêng đọc trực tiếp MongoDB; Service cam kết **dữ liệu** đúng hợp đồng schema (§4, §5.4, §5.4b) và độ trễ thống kê < 1 phút (§5.8), không cam kết API cho Portal.

---

## 6. Lộ trình

Giả định: 2–3 dev backend, 1 QA bán thời gian (frontend thuộc Project 2).

| Phase | Nội dung | Thời lượng | Đầu ra / tiêu chí xong |
|-------|----------|-----------|------------------------|
| **0. Khảo sát & chốt** | Viết **OpenAPI spec legacy v1/v2/v3** (§3.1) từ code + golden test; Dump schema prod; thu golden request/response từ access log; chốt các mục ⚠️ (§9); chọn hạ tầng; chốt E1–E12 (`ENV_OBSERVABILITY_PLAN.md`); tạo secret trong Vault; **gỡ `.env.Staging`/`.env.Production` khỏi git + rotate secret**; viết ADR + cập nhật manifest KB | 1–2 tuần | ADR được duyệt, bộ golden test, schema thật |
| **1. Nền tảng Go** | Pipeline OpenAPI (`oapi-codegen`, `spectral`, `oasdiff`, Swagger UI `/docs`); Khung repo, config loader 3 môi trường, log slog + Kafka, OpenTelemetry, middleware (apikey, quota, checkIP, forwardedHost, rate limit), Mongo/Redis client, health check, CI/CD 3 môi trường, Dockerfile, script index, **dựng mới môi trường dev**, docker compose local | 2–3 tuần | Service chạy trên dev + staging, log thấy trên Kibana, trace thấy trên backend OTel, CI xanh |
| **2. Redirect + click pipeline** | Redirect với LRU/Redis/negative cache; ClickEvent → Kafka; consumer ghi click, tổng hợp, `$inc` clicks; tracking TrackingApi/FMI chạy async | 2–3 tuần | Load test đạt G1; mất Redis/Kafka vẫn redirect được |
| **3. API quản lý link** | v1/v2/v3 shorten (+multi, update-multi), delete/restore, search, QR, campaign, template, admin, cache tools; handler sinh từ spec, tài liệu Swagger đầy đủ | 3–4 tuần | Golden test pass cho toàn bộ nhóm |
| **4. Report** (§5) | 4a: collection `stats_*` + consumer fan-out + backfill/rebuild + job đối soát · 4b: endpoint cũ (`report*`, `report-click-*`, `report-overview-users`, `report-total-detail`) đọc từ `stats_*` · 4c: API `/api/v4/reports/*` R1–R7 cho đối tác (§5.6) · 4d: export job (R8) · 4e: job backfill tham số theo `param_registry`. **Hợp đồng schema MongoDB (`migrations/`) phải chốt từ cuối Phase 1** để Portal làm song song trên DB seed | 4–5 tuần | Đối chiếu số liệu khớp MySQL; đạt mục tiêu hiệu năng §5.8 |
| **5. Hỗ trợ Portal** | Bổ sung trường / index theo truy vấn thực tế của `portal-api`; trường `users` cho Portal (`portal_access`, `role`, `viewer_accounts`); user MongoDB `portal_api`; node đọc analytics nếu cần | song song Phase 4–6 | Portal (Project 2) UAT đạt trên staging |
| **6. Migrate & cutover** | Backfill; CDC binlog → Mongo; shadow traffic; canary redirect → API theo nhóm | 2–3 tuần | Mục K, O trong checklist đạt |
| **7. Ổn định** | Theo dõi, alert, runbook; MySQL read-only N tuần rồi tắt PHP | 2–4 tuần | Không sự cố P1, tắt hệ cũ |

**Tổng ước lượng:** ~5 tháng tới khi tắt hệ PHP (R9 báo cáo định kỳ làm sau).

### Thứ tự cutover
1. Redirect (canary 5% → 50% → 100%, có shadow ≥ 1 tuần trước đó) — chuyển **cả `/sale/*` và `/lm/*`** cùng lúc (chung không gian mã), kiểm cả `fpt.vn` và `staging.fpt.vn`
2. Shorten (v3 → v2 → v1)
3. Delete / restore / search / QR / campaign / template
4. Report
5. Báo cáo → Portal (Project 2); tắt `/report-chart`, `/report-users`, Blade

---

## 7. Chiến lược migrate dữ liệu

> Chi tiết đầy đủ (mapping từng cột, profiling, công cụ, CDC, đối chiếu, runbook, rollback, quyết định M1–M11): **[`DATA_MIGRATION_PLAN.md`](DATA_MIGRATION_PLAN.md)**. Tóm tắt:

1. **Tạo collection + index trước** (unique `code`) để phát hiện trùng ngay khi backfill.
2. **Backfill** bằng `cmd/migrate`: đọc MySQL theo `id` từng lô (keyset, song song theo dải id), bulk upsert theo `_id` = id cũ (chạy lại được).
   - `links`, `users`, `campaigns`, `domains`, `templates`: đầy đủ.
   - `clicks`: raw N tháng gần nhất (theo retention chốt ở Phase 0); dữ liệu cũ hơn chỉ chuyển dạng tổng hợp ngày.
3. **Đồng bộ liên tục** bằng CDC từ binlog (Debezium hoặc `go-mysql`) — không phải sửa code PHP.
4. **Đối chiếu hằng ngày**: số link, số click theo link/ngày, mẫu ngẫu nhiên 1.000 link so từng trường.
5. **Một nguồn sinh mã**: trong giai đoạn song song, chỉ hệ đang nhận traffic shorten được sinh mã mới.
6. **Rollback**: giữ MySQL nhận ghi qua đồng bộ ngược (hoặc dual-write) trong 2–4 tuần sau cutover.

---

## 8. Kiểm thử & nghiệm thu

| Loại | Cách làm |
|------|----------|
| Unit | Rule sinh mã, validate ending, dedupe, prefix v3, device detect, quota |
| Integration | testcontainers Mongo/Redis/Kafka cho repository, consumer, middleware |
| Contract / golden | Replay ≥ 10.000 request thật vào PHP và Go, diff JSON (bỏ qua trường thời gian/mã random) |
| Số liệu | So report PHP vs Go trên ≥ 20 user/campaign thật |
| Load | k6: redirect, shorten đơn, shorten-multi 1000 url, report |
| Chaos | Tắt Redis / Kafka / 1 node Mongo trong lúc chạy tải |
| Contract với Portal | Schema MongoDB theo `migrations/` có version; test tích hợp của `portal-api` chạy trên `migrations/` của Service; Portal chạy e2e trên staging (Project 2) |

Nghiệm thu cuối cùng = toàn bộ [`REWRITE_CHECKLIST.md`](REWRITE_CHECKLIST.md) được tick.

---

## 9. Quyết định cần chốt ở Phase 0

Các hành vi hiện tại là bug/rủi ro — phải chốt **giữ** hay **sửa**, ghi vào ADR:

| # | Hành vi hiện tại | Đề xuất |
|---|------------------|---------|
| D2 | Tracking TrackingApi/FMI chạy đồng bộ trên redirect (timeout tới 30s); user `lehongan` hard-code | Async qua consumer; danh sách user thành cấu hình |
| D3 | `expires_at` lưu nhưng redirect không kiểm | Hỏi nghiệp vụ; mặc định giữ nguyên (không chặn) |
| D4 | v2 trả link của user khác khi `custom_ending` đã có chủ | Chặn như v3 |
| D5 | Dedupe bằng crc32 (có thể va chạm) | Hash mạnh + so `long_url` |
| D6 | `report-click-*`, `report-overview-users`, `update-links-ctv-identifier` nhận `creator` từ request | Chỉ admin được chỉ định creator khác |
| D7 | Mọi API key gọi được `cache-clear` toàn bộ | Chỉ admin; xoá theo prefix |
| D8 | Rate limit IP không chặn (code bị comment) | Bật thật, ngưỡng chốt theo traffic |
| D9 | Lỗi non-local trả kèm stack trace | Thông báo chung + log chi tiết |
| D10 | Kiểm campaign ở `shorten-multi` sai logic | Kiểm đúng quyền sở hữu |
| D11 | `CheckForwardedHost` gộp mảng bằng `+` làm bỏ sót domain DB | Gộp đúng |
| D12 | `POST /api/v2/report` khai báo 2 handler | Xác định handler đang chạy trên prod, giữ đúng nó |
| D13 | Click bot vẫn được cộng | Hỏi nghiệp vụ; có thể lưu nhưng tách số |
| D14 | Quy tắc campaign code không thống nhất (create ≤10, shorten 5–40) | Thống nhất một quy tắc |
| D15 | Retention & PII của click (IP, user-agent giữ vô thời hạn) | Chốt thời gian lưu, cân nhắc mask IP |
| D16 | Có bắt buộc tương thích 100% API v1/v2/v3 hay cho phép v4 mới + lớp tương thích | Giữ tương thích 100% ở giai đoạn này |
| D17 | Định nghĩa `unique_clicks` (IP+UA theo ngày?) và ngưỡng `suspicious_clicks` | Như §5.2, nghiệp vụ duyệt |
| D18 | Click bot / nghi vấn có được tính vào số liệu đối soát trả thưởng CTV không | Không tính; hiển thị riêng |
| D19 | Có danh mục CTV (tên, SĐT, tài khoản quản lý) ở hệ nào để import không | Tạo `ctvs` tuỳ chọn, import nếu có nguồn |
| D20 | Chuẩn hoá `ctv_identifier` (SĐT `+84`/`84`/`0`, hash) và backfill cho link cũ | Chuẩn hoá về `0xxxxxxxxx`, backfill khi migrate |
| D21 | Thời gian giữ dữ liệu tổng hợp vs click thô | Tổng hợp giữ vĩnh viễn; click thô theo D15 |
| D22 | Có cần vai trò `viewer` cho quản lý kinh doanh | Có — Service thêm `role = viewer` + `viewer_accounts` trong `users`; `portal-api` áp quyền |
| D23 | Mã tạo ở `/lm` hiện mở được cả qua `/sale` (và ngược lại) vì dùng chung không gian mã. Giữ hay ràng buộc mã chỉ chạy đúng prefix đã cấp? | **Giữ chung** (an toàn, không làm hỏng link đã phát hành; link cũ không biết prefix). Chỉ ghi nhận prefix truy cập vào click để báo cáo |
| D24 | Ghi nhận prefix thực tế lúc click (`clicks.prefix`) để biết traffic `/sale` vs `/lm` | Có — redirect-svc tự đọc từ path |
| D25 | Đối tác mới cần prefix riêng (ngoài `sale`, `lm`) | Thêm vào `prefixes` + gán `users.prefix`, không cần deploy |
| D28 | Click Explorer (R6b) chạy trên MongoDB hay cần ClickHouse cho truy vấn tự do | Bắt đầu MongoDB (stats mở rộng + aggregation có giới hạn); đo p95 ở staging dữ liệu thật, vượt 5s → ClickHouse |
| D30 | Danh sách tham số URL theo dõi mặc định & tham số PII (`phonenumber`, `fullname`, `guestid`...) xử lý `hash` hay `drop` | Theo dõi `utm_*`, `utm_extra_ctv`; PII = `drop` (riêng tra cứu cần thiết → `hash`); nghiệp vụ duyệt |
| D31 | Tách database báo cáo `am_shortlink_report` khỏi `am_shortlink` — cùng cụm hay cụm riêng | Cùng cụm giai đoạn đầu, tách cụm khi tải báo cáo ảnh hưởng redirect |
| D29 | Lưu sẵn `utm_*`, `dest_host` trên link và `os`, `browser`, `province` trên click — có cần GeoIP City (tỉnh/thành) không | Có lưu; GeoIP City nếu có license MaxMind City |
| D27 | Swagger UI ở production: tắt hẳn, chỉ IP nội bộ, hay công khai cho đối tác | Tắt `/docs` trên prod; publish bản HTML (Redoc) riêng cho đối tác |
| D26 | Môi trường, log Kafka, OpenTelemetry | Theo E1–E12 trong [`ENV_OBSERVABILITY_PLAN.md`](ENV_OBSERVABILITY_PLAN.md) §6 |

---

## 10. Rủi ro

| Rủi ro | Mức | Giảm thiểu |
|--------|-----|-----------|
| Client phụ thuộc chi tiết response cũ (chuỗi lỗi, HTTP 200 khi lỗi, `ending` là full URL ở v1) | Cao | Golden test từ traffic thật; giữ nguyên định dạng |
| Lệch số click khi chuyển hệ | Cao | CDC + đối chiếu hằng ngày; event id idempotent |
| Trùng mã ngắn giữa 2 hệ | Trung bình | Một nguồn sinh mã tại một thời điểm |
| Phân biệt hoa/thường mã ngắn khác MySQL collation | Trung bình | Kiểm collation prod ở Phase 0, chuẩn hoá trước backfill |
| Collection click phình to | Trung bình | Time-series + TTL + tổng hợp |
| Report ad-hoc khó trên Mongo | Thấp | Thiết kế bảng tổng hợp theo truy vấn; ClickHouse sau nếu cần |
| Thiếu người am hiểu Go/Mongo | Trung bình | Pair programming, code review chặt, tài liệu ADR |

---

## 11. Vai trò

| Vai trò | Trách nhiệm |
|---------|-------------|
| Tech lead | Kiến trúc, ADR, review, chốt các mục §9 |
| Backend (2–3) | Go services, migrate tool, CDC |
| QA | Golden test, đối chiếu số liệu, UAT |
| Ops | Mongo/Redis/Kafka, CI/CD, monitoring, cutover |
| PO / nghiệp vụ | Chốt D3, D13, D15, D17–D25 (định nghĩa chỉ số, CTV, prefix); nghiệm thu số liệu báo cáo |

---

## 12. Việc tiếp theo

- [ ] Duyệt kế hoạch này và trả lời các câu hỏi ở §9
- [ ] Viết ADR "Rewrite sang Go + MongoDB + Next.js" trong `am-shortlink-knowledge/10-adr/`
- [ ] Thêm bounded-context/feature cho hệ mới vào `am-shortlink-knowledge/_meta/manifest.yml`
- [ ] Lấy access log 7 ngày để dựng bộ golden test
- [ ] Dump schema prod (chỉ cấu trúc, không dữ liệu)
- [ ] Tạo repo `am-shortlink-service`
- [ ] Viết OpenAPI spec cho API legacy v1/v2/v3 (dựa trên collection Insomnia + code PHP)
- [ ] Viết OpenAPI spec v4 (báo cáo cho đối tác)
- [ ] Chốt hợp đồng schema MongoDB (`migrations/` có version) với Project 2 — Portal đọc trực tiếp
- [ ] Xin hạ tầng cho **môi trường dev mới** (namespace K8s, Mongo, Redis, topic Kafka `dev-`, domain) và staging cho hệ mới
- [ ] Gỡ `.env.Staging`, `.env.Production` khỏi git repo hiện tại + rotate secret (E5 — làm ngay, không chờ hệ mới)
