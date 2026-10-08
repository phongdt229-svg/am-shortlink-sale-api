package legacy

import "net/http"

// Hệ cũ (Lumen) trả trang HTML lỗi cho route không tồn tại.
const notFoundHTML = `<!doctype html><html lang="vi"><head><meta charset="utf-8"><title>404</title></head>` +
	`<body><h1>404</h1><p>This page could not be found.</p></body></html>`

func NotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(notFoundHTML))
}

func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = w.Write([]byte(`<!doctype html><html lang="vi"><head><meta charset="utf-8"><title>405</title></head><body><h1>405</h1><p>Method Not Allowed</p></body></html>`))
}
