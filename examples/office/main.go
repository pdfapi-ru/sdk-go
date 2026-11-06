// docx, xlsx или pptx в PDF (тариф premium и выше).
//
//	PDFAPI_KEY=pdf_live_... go run ./examples/office report.docx
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	pdfapi "github.com/Homelander-77/PDF-creator-site/sdk/go"
)

func main() {
	source := "report.docx"
	if len(os.Args) > 1 {
		source = os.Args[1]
	}
	client, err := pdfapi.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	pdf, err := client.OfficeFile(context.Background(), source, nil) // из памяти — client.Office(ctx, "report.docx", data, nil)
	if err != nil {
		log.Fatal(err)
	}
	target := strings.TrimSuffix(source, filepath.Ext(source)) + ".pdf"
	if err := pdf.Save(target); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s: %d стр.\n", target, *pdf.Pages)
}
