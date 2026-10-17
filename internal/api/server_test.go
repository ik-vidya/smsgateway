package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ik-vidya/smsgateway/internal/config"
	"github.com/ik-vidya/smsgateway/internal/storage"
)

const testKey = "test-api-key-12345"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := &config.Config{
		HTTPAddr:     "127.0.0.1:0",
		APIKeys:      []string{testKey},
		DBPath:       ":memory:",
		SerialPort:   "none",
		ModemProfile: "generic",
		LogLevel:     "error",
		LogFormat:    "text",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, store, nil, nil, logger)
}

func doRequest(t *testing.T, s *Server, method, path string, body any, auth string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			rdr = strings.NewReader(v)
		default:
			buf, _ := json.Marshal(v)
			rdr = bytes.NewReader(buf)
		}
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("X-API-Key", auth)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	return w
}

func TestHealthzNoAuth(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/healthz", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, тело: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status=%q", resp["status"])
	}
}

func TestAuthMissing(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/api/v1/status", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, ожидалось 401", w.Code)
	}
}

func TestAuthInvalid(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/api/v1/status", nil, "wrong-key")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, ожидалось 401", w.Code)
	}
}

func TestAuthBearerHeader(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, тело: %s", w.Code, w.Body.String())
	}
}

func TestSendMessage(t *testing.T) {
	s := newTestServer(t)
	body := SendRequest{To: "+37121234567", Text: "Привет"}
	w := doRequest(t, s, "POST", "/api/v1/messages", body, testKey)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, тело: %s", w.Code, w.Body.String())
	}
	var resp SendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID == 0 {
		t.Error("id не установлен")
	}
	if resp.Status != "queued" {
		t.Errorf("status=%q, ожидалось queued", resp.Status)
	}
}

func TestSendMessageValidation(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name string
		body SendRequest
	}{
		{"empty to", SendRequest{To: "", Text: "x"}},
		{"to without +", SendRequest{To: "37121234567", Text: "x"}},
		{"empty text", SendRequest{To: "+37121234567", Text: ""}},
		{"text too long", SendRequest{To: "+37121234567", Text: strings.Repeat("a", 5000)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doRequest(t, s, "POST", "/api/v1/messages", c.body, testKey)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status=%d, ожидалось 400, тело: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGetMessage(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "POST", "/api/v1/messages",
		SendRequest{To: "+37121234567", Text: "Hello"}, testKey)
	var send SendResponse
	_ = json.Unmarshal(w.Body.Bytes(), &send)

	w = doRequest(t, s, "GET", "/api/v1/messages/"+itoa(send.ID), nil, testKey)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, тело: %s", w.Code, w.Body.String())
	}
	var dto MessageDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dto.Peer != "+37121234567" || dto.Text != "Hello" {
		t.Errorf("не совпадает: %+v", dto)
	}
	if dto.Status != "queued" {
		t.Errorf("status=%q", dto.Status)
	}
}

func TestGetMessageNotFound(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/api/v1/messages/99999", nil, testKey)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, ожидалось 404", w.Code)
	}
}

func TestGetMessageBadID(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/api/v1/messages/abc", nil, testKey)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, ожидалось 400", w.Code)
	}
}

func TestListMessages(t *testing.T) {
	s := newTestServer(t)
	for i := 0; i < 5; i++ {
		doRequest(t, s, "POST", "/api/v1/messages",
			SendRequest{To: "+3712123450" + itoa(int64(i)), Text: "msg"},
			testKey)
	}
	w := doRequest(t, s, "GET", "/api/v1/messages?limit=3", nil, testKey)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp ListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("total=%d", resp.Total)
	}
	if len(resp.Items) != 3 {
		t.Errorf("items=%d", len(resp.Items))
	}
}

func TestListMessagesFilterStatus(t *testing.T) {
	s := newTestServer(t)
	doRequest(t, s, "POST", "/api/v1/messages",
		SendRequest{To: "+37121234567", Text: "x"}, testKey)

	w := doRequest(t, s, "GET", "/api/v1/messages?status=delivered", nil, testKey)
	var resp ListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Errorf("total=%d, ожидалось 0", resp.Total)
	}

	w = doRequest(t, s, "GET", "/api/v1/messages?status=queued", nil, testKey)
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("total=%d, ожидалось 1", resp.Total)
	}
}

func TestListMessagesBadSince(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/api/v1/messages?since=not-a-date", nil, testKey)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, ожидалось 400", w.Code)
	}
}

func TestDeleteMessage(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "POST", "/api/v1/messages",
		SendRequest{To: "+37121234567", Text: "x"}, testKey)
	var send SendResponse
	_ = json.Unmarshal(w.Body.Bytes(), &send)

	w = doRequest(t, s, "DELETE", "/api/v1/messages/"+itoa(send.ID), nil, testKey)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d, тело: %s", w.Code, w.Body.String())
	}

	w = doRequest(t, s, "GET", "/api/v1/messages/"+itoa(send.ID), nil, testKey)
	if w.Code != http.StatusNotFound {
		t.Fatalf("после удаления status=%d, ожидалось 404", w.Code)
	}
}

func TestDeleteMessageNotFound(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "DELETE", "/api/v1/messages/99999", nil, testKey)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, ожидалось 404", w.Code)
	}
}

func TestStatus(t *testing.T) {
	s := newTestServer(t)
	doRequest(t, s, "POST", "/api/v1/messages",
		SendRequest{To: "+37121234567", Text: "x"}, testKey)

	w := doRequest(t, s, "GET", "/api/v1/status", nil, testKey)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status=%q", resp.Status)
	}
	if resp.Messages["queued"] != 1 {
		t.Errorf("queued=%d, ожидалось 1", resp.Messages["queued"])
	}
	if resp.Database == nil || !resp.Database.Healthy {
		t.Error("database не healthy")
	}
	if resp.Modem != nil {
		t.Errorf("при svc=nil modem должен быть nil, получено: %+v", resp.Modem)
	}
}

func TestTestSMSWithoutService(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "POST", "/api/v1/test-sms",
		TestSMSRequest{Text: "test"}, testKey)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, ожидалось 503", w.Code)
	}
}

func TestDiagnosticsWithoutService(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/api/v1/diagnostics", nil, testKey)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, ожидалось 503", w.Code)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

// ==================== /readyz ====================

func TestReadyzHealthy(t *testing.T) {
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/readyz", nil, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, тело: %s", w.Code, w.Body.String())
	}
	var resp readyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "ready" {
		t.Errorf("status=%q, ожидалось ready", resp.Status)
	}
	if c, ok := resp.Checks["database"]; !ok || !c.OK {
		t.Errorf("checks.database=%v, ожидалось ok=true", c)
	}
}

func TestReadyzModemNotConfigured(t *testing.T) {
	// newTestServer создаёт cfg с SerialPort="none" — это
	// штатный режим «без модема». /readyz должен вернуть 200
	// с warnings про неконфигурированный модем.
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/readyz", nil, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, тело: %s", w.Code, w.Body.String())
	}
	var resp readyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, warn := range resp.Warnings {
		if strings.Contains(warn, "modem not configured") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings=%v, ожидалось упоминание 'modem not configured'",
			resp.Warnings)
	}
}

func TestReadyzDBUnavailable(t *testing.T) {
	// Закрываем БД — PingContext должен упасть, /readyz вернёт
	// 503 not_ready.
	s := newTestServer(t)
	if err := s.store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	w := doRequest(t, s, "GET", "/readyz", nil, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, ожидалось 503, тело: %s",
			w.Code, w.Body.String())
	}
	var resp readyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "not_ready" {
		t.Errorf("status=%q, ожидалось not_ready", resp.Status)
	}
	if c, ok := resp.Checks["database"]; !ok || c.OK {
		t.Errorf("checks.database=%v, ожидалось ok=false", c)
	}
}

func TestReadyzNoAuthRequired(t *testing.T) {
	// /readyz — без авторизации, как /healthz.
	s := newTestServer(t)
	w := doRequest(t, s, "GET", "/readyz", nil, "") // без ключа
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d — /readyz не должен требовать авторизацию",
			w.Code)
	}
}
