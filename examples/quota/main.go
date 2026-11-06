// Сколько страниц осталось и когда квота обнулится — для cron и мониторинга.
//
//	PDFAPI_KEY=pdf_live_... go run ./examples/quota       # предупредить, если осталось меньше 10%
//	PDFAPI_KEY=pdf_live_... go run ./examples/quota 25    # …меньше 25%
//
// Код выхода 2 — страниц меньше порога: cron пришлёт письмо, мониторинг поднимет тревогу.
// Запрос страниц не тратит.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	pdfapi "github.com/Homelander-77/PDF-creator-site/sdk/go"
)

func main() {
	threshold := 10.0
	if len(os.Args) > 1 {
		v, err := strconv.ParseFloat(os.Args[1], 64)
		if err != nil {
			log.Fatalf("порог в процентах: %v", err)
		}
		threshold = v
	}
	client, err := pdfapi.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	me, err := client.Me(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	left := 0.0
	if me.PagesLimit > 0 {
		left = 100 * float64(me.PagesRemaining) / float64(me.PagesLimit)
	}
	reset := "неизвестно когда"
	if !me.PeriodEnd.IsZero() {
		reset = me.PeriodEnd.Format("02.01.2006 15:04 UTC")
	}
	fmt.Printf("Тариф %s: использовано %d из %d, осталось %d (%d%%).\n", me.Plan, me.PagesUsed, me.PagesLimit, me.PagesRemaining, int(left))
	fmt.Printf("Счётчик обнулится %s.\n", reset)
	if left < threshold {
		fmt.Fprintf(os.Stderr, "Осталось меньше %g%% — пора докупить страницы или сменить тариф.\n", threshold)
		os.Exit(2)
	}
}
