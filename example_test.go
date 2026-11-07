package pdfapi_test

import (
	"context"
	"errors"
	"fmt"
	"log"

	pdfapi "github.com/pdfapi-ru/sdk-go"
)

func Example() {
	client, err := pdfapi.NewClient() // ключ из PDFAPI_KEY
	if err != nil {
		log.Fatal(err)
	}
	pdf, err := client.HTML(context.Background(), "<h1>Счёт №42</h1>", &pdfapi.PageOptions{MarginTop: "20mm"})
	switch {
	case errors.Is(err, pdfapi.ErrQuotaExceeded):
		log.Fatal("страницы кончились")
	case err != nil:
		log.Fatal(err)
	}
	if err := pdf.Save("invoice.pdf"); err != nil {
		log.Fatal(err)
	}
	fmt.Println("страниц:", *pdf.Pages)
}
