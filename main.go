package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// The JSON response that developers buying the API will receive
type ProductResponse struct {
	Status   int     `json:"status"`
	ASIN     string  `json:"asin"`
	URL      string  `json:"url"`
	Title    string  `json:"title"`
	Price    float64 `json:"price"` // Float makes it easier for devs to use
	Currency string  `json:"currency"`
	Image    string  `json:"image"`
	Rating   string  `json:"rating"`
	Reviews  string  `json:"reviews"`
	InStock  bool    `json:"in_stock"`
	Error    string  `json:"error,omitempty"`
}

var client = &http.Client{
	Timeout: 15 * time.Second,
}

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.3 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:123.0) Gecko/20100101 Firefox/123.0",
}

func main() {
	rand.Seed(time.Now().UnixNano())

	http.HandleFunc("/api/v1/amazon", handleScrape)

	port := ":8000"
	fmt.Printf("🚀 PRO Amazon RapidAPI Scraper running on port %s\n", port)
	fmt.Println("📍 Test URL: http://localhost:8000/api/v1/amazon?asin=B08J5F3G18")

	if err := http.ListenAndServe(port, nil); err != nil {
		fmt.Printf("Server failed: %s\n", err)
	}
}

func handleScrape(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	asin := r.URL.Query().Get("asin")
	if asin == "" {
		sendError(w, 400, "Missing required query parameter: 'asin'")
		return
	}

	result := scrapeProduct(asin)

	w.WriteHeader(result.Status)
	json.NewEncoder(w).Encode(result)
}

func scrapeProduct(asin string) ProductResponse {
	url := fmt.Sprintf("https://www.amazon.com/dp/%s", asin)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ProductResponse{Status: 500, ASIN: asin, Error: "Server Request Error"}
	}

	// Pro-grade headers to look like a real browser (Prevents Amazon 503s)
	req.Header.Set("User-Agent", userAgents[rand.Intn(len(userAgents))])
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="122", "Not(A:Brand";v="24", "Google Chrome";v="122"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Cache-Control", "max-age=0")

	resp, err := client.Do(req)
	if err != nil {
		return ProductResponse{Status: 502, ASIN: asin, Error: "Upstream Timeout"}
	}
	defer resp.Body.Close()

	if resp.StatusCode == 503 {
		return ProductResponse{Status: 503, ASIN: asin, Error: "Amazon CAPTCHA Challenge Hit (IP Blocked temporarily)"}
	}
	if resp.StatusCode == 404 {
		return ProductResponse{Status: 404, ASIN: asin, Error: "Product ASIN Not Found"}
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return ProductResponse{Status: 500, ASIN: asin, Error: "HTML Parsing Error"}
	}

	// 1. EXTRACT TITLE
	title := strings.TrimSpace(doc.Find("#productTitle").First().Text())

	// 2. EXTRACT PRICE (Amazon has 5+ different price DOM structures)
	priceStr := ""
	priceSelectors := []string{
		"#corePriceDisplay_desktop_feature_div .a-price .a-offscreen",
		"#priceblock_ourprice",
		"#priceblock_dealprice",
		".a-price.priceToPay span.a-offscreen",
		"#twister-plus-price-data-price",
	}

	for _, selector := range priceSelectors {
		val := doc.Find(selector).First().Text()
		if val != "" {
			priceStr = strings.TrimSpace(val)
			break
		}
	}

	// Value inside an attribute fallback
	if priceStr == "" {
		val, exists := doc.Find("#twister-plus-price-data-price").Attr("value")
		if exists {
			priceStr = strings.TrimSpace(val)
		}
	}

	// 3. CLEAN CURRENCY & FLOAT PARSING
	currency := ""
	var finalPrice float64 = 0.0

	if priceStr != "" {
		runes := []rune(priceStr)
		if len(runes) > 0 {
			first := runes[0]
			if first == '$' || first == '£' || first == '€' {
				currency = string(first)
				priceStr = string(runes[1:])
			}
		}
		// Remove commas
		priceStr = strings.ReplaceAll(priceStr, ",", "")
		fmt.Sscanf(priceStr, "%f", &finalPrice)
	}

	// 4. EXTRACT IMAGE (High-res from JSON payload)
	imgData, exists := doc.Find("#landingImage").Attr("data-a-dynamic-image")
	image := ""
	if exists {
		var imgMap map[string]interface{}
		json.Unmarshal([]byte(imgData), &imgMap)
		for key := range imgMap {
			image = key
			break // First key is usually the main image URL
		}
	}

	// 5. EXTRACT RATINGS & REVIEWS
	ratingRaw := strings.TrimSpace(doc.Find("#acrPopover").First().AttrOr("title", ""))
	rating := ""
	if len(ratingRaw) >= 3 {
		rating = ratingRaw[:3] // "4.7 out of 5 stars" -> "4.7"
	}

	reviews := strings.TrimSpace(doc.Find("#acrCustomerReviewText").First().Text())
	reviews = strings.Split(reviews, " ")[0] // "147,959 ratings" -> "147,959"

	// 6. AVAILABILITY
	availability := strings.ToLower(doc.Find("#availability").Text())
	inStock := !strings.Contains(availability, "currently unavailable") && !strings.Contains(availability, "out of stock")
	if title == "" {
		inStock = false
	}

	return ProductResponse{
		Status:   200,
		ASIN:     asin,
		URL:      url,
		Title:    title,
		Price:    finalPrice,
		Currency: currency,
		Image:    image,
		Rating:   rating,
		Reviews:  reviews,
		InStock:  inStock,
	}
}

func sendError(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	w.Write([]byte(fmt.Sprintf(`{"status": %d, "error": "%s"}`, code, msg)))
}
