package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHasUniqueFieldsRejectsDuplicateKeysAtAnyDepth(t *testing.T) {
	for body, want := range map[string]bool{
		`{"a":1,"b":[{"c":2}]}`: true,
		`{"a":1,"a":2}`:         false,
		`{"a":[{"c":1,"c":2}]}`: false,
		`{"a":1} {"b":2}`:       false,
		`{"a":`:                 false,
		`"scalar"`:              true,
	} {
		if got := HasUniqueFields([]byte(body)); got != want {
			t.Fatalf("HasUniqueFields(%s) = %v, want %v", body, got, want)
		}
	}
}

func TestDecodeExactRequiresJSONExactKeysAndSingleValue(t *testing.T) {
	cases := []struct {
		name, contentType, body string
		status                  int
	}{
		{"valid", "application/json; charset=utf-8", `{"revision":1}`, 0},
		{"media type", "text/plain", `{"revision":1}`, http.StatusUnsupportedMediaType},
		{"missing key", "application/json", `{}`, http.StatusBadRequest},
		{"extra key", "application/json", `{"revision":1,"x":2}`, http.StatusBadRequest},
		{"trailing value", "application/json", `{"revision":1}{}`, http.StatusBadRequest},
		{"too large", "application/json", `{"revision":"` + strings.Repeat("a", MaxRequestBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, test := range cases {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
		request.Header.Set("Content-Type", test.contentType)
		recorder := httptest.NewRecorder()
		var target struct {
			Revision uint64 `json:"revision"`
		}
		ok := DecodeExact(recorder, request, &target, "revision")
		if ok != (test.status == 0) || test.status != 0 && recorder.Code != test.status {
			t.Fatalf("%s: DecodeExact() = %v, status %d, want %d", test.name, ok, recorder.Code, test.status)
		}
	}
}

func TestAdmissionRejectsWhenRateOrConcurrencyIsExhausted(t *testing.T) {
	now := time.Unix(0, 0)
	admission := NewAdmission(1, 2, 1, func() time.Time { return now })
	release, ok := admission.Enter(httptest.NewRecorder())
	if !ok {
		t.Fatal("first Enter() rejected")
	}
	recorder := httptest.NewRecorder()
	if _, ok := admission.Enter(recorder); ok || recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent Enter() = %v, status %d", ok, recorder.Code)
	}
	release()
	recorder = httptest.NewRecorder()
	if _, ok := admission.Enter(recorder); ok || recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited Enter() = %v, status %d", ok, recorder.Code)
	}
}
