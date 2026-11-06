package pdfapi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Против настоящего API. Запускаются, только если заданы PDFAPI_IT_KEY и PDFAPI_IT_BASE_URL:
//
//	PDFAPI_IT_KEY=pdf_live_... PDFAPI_IT_BASE_URL=http://localhost:18080/api go test -run Integration -v
func integrationClient(t *testing.T) *Client {
	t.Helper()
	key, base := os.Getenv("PDFAPI_IT_KEY"), os.Getenv("PDFAPI_IT_BASE_URL")
	if key == "" || base == "" {
		t.Skip("нужны PDFAPI_IT_KEY и PDFAPI_IT_BASE_URL")
	}
	c, err := NewClient(WithAPIKey(key), WithBaseURL(base))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIntegrationHTML(t *testing.T) {
	c := integrationClient(t)
	pdf, err := c.HTML(context.Background(), "<h1>Go SDK</h1><div style='page-break-before:always'>2</div>", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(pdf.Content[:4]) != "%PDF" || pdf.Pages == nil || *pdf.Pages != 2 || pdf.Quota == nil {
		t.Fatalf("pages=%v quota=%v", pdf.Pages, pdf.Quota)
	}
}

func TestIntegrationAllFormatsAndSave(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	fixture := func(name string) string { return filepath.Join("..", "tests", "fixtures", name) }
	pptx, err := os.ReadFile(fixture("slides.pptx"))
	if err != nil {
		t.Skip("нет sdk/tests/fixtures")
	}
	// check("x")(c.Метод(...)) — результат вызова целиком, вместе с ошибкой.
	check := func(name string) func(*Result, error) *Result {
		return func(pdf *Result, err error) *Result {
			t.Helper()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !bytes.HasPrefix(pdf.Content, []byte("%PDF")) || pdf.Pages == nil || *pdf.Pages < 1 {
				t.Fatalf("%s: pages=%v", name, pdf.Pages)
			}
			return pdf
		}
	}
	first := check("markdown")(c.Markdown(ctx, "# Отчёт\n\n- пункт один\n- пункт два", nil))
	check("url")(c.URL(ctx, "https://example.com", &PageOptions{WaitDelay: "500ms"}))
	check("docx")(c.OfficeFile(ctx, fixture("report.docx"), nil))
	check("xlsx")(c.OfficeFile(ctx, fixture("table.xlsx"), Bool(true)))
	check("pptx")(c.Office(ctx, "slides.pptx", pptx, nil))
	path := filepath.Join(t.TempDir(), "out.pdf")
	if err := first.Save(path); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, first.Content) {
		t.Fatal("сохранённый файл отличается")
	}
}

func TestIntegrationMeAndErrors(t *testing.T) {
	c := integrationClient(t)
	me, err := c.Me(context.Background())
	if err != nil || me.PagesLimit <= 0 || me.UserID == "" || me.PagesUsed+me.PagesRemaining != me.PagesLimit ||
		!me.PeriodEnd.After(me.PeriodStart) {
		t.Fatalf("%+v %v", me, err)
	}
	bad, _ := NewClient(WithAPIKey("pdf_live_wrong"), WithBaseURL(c.BaseURL()))
	if _, err := bad.HTML(context.Background(), "<p>x</p>", nil); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("неверный ключ: %v", err)
	}
	_, err = c.Convert(context.Background(), map[string]any{"source": "html", "html": "<p>x</p>", "options": map[string]any{"foo": "bar"}})
	var apiErr *Error
	if !errors.As(err, &apiErr) || !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("неизвестный параметр: %v", err)
	}
}
