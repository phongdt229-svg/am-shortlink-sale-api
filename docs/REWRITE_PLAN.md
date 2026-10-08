# Kế hoạch tổng — Làm lại AM Shortlink (2 project)

> **Trạng thái:** Đề xuất (draft) · **Ngày lập:** 2026-10-06 · **Tách 2 project:** 2026-10-07 · **Cập nhật kiến trúc Portal:** 2026-10-07 — Portal gồm frontend Next.js + backend Go **kết nối trực tiếp MongoDB** (P-D10)
> **Hệ hiện tại:** `am-shortlink-sale-api` — PHP 5.5 / Lumen 5.1 / MySQL (Polr fork)
> **Tài liệu này:** tổng quan chương trình, ranh giới & hợp đồng giữa 2 project, lộ trình chung. Chi tiết nằm trong từng plan con.

---

## 1. Hai project

| | **Project 1 — Service API** | **Project 2 — Portal báo cáo** |
|---|---|---|
| Repo | `am-shortlink-service` | `am-shortlink-portal` (`web/` + `api/`) |
| Plan chi tiết | [`PLAN_SERVICE_API.md`](PLAN_SERVICE_API.md) | [`PLAN_PORTAL_REPORT.md`](PLAN_PORTAL_REPORT.md) |
| Stack | Go 1.23+, MongoDB 7, Redis 7, Kafka, OpenTelemetry | **Frontend:** Next.js 15 + TypeScript, shadcn/ui, Recharts · **Backend:** Go 1.23+ (`chi`, `mongo-go-driver v2`) kết nối trực tiếp MongoDB · OpenTelemetry |
| Làm gì | Redirect `/sale`, `/lm`; API v1/v2/v3 (tương thích ngược); click pipeline + thống kê `stats_*`, `link_params`, `stats_param_*`; API báo cáo v4 **cho đối tác** (gọi bằng `key`); migrate MySQL → MongoDB | `portal-api` (Go) đọc MongoDB, tính số liệu, phân quyền, che dữ liệu, export; `portal-web` (Next.js) hiển thị báo cáo theo **tài khoản, chiến dịch, CTV, link**, **phân tích lượt click chi tiết**, nhật ký click, chất lượng traffic, export |
| Không làm | Giao diện người dùng; API riêng cho Portal | Ghi dữ liệu nghiệp vụ (`links`, `users`, `clicks`, `stats_*`); kết nối Redis / Kafka nghiệp vụ (Portal có Redis riêng chỉ để cache báo cáo); frontend không kết nối DB |
| Người dùng | Đối tác gọi API, người click link | PO, quản lý kinh doanh, đối soát CTV, (sau) đối tác |
| Thời lượng | ~5 tháng tới khi tắt hệ PHP | ~16–18 tuần, chạy song song, go-live khi Service ghi `stats_*` đầy đủ + dữ liệu thật trên staging |
| Nhóm | 2–3 backend, QA chung | 1–2 frontend, 1 backend Go, designer bán thời gian, QA chung |

Tài liệu dùng chung cho cả hai:

| Tài liệu | Nội dung |
|----------|----------|
| [`ENV_OBSERVABILITY_PLAN.md`](ENV_OBSERVABILITY_PLAN.md) | Môi trường dev / staging / production, Vault, Kafka log, OpenTelemetry, Redis |
| [`DATA_MIGRATION_PLAN.md`](DATA_MIGRATION_PLAN.md) | Convert MySQL → MongoDB (thuộc Service) |
| [`REWRITE_CHECKLIST.md`](REWRITE_CHECKLIST.md) | Checklist nghiệm thu — mỗi mục gắn project (bảng đầu file) |
| [`insomnia/am-shortlink-sale-api.insomnia.json`](insomnia/am-shortlink-sale-api.insomnia.json) | Collection API hiện tại — đầu vào viết OpenAPI legacy & golden test |

---

## 2. Bối cảnh & mục tiêu chung

- Stack PHP 5.5 / Lumen 5.1 hết vòng đời; ~30 triệu link, bảng click tăng liên tục; report quét bảng thô (tra CTV ~15s); schema không tái tạo được từ repo; nhiều hành vi rủi ro (xem `PLAN_SERVICE_API.md` §9).
- Báo cáo nằm rải rác (API cho đối tác + 2 trang HTML tĩnh dùng mật khẩu chung); nghiệp vụ không tự xem được số liệu.

| # | Mục tiêu | Project |
|---|----------|---------|
| G1 | Redirect p99 < 20ms, cache hit > 95% | Service |
| G2 | Tương thích ngược API v1/v2/v3 (golden test diff = 0) | Service |
| G3 | Dữ liệu thống kê (`stats_*`) cập nhật < 1 phút; API báo cáo đối tác < 500ms, tra CTV < 100ms | Service |
| G4 | Người dùng tự xem báo cáo theo tài khoản / chiến dịch / CTV / link, trang < 2s | Portal |
| G5 | Không mất dữ liệu khi chuyển hệ | Service |
| G6 | Đúng quyền xem dữ liệu — Portal: kiểm ở `portal-api` (Go) trên mọi truy vấn MongoDB; đối tác: kiểm ở Service | Cả hai |

---

## 3. Kiến trúc tổng

```
                         ┌──────────── Ingress / LB ─────────────┐
 Người click ──/sale|/lm/{code}──► redirect-svc ─┐
 Đối tác ──/api/v1|v2|v3|v4/*────► api-svc ──────┤                 Project 1: am-shortlink-service (Go) — GHI dữ liệu
                                                 ├──► MongoDB `am_shortlink` · Redis · Kafka ──► consumer ──► MongoDB `am_shortlink_report`
                                                 │                                                           (stats_*, link_params, stats_param_*)
                                                 │
 Người dùng nội bộ (trình duyệt)                 │   MongoDB (cùng cụm; Portal đọc secondary / node analytics)
   │                                             │        ▲ đọc am_shortlink + am_shortlink_report
   ▼                                             │        │ ghi collection portal_* trong am_shortlink_report
 portal-web (Next.js) ──BFF──► portal-api (Go) ──┼────────┘
   Project 2: am-shortlink-portal                │

 Tất cả ──OTLP──► otel-collector.fpt.net · log ──► Kafka isc-kafka01..03 ([pfx]am-shortlink-*-logs) · secret ◄── Vault ISC
```

---

## 4. Hợp đồng giữa 2 project

Portal **không gọi API của Service**. Hai project chia sẻ **MongoDB**: Service ghi, Portal đọc.

| Hạng mục | Quy ước |
|----------|---------|
| Nguồn sự thật | **Hợp đồng schema MongoDB** — collection, trường, kiểu, index trong `PLAN_SERVICE_API.md` §4, §5.4, §5.4b và thư mục `migrations/` của repo Service (có version) |
| Thay đổi schema | Service đổi tên / xoá trường, đổi kiểu, đổi index Portal dùng → báo Portal trong MR, giữ tương thích ít nhất 1 release; `portal-api` có test tích hợp chạy trên `migrations/` của Service |
| Quyền DB | `portal-api` dùng user MongoDB riêng: **chỉ đọc** `am_shortlink` và dữ liệu thống kê trong `am_shortlink_report`; **ghi** chỉ collection `portal_*` và `param_registry` (`PLAN_PORTAL_REPORT.md` §3.1) |
| Định nghĩa chỉ số | Một nguồn duy nhất: `PLAN_SERVICE_API.md` §5.2 — Service ghi `stats_*` đúng định nghĩa; `portal-api` tính / tổng hợp đúng định nghĩa đó |
| Tham số URL | Portal (admin) ghi `param_registry`; job backfill của Service đọc thay đổi, tính `stats_param_daily`, ghi tiến độ vào `param_registry.backfill` |
| Người dùng Portal | Bản ghi `users` (Service quản lý) có `portal_access`, `role` (`admin`/`user`/`viewer`), `viewer_accounts`; `portal-api` đọc để đăng nhập (bcrypt) và phân quyền |
| Phân quyền | Portal: `portal-api` thực thi (admin / user / viewer) trên mọi truy vấn; frontend chỉ ẩn/hiện UI. API đối tác: Service thực thi |
| Che dữ liệu | `portal-api` che SĐT CTV / IP theo vai trò trước khi trả cho frontend |
| Xác thực | ✅ **API đối tác (v1–v4): giữ gọi bằng `key` như cũ** (Service). ✅ **Portal: bắt buộc đăng nhập** qua `portal-api`; BFF Next.js giữ JWT người dùng ngắn hạn phía server. API key không dùng để vào Portal |
| Hợp đồng nội bộ Portal | OpenAPI 3.1 của `portal-api` (trong repo Portal) — frontend sinh client bằng `openapi-typescript` |
| Trace | `traceparent`: trình duyệt → `portal-web` → `portal-api` → MongoDB; Service: api-svc → Mongo / Kafka → consumer |
| Lỗi | `portal-api` trả HTTP status chuẩn + `application/problem+json` có `request_id`; frontend hiển thị `request_id` để báo hỗ trợ |

---

## 5. Lộ trình chung

```
Tuần:           1   2   3   4   5   6   7   8   9  10  11  12  13  14  15  16  17  18  19  20  21
Service  P0 ███
         P1     ██████                     ← chốt schema MongoDB + migrations/ (M-1)
         P2           ██████
         P3                 ████████
         P4                         ██████████   ← stats_*, link_params, backfill tham số
         P6 migrate & cutover                               ██████
         P7 ổn định                                                  ████████
Portal   P0 thiết kế ███
         P1 nền tảng (web + api) ██████     ← cần schema MongoDB chốt (cuối Service P1); chạy trên DB seed
         P2 R1–R3                    ██████
         P3 R4–R6                          ██████
         P3b Click Explorer + tham số            ██████
         P4 R7–R8 + quản lý tham số                    ████
         P5 UAT & go-live                                          ████████  ← cần dữ liệu thật staging (Service P6)
```

Mốc phối hợp:

| Mốc | Khi nào | Bên giao | Bên nhận |
|-----|---------|----------|----------|
| M-1 Hợp đồng schema MongoDB (`migrations/` có version) + trường Portal trong `users` | Cuối Service Phase 1 (~tuần 5) | Service | Portal bắt đầu backend P1 (DB seed đúng schema) |
| M-2 User MongoDB `portal_api` (quyền tối thiểu) trên dev / staging | ~tuần 6 | Ops | Portal |
| M-3 `stats_*` + `clicks` làm giàu được ghi trên dev | Giữa Service Phase 4 (~tuần 14) | Service | Portal chạy trên dữ liệu Service dev |
| M-4 Backfill tham số đọc `param_registry` | Cuối Service Phase 4 (~tuần 16) | Service | Portal P4 (quản lý tham số) |
| M-5 Dữ liệu thật trên staging | Service Phase 6 (~tuần 17–18) | Service | Portal UAT |
| M-6 Go-live Portal + tắt `/report-chart`, `/report-users` | Sau UAT | Portal | Nghiệp vụ |

---

## 6. Quyết định cấp chương trình

| # | Câu hỏi | Đề xuất |
|---|---------|---------|
| C1 | Tách 2 repo hay monorepo | 2 repo (`am-shortlink-service`, `am-shortlink-portal`), vòng đời deploy độc lập; hợp đồng qua **schema MongoDB** |
| C2 | Portal giai đoạn 1 chỉ báo cáo hay cả quản trị | Chỉ báo cáo; quản trị link/user/API key/prefix sang giai đoạn 2 (`PLAN_PORTAL_REPORT.md` §9) — các thao tác ghi nghiệp vụ đó vẫn đi qua Service |
| C3 | Ai sở hữu định nghĩa chỉ số | PO duyệt, ghi trong `PLAN_SERVICE_API.md` §5.2; Service ghi `stats_*` và `portal-api` tính theo đúng định nghĩa đó |
| C4 | Portal lấy dữ liệu thế nào | ✅ **Đã chốt (P-D10): backend Portal bằng Go kết nối trực tiếp MongoDB**, frontend Next.js; không qua API của Service |
| C5 | Quyết định chi tiết | Service: D2–D31; Portal: P-D1–P-D12; Môi trường: E1–E12; Migrate: M1–M11 |

---

## 7. Vai trò chung

| Vai trò | Service | Portal |
|---------|---------|--------|
| Tech lead | Kiến trúc, ADR, spec OpenAPI (API đối tác), hợp đồng schema MongoDB | Review hợp đồng schema, kiến trúc `portal-api` + BFF, OpenAPI `portal-api` |
| Dev | 2–3 backend Go | 1 backend Go (`portal-api`), 1–2 frontend Next.js |
| QA | Golden test, đối chiếu số liệu, load test | Test tích hợp `portal-api`, E2E, đối chiếu số liệu màn hình, UAT |
| Ops | Mongo / Redis / Kafka / Vault / CI/CD / cutover | Domain, CI/CD (2 image), Vault, user MongoDB `portal_api` |
| PO / nghiệp vụ | Chốt quy tắc nghiệp vụ, chỉ số | Chốt màn hình, nghiệm thu Portal |

---

## 8. Việc tiếp theo

- [ ] Duyệt cách chia 2 project (C1, C2) và kiến trúc Portal đọc trực tiếp MongoDB (C4)
- [ ] Viết ADR "Rewrite sang Go + MongoDB; Portal báo cáo = Next.js + backend Go đọc trực tiếp MongoDB" trong `am-shortlink-knowledge/10-adr/`; thêm `am-shortlink-service`, `portal-api`, `portal-web` vào `am-shortlink-knowledge/_meta/manifest.yml`
- [ ] Tạo 2 repo `am-shortlink-service`, `am-shortlink-portal`
- [ ] Service: việc tiếp theo trong `PLAN_SERVICE_API.md` §12
- [ ] Portal: việc tiếp theo trong `PLAN_PORTAL_REPORT.md` §14
- [ ] Gỡ `.env.Staging`, `.env.Production` khỏi git repo hiện tại + rotate secret (E5 — làm ngay)
