package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/exp/slog"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-rod/rod"
)

type Kost struct {
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	RentPrice   string `json:"rent_price,omitempty"`
	Lokasi      string `json:"lokasi,omitempty"`
	Provinsi    string `json:"provinsi,omitempty"`
	Kota        string `json:"kota,omitempty"`
	Alamat      string `json:"alamat,omitempty"`
	JenisKost   string `json:"jenis_kost,omitempty"`
	FreeWiFi    bool   `json:"free_wifi,omitempty"`
	HasAC       bool   `json:"has_ac,omitempty"`
	KamarMandi  bool   `json:"kamar_mandi,omitempty"`
}

func (k *Kost) ToJSON() []byte {
	res, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		panic(err)
	}

	return res
}

var seenURLs map[string]bool
var outputDir string
var logger *slog.Logger

func init() {
	now := time.Now().UTC().Format("2006-01-02")
	syscall.Umask(0)
	outputDir = "data/" + now

	logger = getLogger()

	err := os.MkdirAll(outputDir, 0755)
	if err != nil {
		panic(err)
	}
}

func main() {

	seenURLs = make(map[string]bool)

	ctx, cancel := context.WithCancel(context.Background())
	browser := rod.New().NoDefaultDevice().MustConnect().Context(ctx)

	var wg sync.WaitGroup

	// Run the scripts for 20 seconds
	go func() {
		time.Sleep(20 * time.Second)
		fmt.Println("Cancelled.....")
		cancel()
	}()
	page := browser.MustPage("https://www.sewakost.com/kost.html")

	urls := make(chan string, 100)

	go crawlURL(urls, page)

	for w := 1; w <= 10; w++ {
		wg.Add(1)

		w := w
		go func() {
			defer wg.Done()
			worker(w, urls)
		}()
	}

	wg.Wait()
}

func crawlURL(urls chan<- string, page *rod.Page) {
	for {

		items := page.MustWaitStable().MustElements(".item")

		for _, item := range items {
			url := item.MustElement(".picture").MustElement("a").MustProperty("href").Str()

			if strings.TrimSpace(url) == "" {
				logger.Debug("URL is empty")
				continue
			}

			if _, ok := seenURLs[url]; ok {
				logger.Debug(fmt.Sprintf("%s is already visited.", url))
				continue
			}

			// network sama i/o
			select {
			case urls <- url:
				logger.Debug(fmt.Sprintf("Sent: %s", url))
				seenURLs[url] = true
			default:
				logger.Debug("Channel is full. Waiting the channel to be available... ")

				buffer := make([]string, 0, 10)
				buffer = append(buffer, url)

				for len(buffer) > 0 {
					select {
					case urls <- buffer[0]:
						logger.Debug(fmt.Sprintf("Sent buffered data: %s", buffer[0]))
						buffer = buffer[1:]
					default:
						logger.Debug("Channel is still full. Waiting....")
						time.Sleep(time.Second)
					}
				}
			}
		}

		button := page.MustElement("#controller_area > ul > li.navigator.rs > a")
		if button == nil {
			break
		}

		button.MustClick()
	}
}

func worker(wID int, jobs <-chan string) {
	for j := range jobs {
		logger.Debug(fmt.Sprintf("Worker %d started", wID))
		scrape(j)
		logger.Debug(fmt.Sprintf("Worker %d ended", wID))
	}
}

func scrape(urlString string) {
	logger.Info(fmt.Sprintf("Visiting: %s", urlString))

	kost := Kost{}

	resp, _ := http.Get(urlString)
	kost.URL = urlString

	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	getLocations(doc, &kost)
	getDescriptions(doc, &kost)
	getCommonInformation(doc, &kost)

	path := parsePath(urlString)

	jsonBytes := kost.ToJSON()
	filename := fmt.Sprintf("%s.json", path)
	filename = filepath.Join(outputDir, filename)
	writeJSON(jsonBytes, filename)

}

func getLocations(doc *goquery.Document, kost *Kost) {
	doc.Find("div.location").Find("div.table-cell.clearfix").Each(func(i int, s *goquery.Selection) {
		name, ok := s.Find("div.name").Attr("title")
		if !ok {
			logger.Info(fmt.Sprintf("%s doesn't have any location", kost.URL))
			return
		}
		name = strings.TrimSpace(name)

		value := s.Find("div.value").Text()
		value = strings.TrimSpace(value)

		switch name {
		case "Alamat":
			kost.Alamat = value
		case "Provinsi":
			kost.Provinsi = value
		case "Kota":
			kost.Kota = value
		}

	})
}

func getDescriptions(doc *goquery.Document, kost *Kost) {
	descriptionContainer := doc.Find("#df_field_additional_information")
	descText := descriptionContainer.Find("div.value").Text()

	kost.Description = descText
}

func getCommonInformation(doc *goquery.Document, kost *Kost) {
	doc.Find("div.common.row").Find("div.table-cell.clearfix").Each(func(i int, s *goquery.Selection) {
		name, ok := s.Find("div.name").Attr("title")
		if !ok {
			logger.Info(fmt.Sprintf("%s doesn't have any common informations.", kost.URL))
			return
		}

		name = strings.TrimSpace(name)

		value := s.Find("div.value").Text()
		value = strings.TrimSpace(value)

		switch name {
		case "Jenis Kost":
			kost.JenisKost = value
		case "AC":
			if value == "Ya" {
				kost.HasAC = true
			} else {
				kost.HasAC = false
			}
		case "Free WiFi":
			if value == "Ya" {
				kost.FreeWiFi = true
			} else {
				kost.FreeWiFi = false
			}
		case "Kamar Mandi Dalam":
			if value == "Ya" {
				kost.KamarMandi = true
			} else {
				kost.KamarMandi = false
			}
		}

	})
}

func writeJSON(jsonBytes []byte, fileName string) {
	err := ioutil.WriteFile(fileName, jsonBytes, 0644)
	logger.Info(fmt.Sprintf("Writing json to %s", fileName))
	if err != nil {
		panic(err)
	}
}

func parsePath(urlString string) string {

	urlParsed, err := url.Parse(urlString)
	if err != nil {
		panic(err)
	}

	pathList := strings.Split(urlParsed.Path, "/")
	path := pathList[len(pathList)-1]
	path = strings.Replace(path, ".html", "", -1)

	return path
}

func getLogger() *slog.Logger {
	slogLevel := new(slog.LevelVar)
	slogLevel.Set(slog.LevelDebug)

	textHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel})
	logger := slog.New(textHandler)

	slog.SetDefault(logger)

	return logger
}
