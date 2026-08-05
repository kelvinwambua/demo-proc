package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gocolly/colly/v2"
)

type ScrapedTender struct {
	Title       string `json:"title"`
	Ref         string `json:"ref"`
	Entity      string `json:"entity"`
	ClosingDate string `json:"closing_date"`
}

func main() {
	targetURL := "https://www.tenders.go.ke/latest-tenders"
	results := make([]ScrapedTender, 0, 5)
	seen := map[string]bool{}

	c := colly.NewCollector(
		colly.AllowedDomains("www.tenders.go.ke", "tenders.go.ke"),
		colly.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
		colly.MaxDepth(1),
	)

	c.OnHTML("article, .tender-card, .tender-item, [class*='tender'], [class*='Tender'], tr", func(e *colly.HTMLElement) {
		if len(results) >= 5 {
			return
		}

		title := strings.TrimSpace(e.ChildText("h2, h3, h4, .title, .tender-title, a, td:first-child"))
		ref := strings.TrimSpace(e.ChildText(".ref, .reference, .tender-ref, [class*='ref'], td:nth-child(2)"))
		entity := strings.TrimSpace(e.ChildText(".entity, .procuring-entity, .agency, [class*='entity'], td:nth-child(3)"))
		closingDate := strings.TrimSpace(e.ChildText(".closing, .deadline, .date, [class*='closing'], [class*='deadline'], td:last-child"))

		if title == "" {
			return
		}

		key := title + "|" + ref
		if seen[key] {
			return
		}
		seen[key] = true

		results = append(results, ScrapedTender{
			Title:       title,
			Ref:         ref,
			Entity:      entity,
			ClosingDate: closingDate,
		})
	})

	c.OnError(func(r *colly.Response, err error) {
		fmt.Fprintf(os.Stderr, "scrape error %d: %v\n", r.StatusCode, err)
	})

	c.OnResponse(func(r *colly.Response) {
		fmt.Fprintf(os.Stderr, "fetched %s (%d bytes, %d)\n", r.Request.URL, len(r.Body), r.StatusCode)
	})

	if err := c.Visit(targetURL); err != nil {
		fmt.Fprintf(os.Stderr, "visit failed: %v\n", err)
	}

	c.Wait()

	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "no tenders scraped; website structure may have changed, try adjusting selectors")
	}

	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(out))
}
