package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"health-checker/checker"
)

func main() {
	// Определение флагов командной строки
	urlsFile := flag.String("file", "", "Файл со списком URL (по одному на строку)")
	timeout := flag.Duration("timeout", 10*time.Second, "Таймаут для каждого запроса")
	concurrent := flag.Int("concurrent", 5, "Количество одновременных проверок")
	outputFormat := flag.String("format", "table", "Формат вывода: table, json, csv")
	retries := flag.Int("retries", 1, "Количество повторных попыток при ошибке")
	flag.Parse()

	// Получаем URL из аргументов или файла
	var urls []string
	if *urlsFile != "" {
		data, err := os.ReadFile(*urlsFile)
		if err != nil {
			fmt.Printf("Ошибка чтения файла: %v\n", err)
			os.Exit(1)
		}
		urls = parseURLs(string(data))
	} else {
		urls = flag.Args()
	}

	if len(urls) == 0 {
		fmt.Println("Использование: health-checker [опции] <url1> <url2> ...")
		fmt.Println("\nОпции:")
		flag.PrintDefaults()
		fmt.Println("\nПримеры:")
		fmt.Println("  health-checker https://google.com https://github.com")
		fmt.Println("  health-checker -file=urls.txt -concurrent=10")
		fmt.Println("  health-checker -timeout=5s -format=json https://example.com")
		os.Exit(1)
	}

	// Создаем конфигурацию для проверки
	config := &checker.Config{
		Timeout:     *timeout,
		Concurrency: *concurrent,
		Retries:     *retries,
		UserAgent:   "HealthChecker/1.0",
	}

	// Создаем и запускаем проверку
	hc := checker.NewHealthChecker(config)
	results := hc.CheckAll(urls)

	// Выводим результаты в выбранном формате
	switch strings.ToLower(*outputFormat) {
	case "json":
		checker.PrintJSON(results)
	case "csv":
		checker.PrintCSV(results)
	case "simple":
		checker.PrintSimple(results)
	default:
		checker.PrintTable(results)
	}

	// Выводим статистику
	checker.PrintStats(results)
}
