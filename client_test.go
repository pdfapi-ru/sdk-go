package pdfapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "pdf_live_secret_test_key_123"

func pdfServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(WithAPIKey(testKey), WithBaseURL(srv.URL+"/api/"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func okPDF(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Pages-Rendered", "1")
	_, _ = io.WriteString(w, "%PDF-1.4")
}

func TestNoKeyFailsAtConstruction(t *testing.T) {
	t.Setenv("PDFAPI_KEY", "")
	if _, err := NewClient(); !errors.Is(err, ErrInvalidArgument) || !strings.Contains(err.Error(), "PDFAPI_KEY") {
		t.Fatalf("ждали ошибку про PDFAPI_KEY, получили %v", err)
	}
}

func TestSettingsFromEnvAndTrailingSlash(t *testing.T) {
	t.Setenv("PDFAPI_KEY", "pdf_live_env")
	t.Setenv("PDFAPI_BASE_URL", "http://localhost:18080/api/")
	c, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != "http://localhost:18080/api" || c.apiKey != "pdf_live_env" {
		t.Fatalf("%s %s", c.BaseURL(), c.apiKey)
	}
	t.Setenv("PDFAPI_BASE_URL", "")
	c, _ = NewClient(WithAPIKey("explicit"))
	if c.BaseURL() != DefaultBaseURL || c.apiKey != "explicit" {
		t.Fatal("явный ключ должен перекрывать окружение, адрес — по умолчанию")
	}
}

func TestHeadersAndKeyNeverLeaks(t *testing.T) {
	var got http.Header
	c := pdfServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"invalid_api_key"}`)
	})
	_, err := c.HTML(context.Background(), "<p>x</p>", nil)
	if got.Get("Authorization") != "Bearer "+testKey || !strings.HasPrefix(got.Get("User-Agent"), "pdfapi-go/"+Version+" (go") ||
		got.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("заголовки: %v", got)
	}
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("ждали ErrAuthentication: %v", err)
	}
	for _, s := range []string{err.Error(), fmt.Sprintf("%+v", err), c.String(), fmt.Sprint(c)} {
		if strings.Contains(s, testKey) {
			t.Fatalf("ключ утёк: %s", s)
		}
	}
}

func TestErrorHierarchy(t *testing.T) {
	cases := map[*Error][]error{
		{kind: ErrPayloadTooLarge}: {ErrPayloadTooLarge, ErrInvalidRequest},
		{kind: ErrRenderTimeout}:   {ErrRenderTimeout, ErrRender},
		{kind: ErrTimeout}:         {ErrTimeout, ErrConnection},
	}
	for e, kinds := range cases {
		for _, k := range kinds {
			if !errors.Is(e, k) {
				t.Fatalf("%v должна быть %v", e.kind, k)
			}
		}
	}
	if errors.Is(&Error{kind: ErrRender}, ErrRenderTimeout) {
		t.Fatal("родитель не должен быть потомком")
	}
}

func TestOptionsAndBody(t *testing.T) {
	var body map[string]any
	c := pdfServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		okPDF(w, r)
	})
	_, err := c.HTML(context.Background(), "<p>x</p>", &PageOptions{Landscape: Bool(false), MarginTop: "20mm", WaitDelay: "1s"})
	if err != nil {
		t.Fatal(err)
	}
	opts := body["options"].(map[string]any)
	if opts["landscape"] != false || opts["marginTop"] != "20mm" || opts["waitDelay"] != "1s" || len(opts) != 3 {
		t.Fatalf("options: %v", opts)
	}
	if _, err := c.HTML(context.Background(), "<p>x</p>", &PageOptions{MarginTop: "  "}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("пустой размер: %v", err)
	}
	if _, err := c.Markdown(context.Background(), " ", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("пустое содержимое: %v", err)
	}
}

func TestOffice(t *testing.T) {
	var body map[string]any
	var calls int32
	c := pdfServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewDecoder(r.Body).Decode(&body)
		okPDF(w, r)
	})
	path := filepath.Join(t.TempDir(), "Отчёт.docx")
	if err := os.WriteFile(path, []byte("PK\x03\x04docx"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OfficeFile(context.Background(), path, Bool(true)); err != nil {
		t.Fatal(err)
	}
	if body["filename"] != "Отчёт.docx" || body["fileBase64"] != base64.StdEncoding.EncodeToString([]byte("PK\x03\x04docx")) ||
		body["options"].(map[string]any)["landscape"] != true {
		t.Fatalf("тело: %v", body)
	}
	if _, err := c.Office(context.Background(), "scan.pdf", []byte("%PDF"), nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("расширение: %v", err)
	}
	big := append([]byte("PK\x03\x04"), make([]byte, OfficeMaxBytes)...)
	if _, err := c.Office(context.Background(), "big.pptx", big, nil); !errors.Is(err, ErrInvalidArgument) || !strings.Contains(err.Error(), "3,7 МБ") {
		t.Fatalf("размер: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("неверные аргументы не должны доходить до сервера: %d запросов", calls)
	}
}

func TestBadSettingsFailAtConstruction(t *testing.T) {
	for _, opt := range []Option{WithTimeout(0), WithTimeout(-time.Second), WithMaxRetries(-1)} {
		if _, err := NewClient(WithAPIKey(testKey), opt); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("ждали ErrInvalidArgument: %v", err)
		}
	}
}

func TestSaveReplacesFileAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(path, []byte("old content that is longer than the new one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&Result{Content: []byte("%PDF-new")}).Save(path); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "%PDF-new" {
		t.Fatalf("%q", data)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("лишние файлы: %v", entries)
	}
}

func TestSaveFailureKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(path, []byte("%PDF-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := syncFile
	syncFile = func(*os.File) error { return errors.New("диск отвалился") }
	t.Cleanup(func() { syncFile = orig })
	if err := (&Result{Content: []byte("%PDF-new")}).Save(path); err == nil || err.Error() != "диск отвалился" {
		t.Fatalf("ждали сбой: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "%PDF-old" {
		t.Fatalf("прежний файл испорчен: %q", data)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("временный файл остался: %v", entries)
	}
}

func TestSaveIntoMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := (&Result{Content: []byte("%PDF")}).Save(filepath.Join(dir, "нет", "out.pdf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ждали ErrNotExist: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("создано лишнее: %v", entries)
	}
}

func TestWriteTo(t *testing.T) {
	var buf strings.Builder
	n, err := (&Result{Content: []byte("%PDF-1.4")}).WriteTo(&buf)
	if err != nil || n != 8 || buf.String() != "%PDF-1.4" {
		t.Fatalf("%d %v %q", n, err, buf.String())
	}
	var _ io.WriterTo = (*Result)(nil)
}

func TestSave(t *testing.T) {
	c := pdfServer(t, okPDF)
	r, err := c.HTML(context.Background(), "<p>x</p>", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "out.pdf")
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "%PDF-1.4" || *r.Pages != 1 || r.Quota != nil {
		t.Fatalf("%q %v %v", data, *r.Pages, r.Quota)
	}
}

// roundTripper — подменный транспорт: сначала ошибки по списку, потом обычный ответ.
type roundTripper struct {
	errs  []error
	calls int32
}

func (rt *roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	n := int(atomic.AddInt32(&rt.calls, 1)) - 1
	if n < len(rt.errs) {
		return nil, rt.errs[n]
	}
	rec := httptest.NewRecorder()
	okPDF(rec, r)
	return rec.Result(), nil
}

func clientWith(t *testing.T, rt http.RoundTripper, opts ...Option) *Client {
	t.Helper()
	c, err := NewClient(append([]Option{WithAPIKey(testKey), WithBaseURL("http://pdfapi.test/api"),
		WithHTTPClient(&http.Client{Transport: rt})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDialErrorIsRetried(t *testing.T) {
	sleeps := recordSleeps(t)
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	rt := &roundTripper{errs: []error{dial}}
	if _, err := clientWith(t, rt).HTML(context.Background(), "<p>x</p>", nil); err != nil {
		t.Fatal(err)
	}
	if rt.calls != 2 || len(*sleeps) != 1 || (*sleeps)[0] < 375*time.Millisecond || (*sleeps)[0] > 625*time.Millisecond {
		t.Fatalf("попыток %d, паузы %v", rt.calls, *sleeps)
	}

	rt = &roundTripper{errs: []error{dial, dial, dial}}
	_, err := clientWith(t, rt).HTML(context.Background(), "<p>x</p>", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind() != ErrConnection || rt.calls != 3 {
		t.Fatalf("после повторов: %v, попыток %d", err, rt.calls)
	}
}

func TestBrokenResponseIsNotRetried(t *testing.T) {
	// Сервер мог успеть отрендерить и списать страницы — повтор списал бы их дважды.
	sleeps := recordSleeps(t)
	rt := &roundTripper{errs: []error{io.ErrUnexpectedEOF}}
	_, err := clientWith(t, rt).HTML(context.Background(), "<p>x</p>", nil)
	if !errors.Is(err, ErrConnection) || errors.Is(err, ErrTimeout) || rt.calls != 1 || len(*sleeps) != 0 {
		t.Fatalf("%v, попыток %d", err, rt.calls)
	}
}

// slowHandler отвечает не раньше чем через 2 с. Тело дочитывается сразу:
// иначе сервер не заметит, что клиент ушёл, и будет ждать до конца.
func slowHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(2 * time.Second):
	}
}

func TestTimeoutIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		slowHandler(w, r)
	}))
	defer srv.Close()
	c, _ := NewClient(WithAPIKey(testKey), WithBaseURL(srv.URL), WithTimeout(100*time.Millisecond))
	_, err := c.HTML(context.Background(), "<p>x</p>", nil)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, ErrConnection) || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("%v, попыток %d", err, calls)
	}
}

func TestCallerCancellationIsReturnedAsIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(slowHandler))
	defer srv.Close()
	c, _ := NewClient(WithAPIKey(testKey), WithBaseURL(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.HTML(ctx, "<p>x</p>", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ждали context.DeadlineExceeded, получили %v", err)
	}
}

func TestMaxRetriesZero(t *testing.T) {
	var calls int32
	c := pdfServer(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":"unavailable"}`)
	})
	c.maxRetries = 0
	if _, err := c.HTML(context.Background(), "<p>x</p>", nil); !errors.Is(err, ErrUnavailable) || calls != 1 {
		t.Fatalf("%v, попыток %d", err, calls)
	}
}
