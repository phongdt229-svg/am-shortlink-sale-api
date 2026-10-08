// apikey: cấp / xoay / thu hồi API key (thay `php artisan shortlink:api-key`).
//
//	apikey -user <username>                          cấp key mới (key cũ vô hiệu ngay)
//	apikey -user <username> -create -email <email>   tạo user mới + cấp key
//	apikey -user <username> -quota 120               đổi api_quota (link / phút, âm = không giới hạn)
//	apikey -user <username> -revoke                  tắt API (api_active = false)
//	apikey -user <username> -show                    xem trạng thái
//	apikey -user <username> -import <key>            nhập key có sẵn (migrate từ users.api_key MySQL)
//
// Key chỉ in ra MỘT lần; DB chỉ lưu SHA-256 (api_keys.key_hash).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"am-shortlink-service/internal/app"
	"am-shortlink-service/internal/domain"
	"am-shortlink-service/internal/shortcode"
	"am-shortlink-service/internal/store"
)

func main() {
	username := flag.String("user", "", "username")
	create := flag.Bool("create", false, "tạo user nếu chưa có")
	email := flag.String("email", "", "email (bắt buộc với -create)")
	quota := flag.String("quota", "", "api_quota (link / phút)")
	revoke := flag.Bool("revoke", false, "tắt API của user")
	show := flag.Bool("show", false, "chỉ xem trạng thái")
	importKey := flag.String("import", "", "nhập key có sẵn")
	flag.Parse()
	if *username == "" {
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a := app.Init(ctx, "am-shortlink-apikey")
	defer a.Close(context.Background())
	st := a.Store

	u, err := st.UserByUsername(ctx, *username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		die("%v", err)
	}
	if *show {
		if u == nil {
			die("Không tìm thấy user '%s'.", *username)
		}
		has, _ := st.HasActiveAPIKey(ctx, u.ID)
		fmt.Printf("username   : %s\nemail      : %s\nactive     : %v\napi_active : %v\napi_quota  : %d\ncó api_key : %v\n",
			u.Username, u.Email, u.Active, u.APIActive, u.APIQuota, has)
		return
	}
	if u == nil {
		if !*create {
			die("Không tìm thấy user '%s'. Thêm -create -email <email> để tạo mới.", *username)
		}
		if *email == "" {
			die("Thiếu -email, bắt buộc khi tạo user mới.")
		}
		u = &domain.User{Username: *username, Email: *email, Role: "user", Active: true, APIActive: true,
			APIQuota: 60, RandomKeyLength: a.Cfg.API.RandomKeyLength}
		if err := st.CreateUser(ctx, u); err != nil {
			die("tạo user: %v", err)
		}
		fmt.Printf("Đã tạo user '%s' (active, api_active).\n", u.Username)
	}
	if *revoke {
		if err := st.UpdateUser(ctx, u.ID, bson.D{{Key: "api_active", Value: false}}); err != nil {
			die("%v", err)
		}
		fmt.Printf("Đã tắt quyền gọi API của '%s'.\n", u.Username)
		return
	}
	set := bson.D{{Key: "api_active", Value: true}}
	if *quota != "" {
		q, err := strconv.Atoi(*quota)
		if err != nil {
			die("-quota phải là số")
		}
		set = append(set, bson.E{Key: "api_quota", Value: q})
	}
	if err := st.UpdateUser(ctx, u.ID, set); err != nil {
		die("%v", err)
	}
	if *importKey != "" {
		if err := st.ImportAPIKey(ctx, u.ID, *importKey); err != nil {
			die("%v", err)
		}
		fmt.Printf("Đã nhập api_key có sẵn cho '%s'.\n", u.Username)
		return
	}
	key := shortcode.RandomHex(a.Cfg.API.APIKeyLength)
	if err := st.RotateAPIKey(ctx, u.ID, key); err != nil {
		die("%v", err)
	}
	fmt.Printf("api_key cho '%s' (key cũ — nếu có — đã bị vô hiệu):\n%s\n\n", u.Username, key)
	fmt.Println("Gửi cho đối tác qua kênh bảo mật. Không commit, không dán vào ticket/chat.")
	if !u.Active {
		fmt.Println("CẢNH BÁO: user đang active = false — API sẽ từ chối key cho tới khi kích hoạt.")
	}
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
