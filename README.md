# pdfapi — Go SDK

Официальная библиотека [pdfapi](https://pdfapi.ru): HTML, страница по адресу, Markdown или docx/xlsx/pptx — в PDF одним вызовом. Только стандартная библиотека, Go 1.21+.

```bash
go get github.com/pdfapi-ru/sdk-go
```

```go
import pdfapi "github.com/pdfapi-ru/sdk-go"

client, err := pdfapi.NewClient() // ключ из переменной окружения PDFAPI_KEY
if err != nil {
	log.Fatal(err)
}
pdf, err := client.HTML(ctx, "<h1>Счёт №42</h1>", &pdfapi.PageOptions{MarginTop: "20mm"})
if err != nil {
	log.Fatal(err)
}
if err := pdf.Save("invoice.pdf"); err != nil {
	log.Fatal(err)
}
fmt.Println(*pdf.Pages, "стр.") // сколько страниц списано
```

Ключ выпускается в кабинете на [pdfapi.ru](https://pdfapi.ru). Держите его в переменной окружения, а не в коде:

```bash
export PDFAPI_KEY=pdf_live_...
```

Клиент безопасен для горутин: создайте один и переиспользуйте.

## Методы

Первый аргумент у всех — `context.Context`: отмена и дедлайны работают как обычно.

| Метод | Что делает |
|---|---|
| `HTML(ctx, html, opts)` | HTML → PDF |
| `URL(ctx, url, opts)` | страница по адресу → PDF (тариф premium и выше). Адрес — полный, с `http://` или `https://` |
| `Markdown(ctx, text, opts)` | Markdown → PDF (тариф premium и выше) |
| `Office(ctx, filename, data, landscape)` | docx, xlsx, pptx из памяти → PDF (тариф premium и выше) |
| `OfficeFile(ctx, path, landscape)` | то же с диска |
| `Convert(ctx, body)` | `POST /v1/convert` с телом как есть |
| `Me(ctx)` | тариф, расход, остаток страниц и границы периода |

### Параметры страницы

`*PageOptions` — все поля необязательные, `nil` — значения по умолчанию:

| Поле | Пример | По умолчанию |
|---|---|---|
| `Landscape` | `pdfapi.Bool(true)` | `false` |
| `PaperWidth`, `PaperHeight` | `"8.27in"`, `"297mm"` | A4 |
| `MarginTop`, `MarginBottom`, `MarginLeft`, `MarginRight` | `"20mm"` | `"0.39in"` |
| `WaitDelay` | `"2s"`, `"500ms"` — подождать, пока скрипт на странице дорисует график; до 10 секунд | `"0s"` |

Размер — число и единица `pt`, `px`, `in`, `mm`, `cm` или `pc` без пробела: `"20mm"`, `"1.5cm"`, `".5in"`. Остальное — `ErrInvalidArgument` до запроса: `"20"` — это миллиметры или дюймы? А `"20 mm"` и `"20MM"` рендер не понимает.

### Office-документы

```go
pdf, err := client.OfficeFile(ctx, "report.docx", nil)            // с диска
pdf, err = client.Office(ctx, "table.xlsx", data, pdfapi.Bool(true)) // из памяти — имя с расширением
```

Файл — не больше 3,7 МБ: после base64 тело запроса не должно превысить 5 МБ. docx, xlsx и pptx — ZIP-архивы, поэтому старый `.doc` или PDF, переименованный в `.docx`, отклоняется сразу, не отправляя мегабайты на сервер.

## Результат

`*Result`:
- `Content []byte` — байты PDF;
- `Pages *int` — сколько страниц списано;
- `Quota *Quota{Limit, Used, Remaining}` — остаток квоты после этой конвертации;
- `Save(path)` — записать в файл;
- `WriteTo(w)` — записать в `io.Writer`, например в `http.ResponseWriter` (`*Result` — это `io.WriterTo`).

Если сервер не прислал заголовки, указатели — `nil`.

`Save` пишет атомарно: сначала во временный файл рядом, потом переименовывает поверх. Оборвётся запись или кончится место — на месте останется прежний файл, а не обрезанный PDF. Каталог не создаётся.

## Отдать PDF из веб-приложения

PDF уже в памяти — отдавайте байты, на диск сохранять незачем:

```go
http.HandleFunc("/invoice", func(w http.ResponseWriter, r *http.Request) {
	// r.Context(): клиент ушёл — запрос к pdfapi тоже отменяется.
	pdf, err := client.HTML(r.Context(), "<h1>Счёт №42</h1>", nil)
	if err != nil {
		http.Error(w, "PDF не получился", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	_, _ = pdf.WriteTo(w)
})
```

Данные пользователя в HTML — только через `html/template` или `html.EscapeString`, иначе это HTML-инъекция в документ. Целиком, с обработкой ошибок, — [`examples/server`](examples/server/main.go).

## Аккаунт и квота

```go
me, err := client.Me(ctx)
if err != nil {
	log.Fatal(err)
}
fmt.Println(me.Plan, me.PagesRemaining, "из", me.PagesLimit, "— обнулится", me.PeriodEnd)
```

`*Account`: `UserID`, `Plan` (`free`, `premium`, `business`), `PeriodStart` и `PeriodEnd` (когда счётчик начнётся с нуля; UTC, нулевое время — сервер не сообщил), `PagesUsed`, `PagesLimit`, `PagesRemaining`, `RequestsPerSecond`, `Burst`. Запрос страниц не тратит. Проверка для cron — [`examples/quota`](examples/quota/main.go).

## Ошибки

Ошибки API — `*pdfapi.Error` с `Code`, `Status`, `Message` (по-русски). Вид ошибки проверяется через `errors.Is`:

```go
pdf, err := client.HTML(ctx, html, nil)
if err != nil {
	var apiErr *pdfapi.Error
	switch {
	case errors.Is(err, pdfapi.ErrQuotaExceeded):
		// страницы кончились
	case errors.Is(err, pdfapi.ErrRateLimit) && errors.As(err, &apiErr):
		time.Sleep(apiErr.RetryAfter)
	case errors.Is(err, pdfapi.ErrRender): // и ErrRenderTimeout тоже
		// разметка не отрендерилась
	case errors.As(err, &apiErr):
		log.Println(apiErr.Code, apiErr.Status, apiErr.Message)
	default:
		log.Println(err) // например, context.Canceled — отменили вы сами
	}
	return
}
fmt.Println(*pdf.Pages, "стр.")
```

| Значение | Когда |
|---|---|
| `ErrAuthentication` | 401: ключа нет, он неверный или отозван |
| `ErrPermissionDenied` | 403: почта не подтверждена или формат недоступен на тарифе |
| `ErrQuotaExceeded` | 402: страницы кончились |
| `ErrInvalidRequest` | 400: неверный параметр или адрес во внутреннюю сеть (`Code == "url_not_allowed"`) |
| `ErrPayloadTooLarge` | 413, это и `ErrInvalidRequest` |
| `ErrRateLimit` | 429; `RetryAfter` — через сколько можно снова |
| `ErrRender` | 502: документ не отрендерился |
| `ErrRenderTimeout` | 504, это и `ErrRender` |
| `ErrUnavailable` | 503 |
| `ErrConnection` | нет связи |
| `ErrTimeout` | API не ответил за таймаут, это и `ErrConnection` |
| `ErrInvalidArgument` | неверный аргумент, до запроса; лимит частоты не тратится |
| `*Error` с `Code == "not_found"` | 404: неверный базовый адрес — обычно в нём забыли `/api` |

## Повторы

SDK сам повторяет запрос, когда это безопасно — по умолчанию до 2 раз (`WithMaxRetries`):
- **429** — через `Retry-After` (или `retry_after_sec` из ответа), но не дольше минуты;
- **503** и **не удалось соединиться** — через 0,5 и 1 с (с небольшим разбросом).

**Таймаут и обрыв посреди ответа не повторяются:** сервер мог успеть сделать PDF и списать страницы, и повтор списал бы их второй раз.

## Таймауты и отмена

- Таймаут клиента — `WithTimeout`, по умолчанию 40 с на одну попытку.
- Для одного вызова — `context.WithTimeout`: действует меньший из двух.
- Если контекст отменили вы, вернётся сам `ctx.Err()`, а не ошибка API.

## Настройки клиента

```go
client, err := pdfapi.NewClient(
	pdfapi.WithAPIKey("pdf_live_..."),            // иначе PDFAPI_KEY
	pdfapi.WithBaseURL("https://pdfapi.ru/api"),   // иначе PDFAPI_BASE_URL
	pdfapi.WithTimeout(40*time.Second),
	pdfapi.WithMaxRetries(2),
	pdfapi.WithHTTPClient(&http.Client{Transport: myProxyTransport}),
)
if err != nil {
	log.Fatal(err) // нет ключа или неверные настройки
}
```

Таймаут не больше нуля или отрицательное число повторов — `ErrInvalidArgument` из `NewClient`. Ключа нет ни в `client.String()`, ни в текстах ошибок.

## Примеры

В папке [`examples/`](examples); запуск — `PDFAPI_KEY=pdf_live_... go run ./examples/<имя>`:

| Пример | Что показывает |
|---|---|
| `invoice` | счёт из `html/template` — данные экранируются сами |
| `from-url` | страница по адресу, альбомная, с ожиданием скриптов |
| `office` | docx, xlsx или pptx в PDF рядом с исходным файлом |
| `batch` | пачка документов в горутинах, в пределах лимита частоты |
| `quota` | остаток страниц и дата обнуления — для cron, код выхода 2 при нехватке |
| `server` | отдача PDF из `net/http`: `WriteTo`, контекст запроса, ошибки API — в HTTP-ответы |

## Разработка

```bash
go test -race ./...                 # юнит-тесты и общие кейсы (../tests/cases.json)
PDFAPI_IT_KEY=pdf_live_... PDFAPI_IT_BASE_URL=http://localhost:18080/api go test -run Integration -v
```

Общий контракт SDK — [`../SPEC.md`](../SPEC.md).
