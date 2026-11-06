// Package pdfapi — официальный Go SDK pdfapi: HTML, URL, Markdown и Office в PDF
// одним вызовом.
//
//	client, err := pdfapi.NewClient() // ключ из PDFAPI_KEY
//	if err != nil { log.Fatal(err) }
//	pdf, err := client.HTML(ctx, "<h1>Счёт №42</h1>", &pdfapi.PageOptions{MarginTop: "20mm"})
//	if err != nil { log.Fatal(err) }
//	_ = pdf.Save("invoice.pdf")
//
// Поведение одинаково с SDK на других языках — см. sdk/SPEC.md.
package pdfapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version — версия SDK.
const Version = "0.1.0"

const (
	DefaultBaseURL    = "https://pdfapi.ru/api"
	DefaultTimeout    = 40 * time.Second // рендер — до 30 с, плюс запас
	DefaultMaxRetries = 2
	// OfficeMaxBytes — предел файла для Office: тело запроса ограничено 5 МБ,
	// а base64 раздувает файл на треть.
	OfficeMaxBytes = 3_700_000
	maxRetryAfter  = 60 * time.Second
	maxWaitDelay   = 10 * time.Second
)

var (
	// Число и единица, как понимает рендер. Без единицы сервер считает дюймами,
	// но "20" почти всегда задумано как миллиметры.
	sizeRe = regexp.MustCompile(`^(\d+(\.\d*)?|\.\d+)(pt|px|in|mm|cm|pc)$`)
	// WaitDelay — как на сервере: число и s или ms, не больше 10 секунд.
	waitDelayRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)(ms|s)$`)
	httpURLRe   = regexp.MustCompile(`(?i)^https?://\S`)
	// docx, xlsx и pptx — ZIP-архивы, начинаются с «PK\x03\x04».
	zipMagic = []byte("PK\x03\x04")
)

// Client — клиент pdfapi. Безопасен для одновременного использования из
// нескольких горутин; создайте один и переиспользуйте.
type Client struct {
	apiKey     string
	baseURL    string
	timeout    time.Duration
	maxRetries int
	http       *http.Client
	userAgent  string
}

// Option — настройка клиента для NewClient.
type Option func(*Client)

// WithAPIKey задаёт ключ API (иначе — переменная окружения PDFAPI_KEY).
func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }

// WithBaseURL задаёт адрес API (иначе — PDFAPI_BASE_URL или https://pdfapi.ru/api).
func WithBaseURL(url string) Option { return func(c *Client) { c.baseURL = url } }

// WithTimeout задаёт таймаут одного запроса (по умолчанию 40 с). Для одного
// вызова — context.WithTimeout: действует меньший из двух.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithMaxRetries — сколько раз повторять 429, 503 и ошибки соединения (по умолчанию 2).
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// WithHTTPClient — свой *http.Client (прокси, сертификаты). Таймаут клиента
// SDK всё равно ставит на каждый запрос через context.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// NewClient создаёт клиент. Без ключа — ошибка сразу, а не 401 при первом запросе.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		apiKey:     os.Getenv("PDFAPI_KEY"),
		baseURL:    os.Getenv("PDFAPI_BASE_URL"),
		timeout:    DefaultTimeout,
		maxRetries: DefaultMaxRetries,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.apiKey = strings.TrimSpace(c.apiKey)
	if c.apiKey == "" {
		return nil, fmt.Errorf("%w: не задан ключ API — WithAPIKey(...) или переменная окружения PDFAPI_KEY", ErrInvalidArgument)
	}
	if c.timeout <= 0 {
		return nil, fmt.Errorf("%w: таймаут %v — нужен больше нуля", ErrInvalidArgument, c.timeout)
	}
	if c.maxRetries < 0 {
		return nil, fmt.Errorf("%w: повторов %d — нужно от 0", ErrInvalidArgument, c.maxRetries)
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	c.baseURL = strings.TrimRight(c.baseURL, "/")
	if c.http == nil {
		c.http = &http.Client{}
	}
	c.userAgent = fmt.Sprintf("pdfapi-go/%s (%s)", Version, runtime.Version())
	return c, nil
}

// BaseURL — адрес API, к которому обращается клиент.
func (c *Client) BaseURL() string { return c.baseURL }

// String не показывает ключ.
func (c *Client) String() string { return fmt.Sprintf("pdfapi.Client{baseURL: %q}", c.baseURL) }

// ------------------------------------------------------------------ параметры

// PageOptions — параметры страницы. Пустые строки и nil не отправляются.
// Размеры — число и единица pt, px, in, mm, cm или pc: "20mm", "8.27in";
// WaitDelay — до 10 секунд: "2s", "500ms".
type PageOptions struct {
	Landscape    *bool  `json:"landscape,omitempty"`
	PaperWidth   string `json:"paperWidth,omitempty"`
	PaperHeight  string `json:"paperHeight,omitempty"`
	MarginTop    string `json:"marginTop,omitempty"`
	MarginBottom string `json:"marginBottom,omitempty"`
	MarginLeft   string `json:"marginLeft,omitempty"`
	MarginRight  string `json:"marginRight,omitempty"`
	WaitDelay    string `json:"waitDelay,omitempty"`
}

// Bool — указатель на bool для PageOptions.Landscape: pdfapi.Bool(true).
func Bool(v bool) *bool { return &v }

func (o *PageOptions) toMap() (map[string]any, error) {
	out := map[string]any{}
	if o == nil {
		return out, nil
	}
	if o.Landscape != nil {
		out["landscape"] = *o.Landscape
	}
	for _, f := range [...]struct{ name, value string }{
		{"paperWidth", o.PaperWidth}, {"paperHeight", o.PaperHeight}, {"marginTop", o.MarginTop},
		{"marginBottom", o.MarginBottom}, {"marginLeft", o.MarginLeft}, {"marginRight", o.MarginRight},
		{"waitDelay", o.WaitDelay},
	} {
		if f.value == "" {
			continue
		}
		if f.name == "waitDelay" {
			if !validWaitDelay(f.value) {
				return nil, fmt.Errorf(`%w: waitDelay %q — нужно от 0 до 10 секунд в s или ms, например "2s" или "500ms"`,
					ErrInvalidArgument, f.value)
			}
		} else if !sizeRe.MatchString(f.value) {
			// Иначе рендер падает с 502, и опечатка выглядит как сбой сервиса.
			return nil, fmt.Errorf(`%w: %s %q — нужно число с единицами pt, px, in, mm, cm или pc без пробела, например "20mm"`,
				ErrInvalidArgument, f.name, f.value)
		}
		out[f.name] = f.value
	}
	return out, nil
}

func validWaitDelay(v string) bool {
	m := waitDelayRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return false
	}
	if m[2] == "s" {
		n *= 1000
	}
	return time.Duration(n*float64(time.Millisecond)) <= maxWaitDelay
}

func textBody(source, value string, opts *PageOptions) (map[string]any, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%w: %s — пустое содержимое", ErrInvalidArgument, source)
	}
	if source == "url" {
		value = strings.TrimSpace(value) // пробелы по краям рендер не прощает
		if !httpURLRe.MatchString(value) {
			return nil, fmt.Errorf("%w: url %q — нужен полный адрес с http:// или https://, например https://example.com",
				ErrInvalidArgument, value)
		}
	}
	options, err := opts.toMap()
	if err != nil {
		return nil, err
	}
	body := map[string]any{"source": source, source: value}
	if len(options) > 0 {
		body["options"] = options
	}
	return body, nil
}

// ------------------------------------------------------------------ методы

// HTML — HTML в PDF.
func (c *Client) HTML(ctx context.Context, html string, opts *PageOptions) (*Result, error) {
	body, err := textBody("html", html, opts)
	if err != nil {
		return nil, err
	}
	return c.Convert(ctx, body)
}

// URL — страница по публичному адресу в PDF (тариф premium и выше).
func (c *Client) URL(ctx context.Context, url string, opts *PageOptions) (*Result, error) {
	body, err := textBody("url", url, opts)
	if err != nil {
		return nil, err
	}
	return c.Convert(ctx, body)
}

// Markdown — Markdown в PDF (тариф premium и выше).
func (c *Client) Markdown(ctx context.Context, markdown string, opts *PageOptions) (*Result, error) {
	body, err := textBody("markdown", markdown, opts)
	if err != nil {
		return nil, err
	}
	return c.Convert(ctx, body)
}

// Office — docx, xlsx или pptx в PDF. filename нужен с расширением: по нему
// сервер понимает формат. landscape — nil, чтобы оставить как в документе.
func (c *Client) Office(ctx context.Context, filename string, data []byte, landscape *bool) (*Result, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".docx" && ext != ".xlsx" && ext != ".pptx" {
		return nil, fmt.Errorf("%w: %q — поддерживаются только .docx, .xlsx и .pptx", ErrInvalidArgument, filename)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: %s — файл пустой", ErrInvalidArgument, filename)
	}
	if !bytes.HasPrefix(data, zipMagic) {
		// Старый .doc, PDF или картинка с чужим расширением: сервер всё равно отклонит.
		return nil, fmt.Errorf("%w: %s — не похож на docx, xlsx или pptx: это не ZIP-архив", ErrInvalidArgument, filename)
	}
	if len(data) > OfficeMaxBytes {
		return nil, fmt.Errorf("%w: %s — %.1f МБ, больше предела 3,7 МБ (после base64 тело превысило бы 5 МБ)",
			ErrInvalidArgument, filename, float64(len(data))/1e6)
	}
	body := map[string]any{
		"source":     "office",
		"filename":   filepath.Base(filename),
		"fileBase64": base64.StdEncoding.EncodeToString(data),
	}
	if landscape != nil {
		body["options"] = map[string]any{"landscape": *landscape}
	}
	return c.Convert(ctx, body)
}

// OfficeFile — то же, что Office, но читает файл с диска.
func (c *Client) OfficeFile(ctx context.Context, path string, landscape *bool) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return c.Office(ctx, filepath.Base(path), data, landscape)
}

// Convert — низкоуровневый POST /v1/convert с телом как есть.
func (c *Client) Convert(ctx context.Context, body map[string]any) (*Result, error) {
	// Без экранирования < > &: json.Marshal превратил бы каждый тег HTML в \u003c…\u003e,
	// и документ раньше упёрся бы в лимит тела 5 МБ.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, fmt.Errorf("%w: тело запроса: %v", ErrInvalidArgument, err)
	}
	payload := bytes.TrimRight(buf.Bytes(), "\n")
	resp, data, err := c.send(ctx, http.MethodPost, "/v1/convert", payload)
	if err != nil {
		return nil, err
	}
	return resultFromResponse(resp.Header, data), nil
}

// Me — тариф, расход и остаток страниц в текущем периоде.
func (c *Client) Me(ctx context.Context) (*Account, error) {
	_, data, err := c.send(ctx, http.MethodGet, "/v1/me", nil)
	if err != nil {
		return nil, err
	}
	var a accountJSON
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, &Error{Code: "server_error", Status: http.StatusOK, Message: "Ответ /v1/me не JSON.", cause: err}
	}
	return a.account(), nil
}

// ------------------------------------------------------------------ сеть

// sleep — пауза между повторами; переменная, чтобы тесты не ждали по-настоящему.
var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoff — 0,5 → 1 → 2 с … с разбросом ±25%.
func backoff(attempt int) time.Duration {
	base := float64(500*time.Millisecond) * float64(int(1)<<attempt)
	return time.Duration(base * (0.75 + rand.Float64()*0.5))
}

// retryDelay — через сколько повторить ответ; ok=false — не повторять.
// Повторяются только 429 и 503: сервер ничего не сделал и страниц не списал.
func retryDelay(attempt, status int, h http.Header, body []byte) (time.Duration, bool) {
	switch status {
	case http.StatusTooManyRequests:
		d, ok := retryAfterSeconds(h, body)
		if !ok {
			d = backoff(attempt)
		}
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d, true
	case http.StatusServiceUnavailable:
		return backoff(attempt), true
	}
	return 0, false
}

// notSent — ошибка, при которой запрос точно не ушёл (не удалось соединиться).
// Таймаут чтения и обрыв посреди ответа сюда не входят: сервер мог успеть
// отрендерить и списать страницы, повтор списал бы их второй раз.
func notSent(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func (c *Client) transportError(err error) *Error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &Error{
			Code: "timeout", kind: ErrTimeout, cause: err,
			Message: fmt.Sprintf("API не ответил за %s. Запрос мог быть выполнен — повторять его "+
				"автоматически небезопасно: страницы списались бы дважды.", c.timeout),
		}
	}
	return &Error{Code: "connection_error", kind: ErrConnection, cause: err,
		Message: fmt.Sprintf("Нет связи с %s: %v", c.baseURL, err)}
}

func (c *Client) send(ctx context.Context, method, path string, payload []byte) (*http.Response, []byte, error) {
	for attempt := 0; ; attempt++ {
		resp, data, err := c.once(ctx, method, path, payload)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err() // отменил вызывающий — это не ошибка API
			}
			if notSent(err) && attempt < c.maxRetries {
				if err := sleep(ctx, backoff(attempt)); err != nil {
					return nil, nil, err
				}
				continue
			}
			return nil, nil, c.transportError(err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, data, nil
		}
		if d, retry := retryDelay(attempt, resp.StatusCode, resp.Header, data); retry && attempt < c.maxRetries {
			if err := sleep(ctx, d); err != nil {
				return nil, nil, err
			}
			continue
		}
		return nil, nil, errorFromResponse(resp.StatusCode, resp.Header, data)
	}
}

func (c *Client) once(ctx context.Context, method, path string, payload []byte) (*http.Response, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/pdf, application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, data, nil
}
