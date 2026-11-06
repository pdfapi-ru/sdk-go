package pdfapi

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Quota — квота периода после конвертации (заголовки X-Quota-*).
type Quota struct {
	Limit     int
	Used      int
	Remaining int
}

// Result — готовый PDF.
type Result struct {
	Content []byte // байты PDF
	Pages   *int   // сколько страниц списано; nil — сервер не сообщил
	Quota   *Quota // остаток квоты; nil — сервер не сообщил
}

// syncFile — сброс файла на диск; переменная, чтобы тесты могли изобразить сбой.
var syncFile = (*os.File).Sync

// Save записывает PDF в файл. Атомарно: сначала во временный файл рядом, затем
// переименование поверх — оборвётся запись, на месте останется прежний файл,
// а не обрезанный PDF. Права — как у обычного нового файла (0666 с учётом
// umask). Каталог не создаётся.
func (r *Result) Save(path string) (err error) {
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	dir, name := filepath.Split(path)
	tmp := filepath.Join(dir, "."+name+"."+hex.EncodeToString(rnd[:])+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(r.Content); err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteTo пишет PDF в w — например, в http.ResponseWriter. Реализует io.WriterTo.
func (r *Result) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(r.Content)
	return int64(n), err
}

// Account — тариф и расход (GET /v1/me).
type Account struct {
	UserID string
	Plan   string // free, premium, business
	// PeriodStart — начало периода, PeriodEnd — когда счётчик страниц начнётся
	// с нуля; оба в UTC. Нулевое время (IsZero) — сервер не сообщил.
	PeriodStart       time.Time
	PeriodEnd         time.Time
	PagesUsed         int
	PagesLimit        int
	PagesRemaining    int
	RequestsPerSecond float64
	Burst             int
}

func intHeader(h http.Header, name string) *int {
	v, err := strconv.Atoi(h.Get(name))
	if err != nil {
		return nil
	}
	return &v
}

func resultFromResponse(h http.Header, body []byte) *Result {
	r := &Result{Content: body, Pages: intHeader(h, "X-Pages-Rendered")}
	limit, used, remaining := intHeader(h, "X-Quota-Limit"), intHeader(h, "X-Quota-Used"), intHeader(h, "X-Quota-Remaining")
	if limit != nil && used != nil && remaining != nil {
		r.Quota = &Quota{Limit: *limit, Used: *used, Remaining: *remaining}
	}
	return r
}

type accountJSON struct {
	UserID      string `json:"user_id"`
	Plan        string `json:"plan"`
	Period      string `json:"period"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	Usage       struct {
		PagesUsed      int `json:"pages_used"`
		PagesLimit     int `json:"pages_limit"`
		PagesRemaining int `json:"pages_remaining"`
	} `json:"usage"`
	Limits struct {
		RequestsPerSecond float64 `json:"requests_per_second"`
		Burst             int     `json:"burst"`
	} `json:"limits"`
}

func parseTime(layout, value string) time.Time {
	t, err := time.Parse(layout, value)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func (a accountJSON) account() *Account {
	start := parseTime(time.RFC3339Nano, a.PeriodStart)
	if start.IsZero() {
		start = parseTime(time.DateOnly, a.Period) // старый API: только дата начала, полночь UTC
	}
	return &Account{
		UserID: a.UserID, Plan: a.Plan, PeriodStart: start, PeriodEnd: parseTime(time.RFC3339Nano, a.PeriodEnd),
		PagesUsed: a.Usage.PagesUsed, PagesLimit: a.Usage.PagesLimit, PagesRemaining: a.Usage.PagesRemaining,
		RequestsPerSecond: a.Limits.RequestsPerSecond, Burst: a.Limits.Burst,
	}
}
