// Счёт из HTML-шаблона.
//
//	PDFAPI_KEY=pdf_live_... go run ./examples/invoice
package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log"

	pdfapi "github.com/pdfapi-ru/sdk-go"
)

type item struct {
	Name       string
	Qty, Price int
}

func (i item) Sum() int { return i.Qty * i.Price }

// html/template сам экранирует данные — название услуги не сломает разметку.
var invoice = template.Must(template.New("invoice").Funcs(template.FuncMap{"rub": rub}).Parse(`
<style>
  body { font-family: sans-serif; }
  table { width: 100%; border-collapse: collapse; }
  td, th { border-bottom: 1px solid #ddd; padding: 8px; text-align: left; }
</style>
<h1>Счёт №42</h1>
<table>
  <tr><th>Услуга</th><th>Кол-во</th><th>Сумма</th></tr>
  {{range .Items}}<tr><td>{{.Name}}</td><td>{{.Qty}}</td><td>{{rub .Sum}}</td></tr>{{end}}
</table>
<h2>Итого: {{rub .Total}}</h2>`))

// rub — 12 000 ₽.
func rub(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + " " + s[i:]
	}
	return s + " ₽"
}

func main() {
	items := []item{{"Подписка Premium, октябрь", 1, 1990}, {"Дополнительные страницы", 2, 500}}
	total := 0
	for _, it := range items {
		total += it.Sum()
	}
	var html bytes.Buffer
	if err := invoice.Execute(&html, map[string]any{"Items": items, "Total": total}); err != nil {
		log.Fatal(err)
	}

	client, err := pdfapi.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	pdf, err := client.HTML(context.Background(), html.String(), &pdfapi.PageOptions{MarginTop: "20mm", MarginBottom: "20mm"})
	if err != nil {
		log.Fatalf("Не получилось: %v", err)
	}
	if err := pdf.Save("invoice.pdf"); err != nil {
		log.Fatal(err)
	}
	left := "?"
	if pdf.Quota != nil {
		left = fmt.Sprint(pdf.Quota.Remaining)
	}
	fmt.Printf("invoice.pdf: %d стр., осталось %s\n", *pdf.Pages, left)
}
