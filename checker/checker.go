package checker

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/rodaine/table"
)

// Результат проверки
type Result struct {
	URL        string        `json:"url"`
	Status     string        `json:"status"`     // "success", "error", "timeout"
	StatusCode int           `json:"statusCode"` // HTTP статус
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration"`
	Timestamp  time.Time     `json:"timestamp"`
	Retries    int           `json:"retries"`
}

// Конфигурация проверки
type Config struct {
	Timeout     time.Duration
	Concurrency int
	Retries     int
	UserAgent   string
}

// HealthChecker основной тип
type HealthChecker struct {
	config    *Config
	client    *http.Client
	wg        sync.WaitGroup
	semaphore chan struct{} // семафор для ограничения concurrency
	results   chan Result
	mu        sync.Mutex
}

// NewHealthChecker создает новый экземпляр HealthChecker
func NewHealthChecker(config *Config) *HealthChecker {
	if config == nil {
		config = &Config{
			Timeout:     10 * time.Second,
			Concurrency: 5,
			Retries:     1,
			UserAgent:   "HealthChecker/1.0",
		}
	}

	return &HealthChecker{
		config: config,
		client: &http.Client{
			Timeout: config.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     30 * time.Second,
			},
		},
		semaphore: make(chan struct{}, config.Concurrency),
		results:   make(chan Result, 100),
	}
}

// checkURL проверяет один URL
func (hc *HealthChecker) checkURL(ctx context.Context, url string) Result {
	start := time.Now()
	var lastErr error
	//var statusCode int

	// Пытаемся выполнить запрос с повторами
	for attempt := 0; attempt <= hc.config.Retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", hc.config.UserAgent)

		resp, err := hc.client.Do(req)
		if err != nil {
			lastErr = err
			// Если это последняя попытка, возвращаем ошибку
			if attempt == hc.config.Retries {
				return Result{
					URL:       url,
					Status:    "error",
					Error:     err.Error(),
					Duration:  time.Since(start),
					Timestamp: time.Now(),
					Retries:   attempt,
				}
			}
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()

		// Читаем немного тела ответа для подтверждения
		io.CopyN(io.Discard, resp.Body, 4096)
		//	statusCode = resp.StatusCode

		// Определяем статус на основе кода ответа
		status := "success"
		if resp.StatusCode >= 400 {
			status = "error"
		}

		return Result{
			URL:        url,
			Status:     status,
			StatusCode: resp.StatusCode,
			Duration:   time.Since(start),
			Timestamp:  time.Now(),
			Retries:    attempt,
		}
	}

	return Result{
		URL:       url,
		Status:    "error",
		Error:     lastErr.Error(),
		Duration:  time.Since(start),
		Timestamp: time.Now(),
		Retries:   hc.config.Retries,
	}
}

// worker обрабатывает URL из канала
func (hc *HealthChecker) worker(ctx context.Context, urls <-chan string) {
	defer hc.wg.Done()
	for url := range urls {
		hc.semaphore <- struct{}{} // занимаем слот
		result := hc.checkURL(ctx, url)
		hc.results <- result
		<-hc.semaphore // освобождаем слот
	}
}

// CheckAll проверяет все URL конкурентно
func (hc *HealthChecker) CheckAll(urls []string) []Result {
	ctx, cancel := context.WithTimeout(context.Background(), hc.config.Timeout*time.Duration(len(urls)))
	defer cancel()

	urlChan := make(chan string, len(urls))
	allResults := make([]Result, 0, len(urls))

	// Запускаем воркеры
	hc.wg.Add(hc.config.Concurrency)
	for i := 0; i < hc.config.Concurrency; i++ {
		go hc.worker(ctx, urlChan)
	}

	// Отправляем URL воркерам
	go func() {
		for _, url := range urls {
			urlChan <- normalizeURL(url)
		}
		close(urlChan)
	}()

	// Собираем результаты
	go func() {
		hc.wg.Wait()
		close(hc.results)
	}()

	for result := range hc.results {
		allResults = append(allResults, result)
	}

	return allResults
}

// normalizeURL нормализует URL (добавляет схему если нужно)
func normalizeURL(url string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "https://" + url
	}
	return url
}

// Вспомогательные функции для вывода

// PrintTable выводит результаты в виде таблицы
func PrintTable(results []Result) {
	if len(results) == 0 {
		return
	}

	tbl := table.New("URL", "Status", "Code", "Duration", "Retries")
	headerFmt := color.New(color.FgGreen, color.Underline).SprintfFunc()
	columnFmt := color.New(color.FgYellow).SprintfFunc()
	tbl.WithHeaderFormatter(headerFmt).WithFirstColumnFormatter(columnFmt)

	for _, r := range results {
		statusColor := color.New(color.FgGreen)
		if r.Status == "error" {
			statusColor = color.New(color.FgRed)
		}

		statusText := statusColor.Sprintf(r.Status)
		if r.StatusCode > 0 {
			statusText = fmt.Sprintf("%s (%d)", statusText, r.StatusCode)
		}

		tbl.AddRow(
			r.URL,
			statusText,
			r.StatusCode,
			r.Duration.Round(time.Millisecond),
			r.Retries,
		)
	}

	tbl.Print()
}

// PrintJSON выводит результаты в формате JSON
func PrintJSON(results []Result) {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fmt.Printf("Ошибка при создании JSON: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

// PrintCSV выводит результаты в формате CSV
func PrintCSV(results []Result) {
	writer := csv.NewWriter(os.Stdout)
	writer.Write([]string{"URL", "Status", "StatusCode", "Duration", "Timestamp", "Retries"})

	for _, r := range results {
		writer.Write([]string{
			r.URL,
			r.Status,
			fmt.Sprintf("%d", r.StatusCode),
			r.Duration.String(),
			r.Timestamp.Format(time.RFC3339),
			fmt.Sprintf("%d", r.Retries),
		})
	}

	writer.Flush()
}

// PrintSimple выводит результаты в простом формате
func PrintSimple(results []Result) {
	for _, r := range results {
		icon := "✓"
		if r.Status == "error" {
			icon = "✗"
		}
		fmt.Printf("%s %s - %s (took %v)\n",
			icon,
			r.URL,
			r.Status,
			r.Duration.Round(time.Millisecond),
		)
		if r.Error != "" {
			fmt.Printf("  Error: %s\n", r.Error)
		}
	}
}

// PrintStats выводит статистику проверки
func PrintStats(results []Result) {
	if len(results) == 0 {
		return
	}

	var totalTime time.Duration
	successCount := 0
	errorCount := 0

	for _, r := range results {
		totalTime += r.Duration
		if r.Status == "success" {
			successCount++
		} else {
			errorCount++
		}
	}

	avgTime := totalTime / time.Duration(len(results))

	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Printf("Статистика:\n")
	fmt.Printf("  Всего проверок: %d\n", len(results))
	fmt.Printf("  Успешных: %d\n", successCount)
	fmt.Printf("  Ошибок: %d\n", errorCount)
	fmt.Printf("  Среднее время: %v\n", avgTime.Round(time.Millisecond))
	fmt.Printf("  Общее время: %v\n", totalTime.Round(time.Millisecond))
	fmt.Printf("  Успешность: %.1f%%\n", float64(successCount)/float64(len(results))*100)
}
