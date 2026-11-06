// Пачка документов параллельно, с ограничением одновременных запросов.
//
// Тариф ограничивает частоту (Free — 1 запрос в секунду и до 3 подряд,
// Premium — 10 и 30). SDK сам подождёт и повторит при 429, но ровнее —
// не отправлять больше, чем позволяет тариф.
//
//	PDFAPI_KEY=pdf_live_... go run ./examples/batch
package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	pdfapi "github.com/Homelander-77/PDF-creator-site/sdk/go"
)

const parallel = 3

func main() {
	client, err := pdfapi.NewClient() // один клиент на все горутины
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	limit := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for n := 1; n <= 10; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()

			name := fmt.Sprintf("invoice-%d.pdf", n)
			pdf, err := client.HTML(ctx, fmt.Sprintf("<h1>Счёт №%d</h1>", n), nil)
			if err == nil {
				err = pdf.Save(name)
			}
			if err != nil {
				log.Printf("%s: %v", name, err)
				return
			}
			log.Printf("%s: %d стр.", name, *pdf.Pages)
		}(n)
	}
	wg.Wait()
}
