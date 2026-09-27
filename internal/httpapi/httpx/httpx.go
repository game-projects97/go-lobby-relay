package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/time/rate"
)

const MaxRequestBodyBytes = 64 << 10

func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:                         addr,
		Handler:                      handler,
		DisableGeneralOptionsHandler: true,
		MaxHeaderBytes:               16 << 10,
		ReadHeaderTimeout:            2 * time.Second,
		ReadTimeout:                  5 * time.Second,
		WriteTimeout:                 5 * time.Second,
		IdleTimeout:                  30 * time.Second,
	}
}

type Admission struct {
	limiter   *rate.Limiter
	semaphore chan struct{}
	now       func() time.Time
}

func NewAdmission(requestRate rate.Limit, burst, maxConcurrent int, now func() time.Time) *Admission {
	if now == nil {
		now = time.Now
	}
	return &Admission{
		limiter:   rate.NewLimiter(requestRate, burst),
		semaphore: make(chan struct{}, maxConcurrent),
		now:       now,
	}
}

func (admission *Admission) Enter(writer http.ResponseWriter) (release func(), ok bool) {
	if !admission.limiter.AllowN(admission.now(), 1) {
		WriteError(writer, http.StatusTooManyRequests, "rate_limited", "request rate or concurrency limit exceeded")
		return nil, false
	}
	select {
	case admission.semaphore <- struct{}{}:
		return func() { <-admission.semaphore }, true
	default:
		WriteError(writer, http.StatusTooManyRequests, "rate_limited", "request rate or concurrency limit exceeded")
		return nil, false
	}
}

func ReadJSONBody(writer http.ResponseWriter, request *http.Request) ([]byte, bool) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		WriteError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return nil, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, MaxRequestBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteError(writer, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 65536 bytes")
		} else {
			WriteInvalid(writer)
		}
		return nil, false
	}
	return body, true
}

func DecodeExact(writer http.ResponseWriter, request *http.Request, target any, keys ...string) bool {
	body, ok := ReadJSONBody(writer, request)
	if !ok {
		return false
	}
	var object map[string]json.RawMessage
	if !HasUniqueFields(body) || json.Unmarshal(body, &object) != nil || !HasExactKeys(object, keys...) {
		WriteInvalid(writer)
		return false
	}
	if !DecodeStrict(body, target) {
		WriteInvalid(writer)
		return false
	}
	return true
}

func DecodeStrict(body []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func HasUniqueFields(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if !scanValue(decoder) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func scanValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, keyOK := keyToken.(string)
			if err != nil || !keyOK {
				return false
			}
			if _, exists := seen[key]; exists {
				return false
			}
			seen[key] = struct{}{}
			if !scanValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !scanValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

func HasExactKeys(object map[string]json.RawMessage, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func HasOnlyQueryKeys(query url.Values, allowed ...string) bool {
	allowedSet := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = true
	}
	for key, values := range query {
		if !allowedSet[key] || len(values) != 1 {
			return false
		}
	}
	return true
}

func RequestHasBody(request *http.Request) bool {
	if request.Body == nil || request.ContentLength == 0 {
		return false
	}
	if request.ContentLength > 0 {
		return true
	}
	var oneByte [1]byte
	read, err := request.Body.Read(oneByte[:])
	return read != 0 || err != io.EOF
}

func NotifyFatal(writer http.ResponseWriter, fatal func()) {
	if fatal == nil {
		return
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	fatal()
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func WriteInvalid(writer http.ResponseWriter) {
	WriteError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
}

func WriteMethodNotAllowed(writer http.ResponseWriter, allow string) {
	writer.Header().Set("Allow", allow)
	WriteError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func WriteError(writer http.ResponseWriter, status int, code, message string) {
	response := errorResponse{}
	response.Error.Code = code
	response.Error.Message = message
	WriteJSON(writer, status, response)
}

func WriteJSON(writer http.ResponseWriter, status int, value any) {
	encoded, _ := json.Marshal(value)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(encoded, '\n'))
}
