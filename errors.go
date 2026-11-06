package pdfapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Сигнальные ошибки для errors.Is. Конкретная ошибка API — *Error, у которой
// есть Code, Status и Message (см. sdk/SPEC.md):
//
//	var apiErr *pdfapi.Error
//	if errors.Is(err, pdfapi.ErrQuotaExceeded) { ... }
//	if errors.As(err, &apiErr) { log.Println(apiErr.Code, apiErr.Status) }
var (
	ErrAuthentication   = errors.New("pdfapi: ключ API не принят")           // 401
	ErrPermissionDenied = errors.New("pdfapi: нет доступа")                  // 403
	ErrQuotaExceeded    = errors.New("pdfapi: квота исчерпана")              // 402
	ErrInvalidRequest   = errors.New("pdfapi: неверный запрос")              // 400
	ErrPayloadTooLarge  = errors.New("pdfapi: тело запроса слишком большое") // 413, это и ErrInvalidRequest
	ErrRateLimit        = errors.New("pdfapi: слишком много запросов")       // 429
	ErrRender           = errors.New("pdfapi: документ не отрендерился")     // 502
	ErrRenderTimeout    = errors.New("pdfapi: рендер не уложился во время")  // 504, это и ErrRender
	ErrUnavailable      = errors.New("pdfapi: сервис временно недоступен")   // 503
	ErrConnection       = errors.New("pdfapi: нет связи с API")              // сеть
	ErrTimeout          = errors.New("pdfapi: API не ответил вовремя")       // это и ErrConnection
	ErrInvalidArgument  = errors.New("pdfapi: неверный аргумент")            // до запроса
)

// Наследование видов ошибок, как в SDK на других языках.
var parents = map[error]error{
	ErrPayloadTooLarge: ErrInvalidRequest,
	ErrRenderTimeout:   ErrRender,
	ErrTimeout:         ErrConnection,
}

// Error — ошибка API или соединения.
type Error struct {
	Code       string        // код из ответа API: quota_exceeded, render_failed, …
	Status     int           // HTTP-статус; 0 — ответа не было (сеть, таймаут)
	Message    string        // текст сервера или общая фраза по-русски
	RetryAfter time.Duration // для ErrRateLimit — через сколько можно снова

	kind  error
	cause error
}

func (e *Error) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("pdfapi: %s (HTTP %d, %s)", e.Message, e.Status, e.Code)
	}
	return fmt.Sprintf("pdfapi: %s (%s)", e.Message, e.Code)
}

// Is — errors.Is(err, pdfapi.ErrRender) верно и для ErrRenderTimeout.
func (e *Error) Is(target error) bool {
	for k := e.kind; k != nil; k = parents[k] {
		if k == target {
			return true
		}
	}
	return false
}

func (e *Error) Unwrap() error { return e.cause }

// Kind — вид ошибки (одна из Err*); nil — ошибка API без отдельного вида.
func (e *Error) Kind() error { return e.kind }

var kindByCode = map[string]error{
	"missing_api_key":     ErrAuthentication,
	"invalid_api_key":     ErrAuthentication,
	"email_not_verified":  ErrPermissionDenied,
	"source_not_allowed":  ErrPermissionDenied,
	"quota_exceeded":      ErrQuotaExceeded,
	"invalid_request":     ErrInvalidRequest,
	"url_not_allowed":     ErrInvalidRequest,
	"payload_too_large":   ErrPayloadTooLarge,
	"rate_limit_exceeded": ErrRateLimit,
	"render_failed":       ErrRender,
	"render_timeout":      ErrRenderTimeout,
	"unavailable":         ErrUnavailable,
}

// Ответ не от API (Caddy отдаёт 413 текстом, прокси — 502 в HTML, сайт — 404
// страницей, Fastify — {"error": "Bad Request"}): код по статусу.
var codeByStatus = map[int]string{
	400: "invalid_request", 401: "invalid_api_key", 402: "quota_exceeded", 403: "source_not_allowed",
	404: "not_found", 413: "payload_too_large", 429: "rate_limit_exceeded", 502: "render_failed",
	503: "unavailable", 504: "render_timeout",
}

// apiCode — код API вроде quota_exceeded, а не название статуса вроде «Bad Request».
var apiCode = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var messages = map[string]string{
	"missing_api_key":     "Не передан ключ API.",
	"invalid_api_key":     "Неверный или отозванный ключ API.",
	"email_not_verified":  "Почта аккаунта не подтверждена.",
	"source_not_allowed":  "Этот формат недоступен на вашем тарифе.",
	"quota_exceeded":      "Лимит страниц исчерпан.",
	"invalid_request":     "Некорректный запрос.",
	"url_not_allowed":     "Адрес ведёт во внутреннюю сеть и запрещён.",
	"payload_too_large":   "Тело запроса больше 5 МБ.",
	"rate_limit_exceeded": "Слишком много запросов.",
	"render_failed":       "Не удалось создать PDF.",
	"render_timeout":      "Рендер не уложился в 30 секунд.",
	"unavailable":         "Сервис временно недоступен.",
	"server_error":        "Внутренняя ошибка сервера.",
	"not_found":           "Адрес не найден (HTTP 404). Проверьте базовый адрес: он ведёт на API, например https://pdfapi.ru/api.",
}

func retryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	sec, err := strconv.ParseFloat(v, 64)
	if err != nil || sec < 0 {
		return 0, false // дата в HTTP-формате — API так не отвечает
	}
	return time.Duration(sec * float64(time.Second)), true
}

type errorBody struct {
	Error         any      `json:"error"`
	Message       any      `json:"message"`
	RetryAfterSec *float64 `json:"retry_after_sec"`
}

// retryAfterSeconds — через сколько можно снова: заголовок Retry-After, иначе retry_after_sec из тела.
func retryAfterSeconds(h http.Header, body []byte) (time.Duration, bool) {
	if d, ok := retryAfter(h); ok {
		return d, true
	}
	var data errorBody
	if json.Unmarshal(body, &data) == nil && data.RetryAfterSec != nil && *data.RetryAfterSec >= 0 {
		return time.Duration(*data.RetryAfterSec * float64(time.Second)), true
	}
	return 0, false
}

func errorFromResponse(status int, h http.Header, body []byte) *Error {
	var data errorBody
	_ = json.Unmarshal(body, &data) // не JSON — разберёмся по статусу

	code, _ := data.Error.(string)
	if !apiCode.MatchString(code) {
		if c, ok := codeByStatus[status]; ok {
			code = c
		} else if status >= 500 {
			code = "server_error"
		} else {
			code = "unknown_error"
		}
	}
	message, _ := data.Message.(string)
	if strings.TrimSpace(message) == "" {
		if m, ok := messages[code]; ok {
			message = m
		} else {
			message = fmt.Sprintf("Ошибка API (HTTP %d).", status)
		}
	}
	e := &Error{Code: code, Status: status, Message: message, kind: kindByCode[code]}
	if e.kind == ErrRateLimit {
		e.RetryAfter, _ = retryAfterSeconds(h, body)
	}
	return e
}
