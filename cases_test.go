package pdfapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Общие тест-кейсы SDK (sdk/tests/cases.json): что ответил сервер → что сделал SDK.

type caseResponse struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	JSON       json.RawMessage   `json:"json"`
	Text       *string           `json:"text"`
	BodyBase64 string            `json:"body_base64"`
}

type sharedCase struct {
	Name string `json:"name"`
	Call struct {
		Method  string          `json:"method"`
		Args    map[string]any  `json:"args"`
		Options json.RawMessage `json:"options"`
	} `json:"call"`
	Responses []caseResponse `json:"responses"`
	Expect    struct {
		Request *struct {
			Method      string          `json:"method"`
			Path        string          `json:"path"`
			JSON        json.RawMessage `json:"json"`
			RawContains string          `json:"raw_contains"`
		} `json:"request"`
		Result *struct {
			ContentBase64 string `json:"content_base64"`
			Pages         *int   `json:"pages"`
			Quota         *Quota `json:"quota"`
		} `json:"result"`
		Me            map[string]any `json:"me"`
		ArgumentError *struct {
			Mentions string `json:"mentions"`
		} `json:"argument_error"`
		Error *struct {
			Class           string   `json:"class"`
			Code            string   `json:"code"`
			Status          int      `json:"status"`
			MessageContains string   `json:"message_contains"`
			RetryAfter      *float64 `json:"retry_after"`
		} `json:"error"`
		Attempts      int       `json:"attempts"`
		Sleeps        []float64 `json:"sleeps"`
		SleepsBetween []float64 `json:"sleeps_between"`
	} `json:"expect"`
}

var kindByClass = map[string]error{
	"AuthenticationError": ErrAuthentication, "PermissionDeniedError": ErrPermissionDenied,
	"QuotaExceededError": ErrQuotaExceeded, "InvalidRequestError": ErrInvalidRequest,
	"PayloadTooLargeError": ErrPayloadTooLarge, "RateLimitError": ErrRateLimit, "RenderError": ErrRender,
	"RenderTimeoutError": ErrRenderTimeout, "UnavailableError": ErrUnavailable,
	"APIConnectionError": ErrConnection, "APITimeoutError": ErrTimeout, "PdfApiError": nil,
}

type recorded struct {
	method, path string
	body         []byte
	header       http.Header
}

// scriptedServer отвечает по очереди и запоминает запросы.
func scriptedServer(t *testing.T, responses []caseResponse) (*httptest.Server, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var seen []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, recorded{r.Method, r.URL.Path, body, r.Header.Clone()})
		spec := responses[min(len(seen)-1, len(responses)-1)]
		mu.Unlock()
		for k, v := range spec.Headers {
			w.Header().Set(k, v)
		}
		switch {
		case spec.JSON != nil:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(spec.Status)
			_, _ = w.Write(spec.JSON)
		case spec.Text != nil:
			w.WriteHeader(spec.Status)
			_, _ = io.WriteString(w, *spec.Text)
		default:
			data, _ := base64.StdEncoding.DecodeString(spec.BodyBase64)
			w.WriteHeader(spec.Status)
			_, _ = w.Write(data)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recorded { mu.Lock(); defer mu.Unlock(); return append([]recorded(nil), seen...) }
}

// recordSleeps подменяет паузы между повторами и записывает их.
func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	orig := sleep
	sleep = func(_ context.Context, d time.Duration) error { got = append(got, d); return nil }
	t.Cleanup(func() { sleep = orig })
	return &got
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func loadCases(t *testing.T) []sharedCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "tests", "cases.json"))
	if err != nil {
		t.Fatalf("общие кейсы: %v", err)
	}
	var file struct {
		Cases []sharedCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file.Cases
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("тело запроса не JSON: %s", a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

func TestSharedCases(t *testing.T) {
	for _, tc := range loadCases(t) {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			sleeps := recordSleeps(t)
			srv, seen := scriptedServer(t, tc.Responses)
			client, err := NewClient(WithAPIKey("pdf_live_test"), WithBaseURL(srv.URL+"/api"))
			if err != nil {
				t.Fatal(err)
			}
			var opts *PageOptions
			if tc.Call.Options != nil {
				opts = &PageOptions{}
				if err := json.Unmarshal(tc.Call.Options, opts); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			var result *Result
			var account *Account
			switch tc.Call.Method {
			case "html":
				result, err = client.HTML(ctx, tc.Call.Args["html"].(string), opts)
			case "url":
				result, err = client.URL(ctx, tc.Call.Args["url"].(string), opts)
			case "markdown":
				result, err = client.Markdown(ctx, tc.Call.Args["markdown"].(string), opts)
			case "office":
				data, _ := base64.StdEncoding.DecodeString(tc.Call.Args["file_base64"].(string))
				var landscape *bool
				if opts != nil {
					landscape = opts.Landscape
				}
				result, err = client.Office(ctx, tc.Call.Args["filename"].(string), data, landscape)
			case "me":
				account, err = client.Me(ctx)
			default:
				t.Fatalf("неизвестный метод %q", tc.Call.Method)
			}

			ex := tc.Expect
			requests := seen()
			if len(requests) != ex.Attempts {
				t.Fatalf("попыток: %d, ждали %d", len(requests), ex.Attempts)
			}
			if ex.Request != nil {
				r := requests[0]
				if r.method != ex.Request.Method || !strings.HasSuffix(r.path, ex.Request.Path) {
					t.Fatalf("запрос %s %s, ждали %s …%s", r.method, r.path, ex.Request.Method, ex.Request.Path)
				}
				if ex.Request.JSON != nil && !jsonEqual(t, r.body, ex.Request.JSON) {
					t.Fatalf("тело запроса %s, ждали %s", r.body, ex.Request.JSON)
				}
				if ex.Request.RawContains != "" && !strings.Contains(string(r.body), ex.Request.RawContains) {
					t.Fatalf("тело %s без %s", r.body, ex.Request.RawContains)
				}
			}
			if ex.Result != nil {
				if err != nil {
					t.Fatalf("ждали результат, получили %v", err)
				}
				want, _ := base64.StdEncoding.DecodeString(ex.Result.ContentBase64)
				if !bytes.Equal(result.Content, want) {
					t.Fatalf("содержимое %q", result.Content)
				}
				if !reflect.DeepEqual(result.Pages, ex.Result.Pages) {
					t.Fatalf("страниц %v, ждали %v", result.Pages, ex.Result.Pages)
				}
				if !reflect.DeepEqual(result.Quota, ex.Result.Quota) {
					t.Fatalf("квота %+v, ждали %+v", result.Quota, ex.Result.Quota)
				}
			}
			if ex.Me != nil {
				if err != nil {
					t.Fatal(err)
				}
				got := map[string]any{
					"user_id": account.UserID, "plan": account.Plan, "pages_used": float64(account.PagesUsed),
					"pages_limit": float64(account.PagesLimit), "pages_remaining": float64(account.PagesRemaining),
					"requests_per_second": account.RequestsPerSecond, "burst": float64(account.Burst),
				}
				times := map[string]time.Time{"period_start": account.PeriodStart, "period_end": account.PeriodEnd}
				for k, v := range ex.Me {
					if tm, ok := times[k]; ok {
						var want time.Time // null — нулевое время
						if v != nil {
							want, _ = time.Parse(time.RFC3339, v.(string))
						}
						if !tm.Equal(want) {
							t.Fatalf("%s: %v, ждали %v", k, tm, want)
						}
						continue
					}
					if got[k] != v {
						t.Fatalf("%s: %v, ждали %v", k, got[k], v)
					}
				}
			}
			if ex.ArgumentError != nil {
				// Ошибка аргумента, а не API: запрос не ушёл.
				var apiErr *Error
				if !errors.Is(err, ErrInvalidArgument) || errors.As(err, &apiErr) {
					t.Fatalf("ждали ErrInvalidArgument, получили %v", err)
				}
				if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(ex.ArgumentError.Mentions)) {
					t.Fatalf("текст %q без %q", err, ex.ArgumentError.Mentions)
				}
			}
			if ex.Error != nil {
				var apiErr *Error
				if !errors.As(err, &apiErr) {
					t.Fatalf("ждали *Error, получили %v", err)
				}
				if want := kindByClass[ex.Error.Class]; apiErr.Kind() != want {
					t.Fatalf("вид %v, ждали %v (%s)", apiErr.Kind(), want, ex.Error.Class)
				}
				if apiErr.Code != ex.Error.Code || apiErr.Status != ex.Error.Status {
					t.Fatalf("%s/%d, ждали %s/%d", apiErr.Code, apiErr.Status, ex.Error.Code, ex.Error.Status)
				}
				if !strings.Contains(apiErr.Message, ex.Error.MessageContains) {
					t.Fatalf("текст %q без %q", apiErr.Message, ex.Error.MessageContains)
				}
				if ex.Error.RetryAfter != nil && apiErr.RetryAfter != time.Duration(*ex.Error.RetryAfter*float64(time.Second)) {
					t.Fatalf("retry after %v", apiErr.RetryAfter)
				}
			}
			if ex.Sleeps != nil {
				got := make([]float64, len(*sleeps))
				for i, d := range *sleeps {
					got[i] = d.Seconds()
				}
				if !reflect.DeepEqual(got, ex.Sleeps) {
					t.Fatalf("паузы %v, ждали %v", got, ex.Sleeps)
				}
			}
			if ex.SleepsBetween != nil {
				if len(*sleeps) == 0 {
					t.Fatal("ждали паузу")
				}
				for _, d := range *sleeps {
					if s := d.Seconds(); s < ex.SleepsBetween[0] || s > ex.SleepsBetween[1] {
						t.Fatalf("пауза %v вне %v", s, ex.SleepsBetween)
					}
				}
			}
		})
	}
}
