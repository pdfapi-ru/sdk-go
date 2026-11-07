// Страница по адресу в PDF (тариф premium и выше).
//
//	PDFAPI_KEY=pdf_live_... go run ./examples/from-url https://example.com
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	pdfapi "github.com/pdfapi-ru/sdk-go"
)

func main() {
	url := "https://example.com"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	client, err := pdfapi.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	// WaitDelay — подождать, пока скрипты на странице дорисуют содержимое.
	pdf, err := client.URL(context.Background(), url, &pdfapi.PageOptions{Landscape: pdfapi.Bool(true), WaitDelay: "1s"})
	var apiErr *pdfapi.Error
	switch {
	case errors.Is(err, pdfapi.ErrPermissionDenied):
		log.Fatal("Страницы по адресу — на тарифе premium и выше")
	case errors.Is(err, pdfapi.ErrInvalidRequest) && errors.As(err, &apiErr):
		log.Fatal(apiErr.Message) // например, url_not_allowed: адрес во внутреннюю сеть
	case err != nil:
		log.Fatal(err)
	}
	if err := pdf.Save("page.pdf"); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("page.pdf: %d стр.\n", *pdf.Pages)
}
