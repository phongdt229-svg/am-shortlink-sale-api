# Checklist nghiệm thu — làm lại Shortlink (Service Go + Portal báo cáo Next.js + Go, MongoDB)

> Mục đích: liệt kê **mọi hành vi** của bản PHP/Lumen hiện tại mà bản mới phải giữ (hoặc quyết định sửa), để kiểm tra trước khi cutover.
> Nguồn: đọc trực tiếp code nhánh `request/202610_clear_cache` (2026-10-06). Mỗi mục có tham chiếu file gốc.
>
> Thuộc chương trình 2 project — tổng quan [`REWRITE_PLAN.md`](REWRITE_PLAN.md).
>
> | Project | Mục trong checklist |
> |---------|---------------------|
> | **Service** — [`PLAN_SERVICE_API.md`](PLAN_SERVICE_API.md) | A, B, C, D, E, F, G, G2 (phần *Dữ liệu tổng hợp*, *API & hiệu năng*), H, I (API), J, K, L, L2, N, O |
> | **Portal** — [`PLAN_PORTAL_REPORT.md`](PLAN_PORTAL_REPORT.md) | G2 (các phần màn hình R1–R8), I (thay `/report-chart`, `/report-users`), M |
> | **Cả hai** | N (bảo mật), O (cutover), P (môi trường) |
>
> Ký hiệu: `[ ]` chưa kiểm · `[x]` đạt · **⚠️ QUYẾT ĐỊNH** = hành vi cũ là bug/rủi ro → phải chốt *giữ nguyên* hay *sửa* trước khi code, ghi kết quả vào ADR.

---

## A. Redirect `GET /{short_url}` — `app/Http/Controllers/LinkController.php` (`performRedirect`)

- [ ] `urldecode` + `trim` mã ngắn trước khi tra
- [ ] Không tìm thấy → **404** (trang HTML `errors.404`, không phải JSON)
- [ ] `is_deleted = 1` hoặc `is_disabled = 1` → **404**
- [ ] Redirect **302** tới `long_url` (giữ nguyên query string, không encode lại)
- [ ] Tăng `links.clicks` +1 mỗi lần redirect (atomic, không mất click khi bấm liên tục)
- [ ] Ghi bản ghi click (khi `SETTING_ADV_ANALYTICS` bật): `ip`, `country` (GeoIP, lỗi → null), `referer`, `referer_host`, `user_agent`, `link_id`, `created_at` — `app/Helpers/ClickHelper.php`
- [ ] GeoIP lỗi / thiếu DB **không bao giờ** làm hỏng redirect (`SETTING_GEOIP_ENABLED`)
- [ ] Phân loại thiết bị `bot | tablet | mobile | desktop | unknown` đúng regex cũ — `app/Helpers/DeviceHelper.php`
- [ ] Cache redirect theo key `performRedirect:{short_url}`, TTL 60 phút; link bị xoá/disable/sửa URL phải **mất cache ngay** (hoặc chấp nhận trễ tối đa = TTL)
- [ ] Mã ngắn **phân biệt hoa/thường** như MySQL hiện tại? (kiểm collation cột `short_url` trên prod, Mongo mặc định phân biệt)
- [ ] Mã chứa ký tự `.` `+` `-` `_` vẫn redirect được (regex `validateEnding` cho phép)
- [ ] Link đi qua prefix: short URL có dạng `APP_PROTOCOL + APP_ADDRESS + '/' + ending` (vd `https://fpt.vn/sale/abc12`, v3 `/lm/...`) → kiểm cấu hình Nginx/proxy map prefix → service redirect
- [ ] **2 prefix `/sale` và `/lm`:**
  - [ ] `GET /sale/{code}` và `GET /lm/{code}` đều redirect đúng
  - [ ] Mã tạo qua v3 (`/lm`) mở được qua `/sale` và ngược lại (không gian mã chung — giữ theo D23)
  - [ ] Prefix không có trong danh sách (vd `/abc/{code}`) → 404
  - [ ] Hoạt động trên cả `fpt.vn` và `staging.fpt.vn`
  - [ ] Vẫn chạy khi proxy cắt prefix (`GET /{code}`) trong giai đoạn chuyển
  - [ ] Click ghi nhận prefix truy cập (D24)
  - [ ] Thêm prefix mới vào `prefixes` → dùng được ngay, không cần deploy (D25)
- [ ] **Tracking ngoài đồng bộ trên redirect:**
  - [ ] `creator` thuộc danh sách env `USERNAME` (phân cách dấu phẩy) → parse query `fullname, phonenumber, locationid, districtid, wardid, utm_source, utm_medium, utm_campaign, guestid` → gọi `TrackingApi::createTracking` (`app/Api/TrackingApi.php`)
  - [ ] `creator == 'lehongan'` → gọi `FmiApi::trackingFmi` (prod/non-prod endpoint khác nhau) — `app/Api/FmiApi.php`
  - [ ] Lỗi tracking chỉ ghi log, không chặn redirect
  - [ ] ⚠️ QUYẾT ĐỊNH: chuyển tracking sang **bất đồng bộ** (qua Kafka consumer) để không làm chậm redirect; hard-code `'lehongan'` → đưa thành cấu hình
- [ ] ⚠️ QUYẾT ĐỊNH: `expires_at` được lưu khi tạo link nhưng redirect **không kiểm tra hết hạn** → giữ nguyên hay áp dụng?
- [ ] ⚠️ QUYẾT ĐỊNH: click của bot vẫn được cộng vào `clicks` → có lọc bot không?
- [ ] Route `/{short_url}/{secret_key}` đang tắt → xác nhận không cần

## B. Middleware & xác thực

- [ ] **API key** (`ApiMiddleware`): đọc tham số `key` (body/query); user phải `active = 1` **và** `api_active = 1`; sai → `401` `"Authentication token invalid."`; thiếu → `401` `"Authentication token required."`
- [ ] Anonymous API khi `SETTING_ANON_API` bật: username `ANONIP-{ip}`, quota `SETTING_ANON_API_QUOTA` (mặc định 5)
- [ ] **Quota**: số link `is_api=1` của user tạo trong 60 giây qua ≥ `users.api_quota` → `429 QUOTA_EXCEEDED`; `api_quota < 0` = không giới hạn — `app/Helpers/ApiHelper.php`
- [ ] **checkIP** (`CheckIPMiddleware`): env `ALLOW_IP` (danh sách IP/CIDR, phân cách phẩy); rỗng = cho qua; bị chặn → `/api/v*/*` trả JSON 404 `IPNOTALLOW_ERROR`, còn lại trả trang 404
- [ ] Lấy IP client đúng khi đứng sau proxy (trusted proxies / `X-Forwarded-For`) — so sánh với `IPHelper` và route `/ip`
- [ ] **checkForwardedHost**: header `X-Forwarded-Host` (nếu có) phải thuộc danh sách cứng (`fpt.vn, stag.fpt.vn, sl-api-stag.fpt.vn, sl-api.fpt.vn, fpt.net, fpt.com, am-shortlink-sale-api.local, staging.fpt.vn`) **hoặc** `domains.domain_name` có `is_active = 1`; sai → `403 Forbidden`
  - [ ] ⚠️ Bug cũ: dùng `+` để gộp mảng nên domain trong DB ở index 0–7 bị bỏ qua → bản mới gộp đúng; kiểm có domain nào đang "lọt" nhờ bug không
- [ ] Ma trận middleware theo nhóm route giữ đúng như `app/Http/routes.php` (v1 admin: checkIP+host; v1 api: api+checkIP+host; v2 campaign/report/template: api+host **không checkIP**; v2/v3: api+checkIP+host)
- [ ] ⚠️ QUYẾT ĐỊNH: `RateLimitMiddleware` (60 req/s/IP) hiện **không chặn gì** (dòng return 429 bị comment) → bật thật ở bản mới?
- [ ] Header `Access-Control-Allow-Origin: *` trên response API (client web có thể đang dựa vào)
- [ ] Bỏ header `X-Powered-By` (`RemovePoweredByHeader`)
- [ ] OpenTelemetry tracing request (`OtelRequestTracking`, `app/Helpers/OtelTracer.php`)

## C. Định dạng response & lỗi

- [ ] Thành công: `{"error":0,"error_description":"Xử lý thành công!","data":...}` (HTTP 200)
- [ ] Lỗi nghiệp vụ trong controller (`responseJson(false, ...)`): `{"error":1,...}` với **HTTP 200**
- [ ] `ApiException`: `{"error":1,"error_description":msg,"data":errorData}` — ⚠️ kiểm HTTP status thực tế trả về (Handler không set status code → có thể luôn 200) — `app/Exceptions/Handler.php`
- [ ] Thông báo lỗi validation giữ **nguyên văn tiếng Việt** (client có thể so chuỗi), lấy lỗi đầu tiên
- [ ] Lỗi validate kèm `data` mặc định `{"short_url":null,"ending":null}` (shorten) hoặc `{"code":null,"type":null}` (report)
- [ ] ⚠️ QUYẾT ĐỊNH: lỗi không mong đợi ở môi trường non-local đang nối **cả exception + stack trace** vào `error_description` → bản mới chỉ trả thông báo chung, log chi tiết
- [ ] Phân trang report giữ cấu trúc Laravel paginator (`total, per_page, current_page, last_page, from, to, data`), **bỏ** `next_page_url`/`prev_page_url`

## D. Tạo link

### D1. Quy tắc chung (`app/Factories/LinkFactory.php`, `app/Helpers/LinkHelper.php`)
- [ ] `long_url` được `trim`; dài hơn `MAXIMUM_LINK_LENGTH` → lỗi "Liên kết dài của bạn dài hơn chiều dài tối đa cho phép"
- [ ] Validate `url` sau khi thay khoảng trắng bằng `%20` (chỉ để validate, lưu bản gốc)
- [ ] `custom_ending`: 5–50 ký tự, regex `^[a-zA-Z0-9._+\-]+$`
- [ ] Sinh mã ngẫu nhiên khi `SETTING_PSEUDORANDOM_ENDING` bật: độ dài = `users.random_key_length` ?? env `_PSEUDO_RANDOM_KEY_LENGTH`; lặp tới khi không trùng
- [ ] Sinh mã tuần tự base `POLR_BASE` khi tắt pseudo-random (`findSuitableEnding`) — xác nhận prod dùng chế độ nào
- [ ] **Dedupe theo user**: cùng `creator` + `crc32(long_url)` (`long_url_hash`) → trả link cũ, `is_new = false`
  - [ ] ⚠️ crc32 có thể va chạm → bản mới so thêm `long_url` đầy đủ (hoặc dùng hash mạnh hơn)
- [ ] Link trùng nhưng **đã xoá**: đổi `long_url` cũ thành `long_url + '_del' + 3 ký tự ngẫu nhiên` rồi tạo link mới
- [ ] `custom_ending` đã tồn tại và **đã xoá** → lỗi "Link rút gọn này đã xóa!"
- [ ] `custom_ending` đã tồn tại, chưa xoá → v1/v2 trả về link đó với `is_new=false`
  - [ ] ⚠️ QUYẾT ĐỊNH: v2 trả cả link **của user khác** (rò rỉ long_url) — v3 đã chặn; bản mới thống nhất chặn?
- [ ] Hết hạn: nếu `users.is_expires = 1` → `expires_at = hôm nay + users.expires_value ngày`
- [ ] `ctv_identifier` = query param `utm_extra_ctv` của long_url (không có → null)
- [ ] Lưu `ip` người tạo, `is_custom`, `is_api = 1`, `campaign_code`, `campaign_id`, `long_url_hash`, `creator` (username)
- [ ] Short URL trả về = `APP_PROTOCOL + APP_ADDRESS + '/' + ending`

### D2. `POST /api/v1/shorten`
- [ ] Bắt buộc `url` (url) và `domain` (url)
- [ ] Link đã có (theo user) → trả `{short_url, long_url, ending, is_new:false}`, message "Xử lý thành công!" — ⚠️ `ending` ở nhánh này đang là **full URL** chứ không phải mã
- [ ] Host `fpt.vn` chứa `fpt.vn/shop` → lỗi "[fpt.vn/shop]"
- [ ] Host `shop.fpt.vn` mà không chứa `shop.fpt.vn/tin-tuc/cong-tac-vien` → lỗi "[shop.fpt.vn]"
- [ ] Host không thuộc `config('shorten.domain_allow')` → "Domain không hợp lệ" — `config/shorten.php`
- [ ] Tạo mới → `{short_url, ending, is_new:true}`
- [ ] `POST /api/v1/link_avail_check` — kiểm mã custom còn trống

### D3. `POST /api/v2/shorten`
- [ ] Tham số `url`, `custom_ending`, `code` (campaign, 5–40 ký tự), `domain`
- [ ] Campaign tồn tại nhưng `created_by != user.id` → "Campaign code không hợp lệ."
- [ ] Campaign không tồn tại → vẫn tạo, `campaign_id = 0`, `campaign_code` = code gửi lên
- [ ] Response `{short_url, ending, is_new}`
- [ ] Rate limit middleware (`rate.limit`)

### D4. `POST /api/v2/shorten-multi`
- [ ] `urls` mảng, tối đa **1000** phần tử
- [ ] ⚠️ Bug điều kiện campaign: `!$campaign && $campaign['created_by'] != ...` → thực tế gần như không kiểm quyền campaign; bản mới kiểm đúng
- [ ] Chèn theo lô 500, dedupe theo hash (`insertMultiLinks`, `checkDuplicateUrls`) — so sánh output từng phần tử với bản cũ

### D5. `POST /api/v2/update-shorten-multi`
- [ ] Tối đa **100** urls; mỗi item `{path, custom_ending (có thể là full URL → lấy phần cuối), key_item}`
- [ ] Response mỗi item `{short_url, ending, key_item, is_exist}`
- [ ] Đối chiếu logic `updateLinkMutilApiUser` (cập nhật long_url của mã đã có)

### D6. `POST /api/v3/shorten`, `/api/v3/shorten-multi` — `app/Http/Controllers/Api/V3/ShortenController.php`
- [ ] Prefix short URL theo thứ tự: `users.prefix` → env `APP_ADDRESS_V3` → `APP_ADDRESS`
- [ ] `users.prefix` chỉ nhận giá trị có trong `prefixes` (hiện `sale`, `lm`) hoặc `null`; chỉ lưu đoạn path, chấp nhận gõ tay `/lm`, `lm/` mà không sinh `//lm`; host ghép theo môi trường
- [ ] User `prefix = sale` gọi v3 → link `/sale/...`; user `prefix = lm` → `/lm/...`; user `prefix = null` → `/lm/...` (`APP_ADDRESS_V3`)
- [ ] Link tạo mới lưu `prefix` đã cấp (v1/v2 = `sale`, v3 = prefix đã chọn)
- [ ] Dedupe trả link cũ: short URL trả về dùng prefix của **lần gọi hiện tại** (như code cũ: v3 trả `/lm` kể cả khi link được tạo trước đó qua v2) — xác nhận giữ hành vi này
- [ ] v1/v2 **không bao giờ** dùng `users.prefix`
- [ ] Response v3 shorten: `{short_url, ending}` (**không có** `is_new`)
- [ ] Multi: `code` theo từng item, `code` ngoài là mặc định; lỗi cấp request → không tạo gì; lỗi cấp item → item có `error`, item khác vẫn chạy
- [ ] Thông báo lỗi item dạng `"Dòng {n}: ..."` (thiếu url/path, rỗng, quá dài, sai định dạng, code 5–40, ending 5–50, ký tự không hợp lệ)
- [ ] Chặn ghi đè `custom_ending` thuộc user khác (`endingOwners`, `reserveEndings`)

## E. Xoá / khôi phục / tìm / QR

- [ ] `POST /api/v1/delete` — `short_url` (url) → soft delete (`is_deleted = 1`) — `app/Services/LinkService.php`
- [ ] `POST /api/v1/restore` — `is_deleted = 0`
- [ ] Xoá / khôi phục / sửa URL **phải xoá cache redirect** của mã đó
- [ ] Chỉ chủ link (hoặc admin) được xoá/khôi phục — kiểm hành vi cũ
- [ ] `POST /api/v2/search` — `url` là short URL, lấy phần cuối làm mã; tìm theo user; trả `{short_url, long_url}` hoặc `[]`
- [ ] `POST /api/v1/qrcode` — `short_url` + kích thước → ảnh base64 (`QRService`, có nhánh Google Chart) — so ảnh/định dạng output

## F. Campaign & template

- [ ] `POST /api/v2/campaign/create` — `name` bắt buộc, ≤255, **unique toàn bảng**; `code` bắt buộc ≤10 (⚠️ trong khi shorten yêu cầu code 5–40 → chốt quy tắc chung)
- [ ] `POST /api/v2/campaign/list` — chỉ campaign của user? (kiểm `getList`) + phân trang
- [ ] `GET|POST /api/v2/template/list`, `/detail` — không cần auth key? (nhóm `api` middleware → kiểm lại)

## G. Báo cáo

- [ ] ⚠️ QUYẾT ĐỊNH: `POST /api/v2/report` khai báo 2 lần (`Api\V2\LinkController@listAction` và `ApiLinkController@reportShortenLink`) → xác định handler đang thực sự chạy trên prod bằng request thật
- [ ] `report`: `code` bắt buộc, `type ∈ {all, click}`, `display ≤ 1000`, `page`; lọc theo `creator = user hiện tại`
- [ ] `report-total`: `code`, `from_date`, `to_date`, `type`
- [ ] `report-total-daily`: `code`, `report_date`
- [ ] `report-total-range`: `code`, `from_date`, `to_date`
- [ ] `report-total-detail`: `limit`, `from_date`, `to_date`, `phone`, `hash` (tuỳ chọn) + kiểm khoảng ngày
- [ ] `report-click-analytics`: summary + timeline + breakdown (country/device/referer...) cho chart; `creator` bắt buộc, `code` chuỗi hoặc mảng, khoảng ngày ≤ 3 tháng, `to ≥ from`
- [ ] `report-click-list`: danh sách từng click, phân trang, cùng tham số
- [ ] `report-overview-users`: số link + số click theo user + tổng; ngày tuỳ chọn, ≤ 3 tháng, `display ≤ 1000`
- [ ] Cache report: key `report_click_analytics:{md5(params)}`, `report_overview_users:{md5(params)}`, TTL `SETTING_REPORT_CACHE_TTL` (60s) — `app/Models/Link.php`
- [ ] ⚠️ QUYẾT ĐỊNH: `report-click-*`, `report-overview-users`, `update-links-ctv-identifier` nhận `creator` **từ request** → API key bất kỳ xem/sửa dữ liệu user khác. Bản mới giới hạn theo quyền (admin mới được chỉ định creator khác)
- [ ] **Đối chiếu số liệu**: chạy cùng bộ tham số trên PHP và Go cho ≥ 20 user/campaign thật; chênh lệch = 0 (hoặc giải thích được do timezone/thời điểm)
- [ ] Múi giờ ngày báo cáo (Asia/Ho_Chi_Minh) và biên `to_date` (bao gồm cả ngày cuối?)

## G2. Báo cáo mới (theo `PLAN_SERVICE_API.md` §5, màn hình theo `PLAN_PORTAL_REPORT.md` §4)

### Dữ liệu tổng hợp
- [ ] Định nghĩa chỉ số §5.2 được nghiệp vụ duyệt (D17, D18)
- [ ] Consumer cập nhật đủ `stats_link_daily`, `stats_campaign_daily`, `stats_ctv_daily`, `stats_owner_daily`, `stats_system_daily` cho mỗi click; độ trễ < 1 phút
- [ ] Sự kiện tạo / xoá link cập nhật `new_links`; `active_links` chỉ tăng 1 lần / link / ngày
- [ ] `unique_clicks` không cộng trùng cùng khách trong ngày; bot tách riêng `bot_clicks`
- [ ] Replay cùng event 2 lần → số liệu không đổi (idempotent theo `event_id`)
- [ ] Đổi `campaign_code` / `ctv_identifier` của link → quyết định số liệu cũ giữ theo giá trị cũ hay chuyển (ghi vào ADR)
- [ ] `ctv_identifier` chuẩn hoá (SĐT `+84`/`84`/`0` → `0xxxxxxxxx`) và backfill cho link cũ (D20)
- [ ] Rollup tháng chạy đêm, khớp tổng các ngày
- [ ] Job đối soát đêm: `stats_link_daily` = đếm từ `clicks`; lệch → rebuild + alert
- [ ] Job rebuild theo khoảng ngày chạy được trên dữ liệu thật trong thời gian chấp nhận được

### Theo tài khoản (R2)
- [ ] Danh sách tài khoản: `total_links`, `new_links`, `active_links`, `clicks`, `unique_clicks`, `bot_clicks`, so kỳ trước; sắp xếp / tìm / phân trang
- [ ] Chi tiết tài khoản: KPI, timeline, chiến dịch, top CTV, top link, phân rã
- [ ] Tổng các tài khoản = tổng toàn hệ thống (R1)
- [ ] User thường chỉ xem được tài khoản của mình

### Theo chiến dịch (R3)
- [ ] Danh sách chiến dịch theo tài khoản + khoảng ngày, kèm số CTV tham gia
- [ ] Chi tiết chiến dịch: KPI, timeline, xếp hạng CTV, top link, phân rã thiết bị / nguồn / quốc gia / giờ
- [ ] So sánh 2–5 chiến dịch
- [ ] Tổng các chiến dịch của 1 tài khoản = số liệu tài khoản đó
- [ ] Link không có campaign được gom vào nhóm "Không có chiến dịch"

### Theo CTV (R4)
- [ ] Bảng xếp hạng CTV lọc theo tài khoản / chiến dịch / ngày, có `suspicious_clicks`
- [ ] Chi tiết CTV: KPI, timeline, link, chiến dịch tham gia, cảnh báo
- [ ] Tra cứu theo SĐT / hash < 100ms; kết quả khớp `report-total-detail` cũ (kể cả link tạo trước kỳ nhưng có click trong kỳ)
- [ ] Báo cáo link / click chưa có CTV
- [ ] Số liệu export đối soát trả thưởng loại trừ bot / click nghi vấn theo D18
- [ ] Tổng các CTV + "chưa định danh" = số liệu chiến dịch

### Phân tích lượt click chi tiết — Click Explorer (R6b) — *Service + Portal*
- [ ] Lọc được theo: tài khoản (nhiều), khoảng ngày + khung giờ + thứ, mã link (dán danh sách), prefix truy cập / prefix cấp, API version tạo link, trạng thái link, ngày tạo link, domain đích, `utm_*`, chiến dịch, CTV (dán danh sách SĐT), thiết bị, OS, trình duyệt / in-app, nhóm nguồn, referer host, quốc gia / tỉnh, chất lượng (hợp lệ / bot / nghi vấn / lặp), IP (admin)
- [ ] Include / exclude từng tiêu chí; AND giữa tiêu chí, OR trong tiêu chí
- [ ] Group by 1–3 chiều; chỉ số clicks, unique, bot, nghi vấn, active links, % tổng, % kỳ trước
- [ ] Tổng các nhóm = thẻ tổng; số khớp R2/R3/R4 trên cùng bộ lọc
- [ ] Drill-down từ ô pivot ra click thô đúng bộ lọc
- [ ] Tab "Lượt click" trong chi tiết tài khoản / chiến dịch / CTV / link khoá đúng bộ lọc; user không gỡ được khoá tài khoản để xem tài khoản khác
- [ ] Facets chỉ trả giá trị thuộc phạm vi quyền
- [ ] Lưu / chia sẻ bộ lọc; người nhận chỉ thấy dữ liệu theo quyền của họ
- [ ] Click mới có đủ trường làm giàu (`os`, `browser`, `in_app`, `source_group`, `access_prefix`, `hour`...); link mới lưu sẵn `dest_host`

### Tham số URL (`PLAN_SERVICE_API.md` §5.4b) — *Service + Portal*
- [ ] Dữ liệu báo cáo nằm ở database `am_shortlink_report`, tách khỏi `am_shortlink`; api-svc chỉ có quyền đọc
- [ ] Tạo / sửa link → `link_params` có đủ mọi tham số (decode, tham số lặp, bỏ fragment); sửa `long_url` cập nhật đúng
- [ ] Click → `stats_param_daily` tăng cho mọi tham số `tracked` của link; replay event không cộng trùng
- [ ] Bật theo dõi tham số mới trên Portal → backfill chạy, có tiến độ; số liệu chỉ từ ngày còn click thô
- [ ] Tham số PII (`phonenumber`, `fullname`, `guestid`...) không lưu / không hiển thị bản rõ (D30)
- [ ] Tham số nhiều giá trị (`gclid`, `fbclid`...) tự chuyển `high_cardinality`, không làm phình stats
- [ ] Báo cáo theo tham số: bảng giá trị, ma trận 2 tham số, drill-down sang link và click thô
- [ ] Lọc Click Explorer theo `key = value` / thuộc danh sách / có / không có tham số; tổng theo một key (gồm "không có") = tổng click
- [ ] Migrate: `link_params` được tạo cho toàn bộ link cũ; `stats_param_*` khớp số click theo §4.6 `DATA_MIGRATION_PLAN.md`
- [ ] Hiệu năng: tổng hợp ≤ 12 tháng < 2s; truy vấn tự do ≤ 3 tháng p95 < 5s, timeout 10s, huỷ được (D28)

### Link, nhật ký click, chất lượng traffic (R5–R7)
- [ ] Chi tiết link: KPI, timeline, phân rã, 100 click gần nhất
- [ ] Top link; link không có click N ngày
- [ ] Nhật ký click: lọc đủ chiều, phân trang con trỏ, giới hạn ≤ 3 tháng
- [ ] IP bị che với user thường, đầy đủ với admin
- [ ] Top IP, tỉ lệ click lặp, danh sách click nghi vấn kèm lý do

### Tổng quan & export (R1, R8)
- [ ] Tổng quan: KPI + % so kỳ trước, timeline, top 10 mỗi chiều, heatmap giờ × thứ
- [ ] Export CSV / XLSX chạy nền, file có TTL 7 ngày, giới hạn 1 triệu dòng; số dòng / tổng khớp màn hình
- [ ] File export đúng tiếng Việt (UTF-8 BOM cho Excel), định dạng ngày thống nhất

### API & hiệu năng
- [ ] `/api/v4/reports/*` đủ endpoint §5.6, tham số chung (`from`, `to`, `owner`, `campaign`, `ctv`, `granularity`, `compare`, `cursor`, `sort`)
- [ ] Giới hạn khoảng ngày: ≤ 12 tháng (tổng hợp), ≤ 3 tháng (click thô), thông báo lỗi rõ
- [ ] Báo cáo tổng hợp < 500ms, nhật ký click < 1s trên dữ liệu thật
- [ ] Cache 60s theo tham số; số liệu "hôm nay" trễ ≤ 1 phút
- [ ] Phân quyền admin / user / viewer đúng phạm vi §5.7 (kiểm cả gọi API trực tiếp, không chỉ ẩn trên UI)

## H. Tiện ích & cache

- [ ] `POST /api/v2/update-links-ctv-identifier` — `page`, `limit ≤ 1000`; trả `{updated, total, current_page, per_page}`
- [ ] `POST /api/v2/cache-clear` — xoá toàn bộ cache, trả `{cleared_at, message}`
- [ ] `POST /api/v2/cache-check-key` — nhận `short_url` (→ `performRedirect:{code}`) hoặc `cache_key`; trả `{cache_key, exists, value}` (tóm tắt, không trả nguyên object)
- [ ] `POST /api/v2/cache-clear-key` — trả `{cache_key, existed, cleared_at}`
- [ ] ⚠️ QUYẾT ĐỊNH: mọi API key đều gọi được cache-clear toàn bộ → giới hạn cho admin; với Redis dùng xoá theo prefix, **không** `FLUSHALL`

## I. Admin & trang web (sẽ thay bằng Portal)

- [ ] `/api/v1/admin/*` (session admin): `toggle_api_active`, `generate_new_api_key`, `edit_api_quota`, `toggle_user_active`, `change_user_role`, `add_new_user`, `delete_user`, `toggle_link`, `delete_link`, `edit_link_long_url`, `get_admin_users`, `get_admin_links`, `get_user_links`
- [ ] `GET|POST /signup` (khi bật), `/login`, `/logout`
- [ ] `/report-chart`, `/report-users` (trang HTML tĩnh, header chống cache)
- [ ] `/`  trả 404; `/log` (đẩy log test Kafka); `/ip` (chẩn đoán IP) → quyết định giữ hay bỏ
- [ ] Trường user cần mang sang: `prefix`, `random_key_length`, `is_expires`, `expires_value`, `api_quota`, `api_active`, `role`, `active`

## J. Tích hợp & quan sát

- [ ] Log ra Kafka (`app/Logging/KafkaHandler.php`, `app/Helpers/KafkaConnect/KafkaPush.php`) — giữ topic & format để hệ thống đọc log không vỡ
- [ ] `Helper::writeLog` cho lỗi gọi API ngoài
- [ ] OpenTelemetry: tên service, attribute, exporter
- [ ] GeoIP MaxMind DB (local file) — `config/geoip.php`
- [ ] TrackingApi / FmiApi: URL, auth, timeout (hiện `max_execution_time 30s` trên redirect!)
- [ ] Toàn bộ biến env được map sang cấu hình mới: `APP_ADDRESS`, `APP_ADDRESS_V3`, `APP_PROTOCOL`, `ALLOW_IP`, `USERNAME`, `SETTING_ANON_API`, `SETTING_ANON_API_QUOTA`, `SETTING_ADV_ANALYTICS`, `SETTING_GEOIP_ENABLED`, `SETTING_PSEUDORANDOM_ENDING`, `_PSEUDO_RANDOM_KEY_LENGTH`, `POLR_BASE`, `SETTING_REDIRECT_CACHE`, `SETTING_REPORT_CACHE_TTL`, `SETTING_SHORTEN_PERMISSION`, `APP_ENV`, `KAFKA_LOG_*`, `OTEL_*`, `BASE_URL_FMI`, `API_KEY_FMI`, `MAXMIND_LICENSE_KEY` (bảng map: `ENV_OBSERVABILITY_PLAN.md` §2.4)

### Log Kafka (`ENV_OBSERVABILITY_PLAN.md` §3)
- [ ] Log JSON ra stdout + Kafka topic `dev-am-shortlink-logs` / `stag-am-shortlink-logs` / `am-shortlink-logs` (cụm `isc-kafka01..03:9092`); message giữ trường cũ `log_type, environment, service, data, url` → Kibana hiện tại đọc được không phải sửa
- [ ] Có thêm `timestamp, level, message, trace_id, span_id, request_id, method, status, latency_ms, user, api_version, prefix`
- [ ] Log `tracking_utm` giữ `log_type=tracking_utm` trong cùng topic log (như `fptvn-web`)
- [ ] Topic event & consumer group có tiền tố môi trường (`dev-`, `stag-`, prod rỗng); consumer staging không đọc topic prod (chung cụm Kafka)
- [ ] Ghi log **không chặn request**: tắt Kafka / Kafka chậm → redirect, API vẫn bình thường; có metric `log_kafka_dropped_total`
- [ ] Không có API key, mật khẩu, `Authorization`, cookie trong log; SĐT và IP được che
- [ ] Access log redirect ở prod lấy mẫu theo cấu hình, lỗi 4xx/5xx ghi 100%
- [ ] Topic log tách khỏi topic event nghiệp vụ (`click-events`, `link-events`)

### OpenTelemetry (`ENV_OBSERVABILITY_PLAN.md` §4)
- [ ] Trace đầu-cuối Service: api-svc → Mongo / Redis / Kafka → consumer → TrackingApi / FMI cùng 1 `trace_id`
- [ ] Trace đầu-cuối Portal: trình duyệt → `portal-web` → `portal-api` → MongoDB cùng 1 `trace_id`
- [ ] Span HTTP đặt tên theo route template (`/{prefix}/{code}`), không theo URL thật
- [ ] Resource: `service.name` từng service, `service.namespace`, `service.version`, `deployment.environment`
- [ ] Endpoint Collector lấy từ env (bỏ hard-code `otel-collector.fpt.net`); Collector chết → service vẫn chạy
- [ ] Bộ biến OTel giữ đúng quy ước `fptvn-web` (`OTEL_ENABLED`, `OTEL_SERVICE_NAMESPACE`, `OTEL_SERVICE_NAME`, `OTEL_ENDPOINT_TRACES`, `OTEL_ENDPOINT_LOG`, `OTEL_SAMPLER`, `OTEL_SAMPLER_RATIO`); trace gửi `http://otel-collector.fpt.net/v1/traces`, log error gửi `/v1/logs` (đúng endpoint, không lặp lỗi gửi nhầm của `fptvn-web`)
- [ ] `OTEL_SAMPLER`: dev/staging `always`, prod `ratio` 0.05; `OTEL_SERVICE_NAMESPACE=am-shortlink`, `OTEL_SERVICE_NAME=am-shortlink-sale-api` (đã cấp) đúng ở mọi môi trường

### Redis cache (`ENV_OBSERVABILITY_PLAN.md` §1.1, §2)
- [ ] Biến `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_DB` theo quy ước `fptvn-web`; host `am-shortlink-redis-cache-dev` / `-staging` / `am-shortlink-redis-cache`
- [ ] Key có tiền tố `am-shortlink:`; ghi bằng `SET … EX` (atomic), pool kết nối, không `PING` trước mỗi lệnh
- [ ] Redis chết → redirect / API vẫn chạy (đọc thẳng Mongo), có metric + cảnh báo
- [ ] `cache-clear` chỉ xoá key có tiền tố của shortlink, không `FLUSHDB` / `FLUSHALL`
- [ ] Sampling đúng theo môi trường; trace lỗi / chậm được giữ 100% (tail sampling)
- [ ] Metrics §4.4 có trên dashboard; cảnh báo §4.5 bắn thử được ở prod
- [ ] `trace_id` có trong mọi log; response có `X-Request-Id`

## P. Môi trường dev / staging / production (`ENV_OBSERVABILITY_PLAN.md` §2)

- [ ] **Môi trường dev mới được dựng** (trước đây chỉ có staging + production): namespace, Mongo, Redis, topic `dev-`, domain riêng, Collector
- [ ] dev, staging, production tách biệt hoàn toàn: DB, Redis, Kafka, domain, secret; staging không kết nối gì của prod
- [ ] Domain đúng theo môi trường: dev `…/sale`, `…/lm`; staging `staging.fpt.vn/sale|lm`; prod `fpt.vn/sale|lm` — link sinh ra ở dev/staging **không** mang domain prod
- [ ] Mapping nhánh → môi trường đúng E1: `develop` → dev, `release/*` → staging, tag → prod (duyệt tay)
- [ ] Cùng 1 image chạy được cả 3 môi trường, chỉ khác cấu hình
- [ ] Service thiếu / sai biến môi trường → dừng ngay lúc khởi động, báo rõ biến nào
- [ ] Không còn file `.env` chứa secret trong repo; secret nạp từ **HashiCorp Vault** (`VAULT_PROJECT_PATH=isc-project/${PROJECT_FULLNAME}`, role `VAULT_PROJECT_ROLE`) — `ENV_OBSERVABILITY_PLAN.md` §2.4b
- [ ] Vault có nhánh riêng cho dev / staging / production; secret staging khác production
- [ ] Service đọc được secret dạng env và dạng file (`*_FILE`); thiếu secret → dừng, chỉ báo tên biến
- [ ] Không log giá trị secret (kể cả lúc khởi động, lúc lỗi kết nối)
- [ ] Rotate 1 secret trong Vault → service nhận giá trị mới sau rollout, không phải sửa code
- [ ] ⚠️ Hệ PHP hiện tại: gỡ `.env.Staging`, `.env.Production` khỏi git và rotate toàn bộ secret đã lộ (E5)
- [ ] Staging dùng dữ liệu prod đã ẩn danh (IP, SĐT CTV, email)
- [ ] Tích hợp ngoài: dev dùng mock, staging dùng sandbox / FMI non-prod, prod dùng thật
- [ ] Script index / migration Mongo chạy như Job trước deploy ở cả 3 môi trường, chạy lại không lỗi
- [ ] Local: `docker compose up` + `make seed` chạy được toàn hệ không cần hạ tầng chung

## K. Dữ liệu & migrate MySQL → MongoDB

- [ ] Dump schema **thật** từ prod (gồm cột không có migration: `domain_id, is_deleted, campaign_id, campaign_code, expires_at, ctv_identifier`, bảng `campaigns, domains, templates`, cột user mở rộng)
- [ ] Đếm bản ghi khớp 100%: `links`, `users`, `campaigns`, `domains`, `templates`
- [ ] `clicks`: tổng số click theo link/ngày khớp với bảng tổng hợp mới; `links.clicks` khớp
- [ ] Unique index mã ngắn tạo **trước** khi backfill; không có mã trùng sau migrate
- [ ] Giữ nguyên ID cũ làm `_id` (int64; clicks giữ `legacy_id`) để đối chiếu và để client đang lưu id không vỡ — xem `DATA_MIGRATION_PLAN.md` M1
- [ ] Ngày giờ: chuyển timezone đúng (MySQL lưu giờ local?) — kiểm 10 bản ghi mẫu
- [ ] Link có `long_url` dạng `..._delXXX` được migrate nguyên trạng
- [ ] Đồng bộ liên tục (CDC/dual-write) trong giai đoạn chạy song song; độ trễ đồng bộ < 5s
- [ ] Chỉ **một** hệ được sinh mã mới tại một thời điểm (tránh trùng mã giữa 2 hệ)
- [ ] Kế hoạch rollback: MySQL vẫn nhận ghi / có thể đồng bộ ngược trong N tuần
- [ ] Retention & PII: IP, user-agent trong click — chốt thời gian lưu, có hash/mask không

## L. Hiệu năng & vận hành

- [ ] Load test redirect (k6): p99 < 20ms tại tải mục tiêu, cache hit > 95%
- [ ] Load test shorten đơn & multi 1000 url; thời gian phản hồi ≤ bản cũ
- [ ] Report trên dữ liệu thật (30tr link): mọi endpoint < 1s
- [ ] Mất Redis → redirect vẫn chạy (fallback Mongo); mất Kafka → redirect vẫn chạy, click được buffer
- [ ] Consumer lag có alert; replay được khi lỗi (idempotent theo event id)
- [ ] Health check `/healthz`, `/readyz`; graceful shutdown không mất click
- [ ] Dashboard: RPS, p50/p99, 4xx/5xx, cache hit, quota bị chặn, consumer lag
- [ ] Backup & restore Mongo đã diễn tập
- [ ] CI: lint, unit test, integration test (testcontainers), build image

## L2. Tài liệu API (OpenAPI / Swagger) — `PLAN_SERVICE_API.md` §3.1

- [ ] `api/openapi/openapi.yaml` (OpenAPI 3.1) mô tả **đủ mọi endpoint** đang có trong `app/Http/routes.php` (v1, v2, v3, redirect) + v4 báo cáo (API Portal nằm ở `openapi/portal-api.yaml` trong repo Portal)
- [ ] Mỗi operation có: mô tả, tham số + validate (min/max/enum), ví dụ request/response, danh sách thông báo lỗi nguyên văn
- [ ] Schema `Envelope` `{error, error_description, data}` dùng chung; ghi rõ legacy trả HTTP 200 khi lỗi nghiệp vụ
- [ ] `securitySchemes` đúng thực tế: tham số `key` cho mọi API đối tác v1–v4 (như cũ); ghi chú `ALLOW_IP` (JWT Portal do `portal-api` cấp, không dùng ở Service)
- [ ] Đối tác gọi API v1–v4 bằng `key` **y như cũ** (body / query), không cần đổi code phía đối tác
- [ ] Mọi trang Portal (trừ `/login`) yêu cầu đăng nhập; hết phiên → về `/login`, giữ lại URL đang xem
- [ ] API key không đăng nhập được Portal; JWT Portal (`portal-api`) không gọi được API đối tác v1–v4
- [ ] Đăng nhập sai 5 lần → khoá tạm; đăng xuất huỷ refresh token
- [ ] `spectral lint` pass; `oasdiff breaking` chạy trong CI và chặn MR phá hợp đồng
- [ ] Code Go sinh bằng `oapi-codegen` khớp spec (CI kiểm `git diff` sau khi generate = rỗng)
- [ ] Contract test: chạy golden test theo spec, response của Go **validate được** theo schema
- [ ] Swagger UI `/docs` + `/openapi.yaml` chạy ở dev/staging; production theo D27
- [ ] Import `openapi.yaml` vào Insomnia/Postman chạy được ngay (biến môi trường local/staging/production)
- [ ] Bản HTML (Redoc) theo version publish cho đối tác, kèm changelog API

## M. Portal báo cáo (Next.js + Go + MongoDB) — `PLAN_PORTAL_REPORT.md`

### Kiến trúc & repo
- [ ] Repo `am-shortlink-portal` đúng cấu trúc §3: `api/` (Go, `portal-api`), `web/` (Next.js), `openapi/portal-api.yaml`, `deploy/`
- [ ] `openapi/portal-api.yaml` là nguồn sự thật; `make gen` sinh `api/internal/httpapi/gen` + `web/src/lib/api`; CI fail nếu diff sau generate khác rỗng hoặc `oasdiff breaking`
- [ ] Chỉ package `store` của `portal-api` import driver MongoDB; handler không dựng truy vấn
- [ ] `portal-web` không có thông tin kết nối MongoDB; mọi dữ liệu qua `portal-api` (BFF / server component)
- [ ] Portal không gọi API của Service; chỉ phụ thuộc hợp đồng schema MongoDB (test tích hợp trên `migrations/` của Service)
- [ ] `docker compose` local chạy được MongoDB + `portal-api` + `portal-web` + OTel; `cmd/seed` sinh dữ liệu đúng schema
- [ ] CI build 2 image (`portal-api`, `portal-web`) độc lập theo thư mục thay đổi; `cmd/migrate` chạy như K8s Job trước deploy

### MongoDB (`portal-api`)
- [ ] User Mongo `portal_api` quyền tối thiểu: đọc `am_shortlink`, đọc thống kê `am_shortlink_report`, ghi chỉ `portal_*` + `param_registry`
- [ ] Đọc `secondaryPreferred` / node analytics; `maxTimeMS` 10s mọi truy vấn; huỷ truy vấn khi người dùng huỷ
- [ ] Mọi truy vấn có điều kiện phạm vi tài khoản (package `scope`) và dùng index `(meta.owner, ts)` / `(owner, date)` — kiểm bằng `explain`
- [ ] Giới hạn khoảng ngày: tổng hợp ≤ 12 tháng, click thô ≤ 3 tháng

### Cache Redis (`PLAN_PORTAL_REPORT.md` §3.2)
- [ ] Redis riêng của Portal; khoá cache theo phạm vi đã áp quyền — user A không bao giờ nhận cache của user B khác phạm vi
- [ ] TTL 60s khi kỳ có hôm nay, 15 phút khi kỳ đã qua; tắt Redis / Redis treo → báo cáo vẫn trả đúng (fail-open ≤ 150ms)
- [ ] Rate limit đăng nhập đếm chung giữa replica qua Redis
- [ ] Metrics hit/miss/lỗi cache có trên dashboard

### Xác thực & phân quyền
- [ ] Đăng nhập qua `portal-api` (bcrypt `password_hash` trong `users`, `portal_access`); đăng xuất huỷ refresh token
- [ ] JWT + refresh chỉ ở server (BFF); trình duyệt chỉ có cookie httpOnly JWE
- [ ] Phân quyền admin / user / viewer thực thi ở `portal-api`; gọi thẳng API vượt phạm vi → 403
- [ ] SĐT CTV / IP che theo vai trò ở `portal-api` (package `mask`)

### Màn hình
- [ ] R1 tổng quan, R2 tài khoản, R3 chiến dịch (+ so sánh), R4 CTV (xếp hạng, chi tiết, tra cứu < 1s, chưa định danh), R5 link (+ QR, top / link chết), R6 nhật ký click, R6b Click Explorer, R7 chất lượng traffic, R8 export
- [ ] Báo cáo theo tham số URL; quản lý `param_registry` (admin) hiển thị tiến độ backfill
- [ ] Bộ lọc dùng chung (preset ngày, tài khoản, chiến dịch, CTV, prefix) lưu trên URL, chia sẻ link báo cáo được
- [ ] Drill-down Tài khoản → Chiến dịch → CTV → Link → Click
- [ ] Thay thế báo cáo `/report-users` và `/report-chart`; số liệu khớp báo cáo cũ trên cùng bộ lọc
- [ ] Trang báo cáo < 2s (p95)
- [ ] Responsive, hiển thị đúng tiếng Việt, định dạng ngày `dd/MM/yyyy`, múi giờ Asia/Ho_Chi_Minh

### Giai đoạn 2 (không thuộc nghiệm thu giai đoạn 1 — `PLAN_PORTAL_REPORT.md` §9)
- [ ] Quản trị link (tạo, sửa, bật/tắt, xoá, khôi phục, tạo hàng loạt CSV) — thao tác ghi đi qua Service
- [ ] Quản trị user, API key, prefix, domain, template; công cụ cache theo mã

## N. Bảo mật

- [ ] Không trả stack trace ra client
- [ ] API key lưu dạng hash; có thể rotate
- [ ] Kiểm quyền sở hữu trên mọi endpoint nhận `creator`/`short_url`/`code`
- [ ] Rate limit thật theo IP và theo API key
- [ ] Chặn open-redirect lạm dụng: danh sách domain cho phép (`domain_allow`) áp dụng nhất quán v1/v2/v3?
- [ ] Không commit `.env.production`, `.env.staging`, file credential

## O. Cutover

- [ ] Golden test: replay ≥ 10.000 request thật (lấy từ access log) vào cả 2 hệ, diff response = 0 (trừ các mục ⚠️ đã chốt sửa)
- [ ] Shadow traffic redirect ≥ 1 tuần, không lệch
- [ ] Canary redirect 5% → 50% → 100% — áp dụng cho **cả `/sale/*` và `/lm/*`**, cả production và staging
- [ ] Ingress/Nginx: `/sale/*`, `/lm/*` → redirect-svc; `/api/*` → api-svc; kiểm không còn route nào trỏ về PHP
- [ ] Chuyển API theo nhóm: shorten → delete/restore/search/QR → report → admin
- [ ] Thông báo đối tác/client về thay đổi (nếu có mục ⚠️ đổi hành vi)
- [ ] Cập nhật KB: ADR, `12-api-catalog.md`, `13-data-dictionary.md`, `02-tech-stack.md`, `CHANGELOG.md`
- [ ] MySQL read-only N tuần → tắt hệ PHP
