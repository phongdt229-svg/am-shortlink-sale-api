# Kế hoạch convert dữ liệu MySQL → MongoDB

> **Trạng thái:** Đề xuất (draft) · **Ngày lập:** 2026-10-06
> **Thuộc dự án:** Project 1 [`PLAN_SERVICE_API.md`](PLAN_SERVICE_API.md) (§4 thiết kế dữ liệu, §5 báo cáo, §7 tóm tắt migrate) · tổng quan [`REWRITE_PLAN.md`](REWRITE_PLAN.md)
> **Checklist nghiệm thu:** [`REWRITE_CHECKLIST.md`](REWRITE_CHECKLIST.md) mục K, G2
> **Nguồn:** schema `polr` (MySQL/MariaDB) · **Đích:** MongoDB 7+ replica set

---

## 1. Mục tiêu & nguyên tắc

| # | Mục tiêu | Tiêu chí |
|---|----------|----------|
| M-G1 | Không mất dữ liệu | Số bản ghi, tổng click theo link/ngày khớp 100% |
| M-G2 | Không downtime redirect | Chuyển bằng CDC + canary, không dừng hệ |
| M-G3 | Chạy lại được, dừng / tiếp được | Mọi bước idempotent (upsert theo khoá), có checkpoint |
| M-G4 | Không làm sập MySQL production | Đọc từ **replica**, giới hạn tốc độ |
| M-G5 | Rollback được trong 2–4 tuần sau cutover | Đồng bộ ngược Mongo → MySQL cho dữ liệu tạo mới |

Nguyên tắc:
- **Snapshot + CDC**: ghi lại vị trí binlog/GTID **trước** khi backfill, backfill xong thì phát lại binlog từ vị trí đó → không có khe hở dữ liệu.
- **Upsert theo khoá ổn định** (`_id` = id cũ) → chạy lại không sinh bản ghi trùng.
- **Không sửa dữ liệu nguồn**; mọi chuẩn hoá làm ở bước transform, bản ghi lỗi đưa vào `migration_errors`.

---

## 2. Kiểm kê nguồn

| Bảng MySQL | Khoá | Ghi chú | Collection đích |
|------------|------|---------|-----------------|
| `links` | `id` int AI | ~30 triệu; unique `short_url`; unique `(creator, long_url_hash)` theo code; nhiều cột không có migration (`domain_id`, `is_deleted`, `campaign_id`, `campaign_code`, `expires_at`); `ctv_identifier` (2026-08) | `links` |
| `clicks` | `id` int/bigint AI | Lớn nhất; FK `link_id → links.id` cascade; có `device` (2026-06) | `clicks` (time-series) + `stats_*` |
| `users` | `id` int AI | `username` unique; `api_key`, `api_quota` (string), `active` (string), `prefix` (2026-09), `random_key_length`, `is_expires`, `expires_value`, `deleted_at` | `users` + `api_keys` |
| `campaigns` | `id` | Không có migration; `name`, `code`, `created_by` (= `users.id`) | `campaigns` |
| `domains` | `id` | `domain_name`, `is_active` | `domains` |
| `templates` | `template_id` | `template_name`, `template_url`, `template_images` | `templates` |

### Đo khối lượng (chạy trên replica ở Phase 0)

```sql
-- Kích thước & số dòng ước lượng
SELECT table_name, table_rows,
       ROUND((data_length + index_length)/1024/1024/1024, 2) AS size_gb
FROM information_schema.tables
WHERE table_schema = 'polr' ORDER BY size_gb DESC;

-- Số dòng chính xác + phân bố theo tháng của clicks (quyết định retention)
SELECT DATE_FORMAT(created_at, '%Y-%m') ym, COUNT(*) FROM clicks GROUP BY ym ORDER BY ym;
SELECT MIN(id), MAX(id), COUNT(*) FROM links;

-- Điều kiện cho CDC
SHOW VARIABLES WHERE Variable_name IN
  ('binlog_format','binlog_row_image','gtid_mode','log_bin','expire_logs_days','binlog_expire_logs_seconds',
   'time_zone','system_time_zone','character_set_database','collation_database');
SHOW FULL COLUMNS FROM links;   -- collation cột short_url (phân biệt hoa/thường?)
```

Điền kết quả vào bảng sau trước khi ước lượng thời gian:

| Bảng | Số dòng | Dung lượng | Ghi chú |
|------|---------|-----------|---------|
| links | _TODO_ | _TODO_ | |
| clicks | _TODO_ | _TODO_ | theo tháng: _TODO_ |
| users | _TODO_ | | |
| campaigns / domains / templates | _TODO_ | | |

---

## 3. Profiling chất lượng dữ liệu (Phase 0)

Chạy từng truy vấn, ghi số lượng và quyết định xử lý vào §10:

```sql
-- 1. Trùng short_url khi so không phân biệt hoa/thường (Mongo mặc định phân biệt)
SELECT LOWER(short_url) s, COUNT(*) c FROM links GROUP BY s HAVING c > 1 LIMIT 100;

-- 2. short_url chứa ký tự ngoài regex hiện tại / khoảng trắng / rỗng
SELECT COUNT(*) FROM links WHERE short_url NOT REGEXP '^[a-zA-Z0-9._+-]+$' OR short_url = '';

-- 3. Ngày không hợp lệ
SELECT COUNT(*) FROM links WHERE created_at IS NULL OR created_at = '0000-00-00 00:00:00';
SELECT COUNT(*) FROM links WHERE expires_at = '' OR expires_at = '0000-00-00';

-- 4. Link mồ côi người tạo
SELECT COUNT(*) FROM links l LEFT JOIN users u ON u.username = l.creator WHERE u.id IS NULL;

-- 5. Click mồ côi link (không nên có vì FK cascade)
SELECT COUNT(*) FROM clicks c LEFT JOIN links l ON l.id = c.link_id WHERE l.id IS NULL;

-- 6. Campaign code trên link không có trong bảng campaigns
SELECT COUNT(DISTINCT campaign_code) FROM links l
WHERE campaign_code <> '' AND NOT EXISTS (SELECT 1 FROM campaigns c WHERE c.code = l.campaign_code);

-- 7. Link đã xoá có long_url bị đổi đuôi "_del"
SELECT COUNT(*) FROM links WHERE long_url REGEXP '_del[A-Za-z0-9]{3}$';

-- 8. Cặp cờ trạng thái
SELECT is_deleted, is_disabled, COUNT(*) FROM links GROUP BY is_deleted, is_disabled;

-- 9. ctv_identifier: dạng SĐT / hash / rỗng; link chưa backfill nhưng long_url có utm_extra_ctv
SELECT COUNT(*) FROM links WHERE ctv_identifier IS NULL AND long_url LIKE '%utm_extra_ctv=%';

-- 10. Lỗi encoding (chuỗi double-encoded UTF-8)
SELECT COUNT(*) FROM links WHERE long_url REGEXP 'Ã|Â|Æ°';

-- 11. Giá trị kiểu chuỗi cần ép kiểu
SELECT DISTINCT active, api_active FROM users;
SELECT api_quota FROM users WHERE api_quota NOT REGEXP '^-?[0-9]+$';

-- 12. Mã ngắn trùng nhau giữa các domain (nếu sau này dùng (domain_id, code))
SELECT short_url, COUNT(DISTINCT domain_id) FROM links GROUP BY short_url HAVING COUNT(DISTINCT domain_id) > 1 LIMIT 10;

-- 13. Prefix: giá trị users.prefix đang dùng, và số link theo user có prefix (ước lượng link /lm)
SELECT prefix, COUNT(*) FROM users GROUP BY prefix;
SELECT u.prefix, COUNT(*) FROM links l JOIN users u ON u.username = l.creator GROUP BY u.prefix;
```

---

## 4. Mapping dữ liệu

### 4.1 Quy tắc chung

| Vấn đề | Quy tắc |
|--------|---------|
| Khoá chính | `links`, `users`, `campaigns`, `domains`, `templates`: **`_id` = id cũ (int64)**; bản ghi mới cấp id từ collection `counters` (cấp theo block, bắt đầu từ `MAX(id) + 1.000.000`). `clicks`: `_id` ObjectId, giữ `legacy_id` |
| Thời gian | MySQL `DATETIME` không có múi giờ → hiểu theo múi giờ ghi vào (xác nhận ở Phase 0, giả định `Asia/Ho_Chi_Minh`) → lưu Mongo `Date` (UTC) |
| Ngày rỗng / `0000-00-00` | → `null` |
| Boolean dạng chuỗi/tinyint | `'1'`/`1` → `true`, còn lại → `false` |
| Số dạng chuỗi | ép `int`; không ép được → giá trị mặc định + ghi `migration_errors` |
| Chuỗi | `trim`, sửa double-encoding nếu profiling phát hiện; giữ nguyên `long_url` (không encode lại) |
| Trường mới | Thêm `migrated_at`, `source: "mysql"` để truy vết |

### 4.2 `links` → `links`

| MySQL | Mongo | Transform |
|-------|-------|-----------|
| `id` | `_id` | giữ nguyên |
| `short_url` | `code` | `trim`; nếu chọn không phân biệt hoa/thường thì thêm `code_lower` |
| `long_url` | `long_url` | giữ nguyên |
| `long_url_hash` | `legacy_crc32` | giữ để đối chiếu |
| — | `long_url_hash` | `sha256(long_url)` (hex 16 ký tự đầu hoặc đầy đủ) |
| `creator` | `owner_username` | giữ |
| — | `owner_id` | tra `users.username → id` (cache trong RAM ~ số user) ; không thấy → `null` + ghi lỗi mức warning |
| `ip` | `creator_ip` | giữ (hoặc mask theo D15) |
| `clicks` | `clicks` | giữ, đối chiếu lại với số đếm `clicks` sau migrate |
| `is_deleted`, `is_disabled` | `status` | `is_deleted=1 → "deleted"`; `is_disabled=1 → "disabled"`; còn lại `"active"` |
| `is_custom`, `is_api` | `is_custom`, `is_api` | bool |
| `secret_key` | `secret_key` | giữ (route secret đang tắt) |
| `domain_id` | `domain_id` | int hoặc `null` |
| `campaign_id`, `campaign_code` | `campaign_id`, `campaign_code` | `''` → `null` |
| `ctv_identifier` | `ctv_raw`, `ctv_id` | `ctv_raw` giữ nguyên; `ctv_id` = chuẩn hoá (SĐT `+84`/`84` → `0…`, bỏ khoảng trắng/dấu chấm); nếu rỗng mà `long_url` có `utm_extra_ctv` → tách ra |
| `expires_at` | `expires_at` | `''`/`0000-00-00` → `null` |
| — | `prefix` | DB cũ **không lưu** prefix đã cấp → suy ra theo M11 từ `users.prefix` của người tạo (`sale` / `lm`), `NULL` → `sale`; thêm `prefix_inferred: true` để phân biệt với link mới. Redirect **không phụ thuộc** trường này (không gian mã chung) |
| `created_at`, `updated_at` | `created_at`, `updated_at` | chuyển múi giờ |

Index tạo **trước** khi nạp: unique `code`. Index phụ tạo **sau** khi nạp xong (nhanh hơn): `(owner_username, long_url_hash)`, `(owner_username, created_at)`, `(owner_username, campaign_code, created_at)`, `(ctv_id, created_at)`, `status`.

### 4.3 `users` → `users` + `api_keys`

| MySQL | Mongo | Transform |
|-------|-------|-----------|
| `id` | `_id` | giữ |
| `username`, `email` | giữ | `trim`; email lowercase |
| `password` | `password_hash` | giữ bcrypt; kiểm tra Go đọc được tiền tố `$2y$` |
| `role` | `role` | `'admin'` → `admin`; `''` → `user` |
| `active`, `api_active` | bool | |
| `api_quota` | int | `< 0` = không giới hạn |
| `api_key` | → `api_keys` | `{user_id, key_hash: sha256(key), key_last4, active: api_active, created_at}`; **không** lưu bản rõ (xem M5) |
| `recovery_key` | bỏ | tính năng reset mật khẩu đang tắt |
| `ip` | `signup_ip` | giữ / mask |
| `prefix`, `random_key_length`, `is_expires`, `expires_value` | giữ | ép kiểu; `prefix` hiện chỉ có `sale` / `lm` — chuẩn hoá `trim` + bỏ `/`, rỗng → `null`; giá trị khác 2 giá trị này → ghi `migration_errors` |
| `deleted_at` | `deleted_at` | user đã xoá mềm vẫn migrate (link còn tham chiếu) |

### 4.4 `campaigns`, `domains`, `templates`

- `campaigns`: `_id`=id, `name`, `code`, `created_by` (user id), timestamps. Tạo thêm bản ghi campaign "ảo" cho các `campaign_code` có trên link nhưng không có trong bảng (cờ `virtual: true`) để báo cáo theo chiến dịch không mất nhóm.
- `domains`: `_id`=id, `domain_name`, `is_active` bool.
- `prefixes` *(mới, không có bảng nguồn)*: seed đúng 2 bản ghi `{_id:"sale", is_default:true}`, `{_id:"lm", is_default_v3:true}` (khớp giá trị đang có trong `users.prefix`).
- `templates`: `_id`=`template_id`, `name`, `url`, `images` (giữ chuỗi gốc; nếu là JSON thì parse thành mảng).

### 4.5 `clicks` → `clicks` (time-series) + `stats_*`

| MySQL | Mongo | Transform |
|-------|-------|-----------|
| `id` | `legacy_id` | |
| `created_at` | `ts` | chuyển múi giờ |
| `link_id` | `meta.link_id` | |
| — | `meta.owner`, `meta.campaign_code`, `meta.ctv_id` | **denormalize** từ link (tra trong RAM/Redis theo `link_id`) |
| `ip` | `ip` | giữ / mask |
| — | `visitor_hash` | `sha1(ip + user_agent)` |
| `country` | `country` | `''` → `null` |
| `device` | `device` | nếu `null` → tính lại bằng regex DeviceHelper |
| — | `is_bot` | `device == "bot"` |
| `referer`, `referer_host` | giữ | |
| `user_agent` | giữ | |

**Chiến lược theo tuổi dữ liệu** (chốt N ở M3):
- Click **≤ N tháng**: nạp thô vào `clicks` + tính `stats_*`.
- Click **> N tháng**: **không** nạp thô; tổng hợp trực tiếp bằng SQL rồi ghi vào `stats_link_daily`, sau đó rollup lên campaign/ctv/owner/system.

```sql
-- Tổng hợp click cũ theo link/ngày (chạy theo từng tháng, trên replica)
SELECT link_id, DATE(created_at) d,
       SUM(device <> 'bot' OR device IS NULL) clicks,
       SUM(device = 'bot') bot_clicks,
       COUNT(DISTINCT CONCAT(ip, '|', COALESCE(user_agent, ''))) unique_clicks
FROM clicks
WHERE created_at >= ? AND created_at < ?
GROUP BY link_id, d;
```

Sau khi nạp: rebuild toàn bộ `stats_*` bằng `cmd/migrate rebuild-stats --from --to` (dùng chung code với job đối soát §5.8 của plan).

### 4.6 Tham số URL → `am_shortlink_report.link_params` + `stats_param_*`

- Parse query string `long_url` của **toàn bộ link đã migrate** (`migrate backfill-params`, song song theo dải id) → `link_params` (chuẩn hoá theo `param_registry`; PII theo D30: `hash` / `drop`).
- Seed `param_registry` với tham số mặc định (`utm_*`, `utm_extra_ctv`) + chạy thống kê **mọi key đã xuất hiện** (số link, số giá trị) để nghiệp vụ chọn thêm.
- `stats_param_daily`: tính từ click thô ≤ N tháng; click cũ hơn tổng hợp bằng SQL `JOIN` theo `link_id` với bảng tạm `link_params` (chỉ các key `tracked`).
- Link có `long_url` đuôi `_delXXX` (M7): parse từ `original_long_url`.
- Đối chiếu: tổng `stats_param_daily` của một key (gồm nhóm "không có tham số") = tổng click cùng phạm vi.

---

## 5. Công cụ: `cmd/migrate` (Go)

```
migrate profile                         # chạy các truy vấn §3, xuất báo cáo
migrate snapshot-position               # ghi GTID/binlog hiện tại vào migration_state
migrate backfill --table=links --workers=8 --batch=5000 [--from-id --to-id]
migrate backfill-clicks --since=2025-10-01 --workers=8
migrate aggregate-old-clicks --until=2025-10-01
migrate rebuild-stats --from=... --to=...
migrate backfill-params [--key=utm_source]  # parse long_url -> link_params, tính stats_param_* (§4.6)
migrate verify --table=links [--sample=1000]
migrate cdc --from-position=...         # phát lại binlog -> Mongo (nếu không dùng Debezium)
migrate reverse-sync                    # Mongo change stream -> MySQL (giai đoạn rollback)
```

Thiết kế:
- **Đọc theo keyset** (`WHERE id > ? ORDER BY id LIMIT ?`), không dùng `OFFSET`.
- **Song song theo dải id**: chia `[MIN(id), MAX(id)]` thành N dải, mỗi worker 1 dải.
- **Ghi**: `bulkWrite` **unordered**, `ReplaceOne(upsert)` theo `_id` (clicks: `insertMany` theo dải + kiểm `legacy_id` khi chạy lại dải).
- **Checkpoint** mỗi batch vào `migration_state {table, range, last_id, updated_at}` → dừng / tiếp được.
- **Lỗi từng dòng** → `migration_errors {table, legacy_id, reason, raw}`, không dừng cả job.
- **Throttle**: giới hạn rows/s đọc MySQL và theo dõi replication lag của replica; tự giảm tốc khi lag > 30s.
- **Write concern** `w:1` khi backfill (nhanh), chuyển `majority` khi chạy CDC / production.
- **Metrics**: rows/s, lỗi, lag, ETA (Prometheus / log).

---

## 6. Đồng bộ liên tục (CDC)

Cần cho giai đoạn hai hệ chạy song song (PHP vẫn ghi MySQL).

| Phương án | Ưu | Nhược |
|-----------|----|-------|
| **A. Debezium MySQL → Kafka → Go sink** *(khuyến nghị)* | Đã có Kafka; chuẩn, tin cậy; replay được; sink dùng chung transform với backfill | Thêm Kafka Connect |
| B. `go-mysql/canal` trong `cmd/migrate cdc` | Ít thành phần | Tự lo checkpoint, xử lý DDL |
| C. Dual-write trong code PHP | Không cần binlog | Sửa code PHP cũ, dễ lệch khi lỗi giữa chừng |

Yêu cầu MySQL: `binlog_format=ROW`, `binlog_row_image=FULL`, GTID bật (khuyến nghị), giữ binlog ≥ 7 ngày (đủ thời gian backfill + dự phòng), user có quyền `REPLICATION SLAVE, REPLICATION CLIENT, SELECT`.

Xử lý sự kiện:
- `links` insert/update → upsert theo `_id`; update `is_deleted`/`is_disabled`/`long_url` → cập nhật `status`/`long_url` **và xoá cache redirect** bên Go.
- `links` delete (thật) → đánh dấu `status: "deleted"` (không xoá click).
- `clicks` insert → chuyển thành `ClickEvent` (event_id = `mysql-click-{id}`) đẩy vào luồng consumer → ghi `clicks` + `$inc` `stats_*` (idempotent theo event_id).
- `users`, `campaigns`, `domains`, `templates` → upsert.
- Sự kiện có vị trí cũ hơn snapshot được bỏ qua an toàn nhờ upsert.

Mục tiêu độ trễ CDC: < 5 giây (alert khi > 60 giây).

---

## 7. Thứ tự thực hiện

| Bước | Việc | Ghi chú |
|------|------|---------|
| 1 | Bật binlog ROW/GTID, tạo user CDC, dựng replica đọc | Ops, làm sớm |
| 2 | `profile` + chốt quyết định §10 | |
| 3 | Tạo collection, validator, **unique index** | Index phụ để sau |
| 4 | `snapshot-position` → khởi động CDC ở chế độ **đệm** (ghi vào Kafka, chưa apply) | Không có khe hở |
| 5 | Backfill bảng nhỏ: `domains`, `templates`, `users` + `api_keys`, `campaigns` | Vài phút |
| 6 | Backfill `links` (song song theo dải id) | |
| 7 | Tạo index phụ của `links` | |
| 8 | Backfill click ≤ N tháng; tổng hợp click > N tháng | Bước dài nhất |
| 9 | `rebuild-stats` toàn bộ | |
| 10 | Bật apply CDC từ vị trí snapshot, chờ đuổi kịp (lag < 5s) | |
| 11 | `verify` đầy đủ (§8) | Phải đạt 100% |
| 12 | Shadow / canary theo `PLAN_SERVICE_API.md` §6 | |
| 13 | Cutover ghi sang Go; bật `reverse-sync` | §9 |
| 14 | Sau 2–4 tuần ổn định: tắt reverse-sync, MySQL read-only → lưu trữ | |

**Ước lượng thời gian** (cập nhật sau dry-run): `links` 30 triệu với ~15–20k docs/s ≈ 30–40 phút; `clicks` thô ~30–50k docs/s → tính theo số dòng đo ở §2; tổng hợp click cũ chạy song song theo tháng.

---

## 8. Kiểm tra đối chiếu

| Cấp | Cách kiểm | Ngưỡng đạt |
|-----|-----------|-----------|
| Số lượng | `COUNT(*)` từng bảng vs `countDocuments` (theo `status` với links) | Khớp 100% |
| Theo dải id | Mỗi dải 10.000 id: so `COUNT` + checksum (`CRC32` của chuỗi ghép các trường chính) MySQL vs Go tính trên Mongo | 100% dải khớp |
| Mẫu sâu | 1.000 link + 1.000 user ngẫu nhiên: so từng trường sau transform | 0 lệch ngoài quy tắc |
| Click | Tổng click theo `(link_id, ngày)` cho N tháng gần nhất: `clicks` thô và `stats_link_daily` vs MySQL | Khớp 100% |
| `links.clicks` | So với tổng `stats_link_daily` (lưu ý số cũ có thể đã lệch sẵn) | Lệch được giải thích |
| Nghiệp vụ | Chạy báo cáo cũ (`report-total`, `report-overview-users`, `report-total-detail`, `report-click-analytics`) trên PHP và Go cho ≥ 20 tài khoản / chiến dịch / CTV | Khớp |
| Redirect | 100.000 mã ngẫu nhiên (gồm đã xoá, đã tắt, custom, có ký tự đặc biệt), **mỗi mã thử qua cả `/sale/` và `/lm/`**: PHP và Go trả cùng status + `Location` | 100% |
| Prefix | `users.prefix` chỉ gồm `sale` / `lm` / `null`; số link theo `prefix` khớp truy vấn profiling #13 | Khớp |
| Tham chiếu | Link không có owner, campaign ảo, click mồ côi | Đúng số trong profiling |

Mọi kết quả lưu vào `migration_reports` để làm bằng chứng nghiệm thu.

---

## 9. Cutover & rollback

### Runbook (rút gọn)

| Thời điểm | Việc |
|-----------|------|
| T-14 ngày | Dry-run toàn bộ trên staging với bản sao prod; đo thời gian; sửa lỗi |
| T-7 | Backfill thật + CDC chạy liên tục; verify hằng ngày |
| T-3 | Shadow traffic redirect; freeze thay đổi schema MySQL |
| T-0 | Canary redirect 5% → 50% → 100% (đọc Mongo); **ghi vẫn ở PHP/MySQL** |
| T+3 | Chuyển ghi (shorten/delete/restore) sang Go — dừng tạo link ở PHP trong vài phút, chờ CDC lag = 0, đổi route; Go sinh id từ `counters` (> MAX cũ) nên không đụng id cũ |
| T+3 | Bật `reverse-sync` Mongo → MySQL cho link/user/campaign mới và click |
| T+7…14 | Chuyển report, admin → Portal |
| T+14…30 | Theo dõi; tắt reverse-sync; MySQL read-only; backup lưu trữ |

### Rollback
- **Trước khi chuyển ghi**: chỉ cần trả route redirect về PHP (MySQL vẫn là nguồn chính).
- **Sau khi chuyển ghi**: nhờ `reverse-sync`, MySQL có đủ link/click mới → trả route về PHP, tắt Go. Mã sinh bởi Go dùng cùng regex, id lớn hơn MAX cũ nên PHP đọc được bình thường.
- Điều kiện kích hoạt rollback: lỗi redirect > 0,1%, lệch số liệu không giải thích được, p99 vượt ngưỡng 15 phút liên tục.

---

## 10. Quyết định cần chốt

| # | Câu hỏi | Đề xuất |
|---|---------|---------|
| M1 | `_id` dùng id cũ (int64) hay ObjectId + `legacy_id` | Id cũ int64 cho links/users/campaigns/domains/templates; ObjectId cho clicks |
| M2 | MySQL lưu thời gian theo múi giờ nào | Xác nhận bằng `time_zone` + so mẫu; giả định Asia/Ho_Chi_Minh |
| M3 | Giữ click thô bao nhiêu tháng (N) | 13 tháng (đủ so cùng kỳ năm trước); cũ hơn chỉ giữ tổng hợp |
| M4 | Mã ngắn phân biệt hoa/thường? | Theo collation hiện tại; nếu MySQL `_ci` thì Go tra theo `code_lower` |
| M5 | Hash API key (không xem lại được bản rõ) | Hash; Portal chỉ hiển thị 4 ký tự cuối, muốn xem → sinh key mới. Cần báo trước cho đối tác |
| M6 | Mask IP khi migrate (PII) | Theo D15; nếu chưa chốt thì giữ nguyên, mask khi hiển thị |
| M7 | Xử lý link có `long_url` đuôi `_delXXX` | Migrate nguyên trạng, `status: deleted`, thêm `original_long_url` (cắt đuôi) để tra cứu |
| M8 | CDC bằng Debezium hay go-mysql | Debezium (đã có Kafka) |
| M9 | Có tạo campaign ảo cho `campaign_code` mồ côi | Có, cờ `virtual: true` |
| M10 | Bỏ hay giữ `secret_key`, `recovery_key` | Giữ `secret_key`, bỏ `recovery_key` |
| M11 | Suy ra `prefix` cho link cũ thế nào (DB không lưu) | `links.prefix = users.prefix` của người tạo (`sale` / `lm`); user `prefix NULL` → `sale`. Lưu ý: v1/v2 luôn trả `/sale` kể cả user `lm`, nên link do user `lm` tạo qua v1/v2 có thể bị gán nhầm `lm` → chấp nhận (chỉ ảnh hưởng hiển thị/báo cáo, không ảnh hưởng redirect vì chung không gian mã); nếu cần chính xác hơn thì link tạo trước ngày ra mắt v3 → `sale`. Đánh dấu `prefix_inferred: true` |

---

## 11. Rủi ro

| Rủi ro | Giảm thiểu |
|--------|-----------|
| Backfill làm chậm MySQL prod | Đọc từ replica, throttle theo replication lag, chạy giờ thấp điểm |
| Binlog bị xoá trước khi CDC đuổi kịp | Tăng thời gian giữ binlog ≥ 7 ngày trong giai đoạn migrate; alert dung lượng |
| Trùng mã khi đổi sang không phân biệt hoa/thường | Profiling #1; giữ phân biệt nếu có trùng |
| Lệch múi giờ làm sai báo cáo theo ngày | Chốt M2, kiểm mẫu ở ranh giới 00:00 |
| Dữ liệu bẩn (encoding, ngày 0, số dạng chuỗi) | Transform có quy tắc + `migration_errors`, review trước cutover |
| Bộ nhớ khi denormalize click (tra link) | Cache LRU `link_id → owner/campaign/ctv` + nạp theo dải; hoặc tra Redis |
| Lệch `links.clicks` sẵn có từ hệ cũ | Lấy số đếm từ click làm chuẩn cho báo cáo; giữ `clicks` cũ để tham khảo |
| Đối tác dùng API key bản rõ | Key cũ vẫn dùng được (so hash); chỉ không xem lại được |

---

## 12. Việc tiếp theo

- [ ] Ops: kiểm tra / bật `binlog_format=ROW`, GTID, replica đọc, user CDC
- [ ] Chạy các truy vấn §2, §3 trên replica, điền số liệu
- [ ] Chốt M1–M11 (cùng D15, D17–D25 của `PLAN_SERVICE_API.md`)
- [ ] Viết `cmd/migrate` (profile, backfill, verify) — song song Phase 1–2
- [ ] Dry-run trên staging với bản sao prod, cập nhật ước lượng thời gian ở §7
