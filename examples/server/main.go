// Отдавать PDF из веб-приложения: net/http, без фреймворков.
//
//	PDFAPI_KEY=pdf_live_... go run ./examples/server
//	curl -o invoice.pdf 'http://127.0.0.1:8080/invoice/42?customer=ООО%20Ромашка'
package main

import (
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"

	pdfapi "github.com/pdfapi-ru/sdk-go"
)

func main() {
	client, err := pdfapi.NewClient() // один на приложение, безопасен для горутин
	if err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/invoice/", func(w http.ResponseWriter, r *http.Request) {
		number, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/invoice/"))
		if err != nil || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		customer := r.URL.Query().Get("customer")
		if customer == "" {
			customer = "Покупатель"
		}
		// Всё, что пришло от пользователя, — только через экранирование: иначе это HTML-инъекция в документ.
		doc := fmt.Sprintf("<h1>Счёт №%d</h1><p>Покупатель: %s</p><p>К оплате: 12 000 ₽</p>", number, html.EscapeString(customer))
		// Контекст запроса: клиент ушёл — запрос к pdfapi тоже отменяется.
		pdf, err := client.HTML(r.Context(), doc, &pdfapi.PageOptions{MarginTop: "20mm"})
		var apiErr *pdfapi.Error
		switch {
		case errors.Is(err, pdfapi.ErrRateLimit) && errors.As(err, &apiErr):
			w.Header().Set("Retry-After", strconv.Itoa(int(apiErr.RetryAfter.Seconds())+1))
			http.Error(w, "Слишком часто, повторите позже", http.StatusTooManyRequests)
			return
		case errors.As(err, &apiErr):
			http.Error(w, "PDF не получился: "+apiErr.Message, http.StatusBadGateway)
			return
		case err != nil:
			log.Print(err)
			http.Error(w, "Внутренняя ошибка", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="invoice-%d.pdf"`, number))
		_, _ = pdf.WriteTo(w)
	})
	log.Print("http://127.0.0.1:8080/invoice/42")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", nil))
}
