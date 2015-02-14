package donkeytownsfolk

import (
	"bytes"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"math/rand"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var random = rand.New(rand.NewSource(time.Now().UTC().UnixNano()))
var waitBetweenScrapes = 24 * time.Hour
var freeCards = []string{"Plains", "Island", "Swamp", "Mountain", "Forest"}

func (db *Db) ScrapeForever() {
	c := time.Tick(30 * time.Second)
	for now := range c {
		stats, err := db.GetScraperStats()
		if err != nil || stats.LastPriceUpdate.Add(waitBetweenScrapes).Before(now) {
			db.scrapePricesPeriodic()
		}
	}
}

func (db *Db) scrapePricesPeriodic() {
	err := db.scrapePrices()
	if err == nil {
		fmt.Println("Price scraper: complete!")
		db.SetScraperStats(&ScraperStats{time.Now(), nil})
	} else {
		fmt.Println("Price scraper: failed!")
		fmt.Println(err.Error())
		db.SetScraperStats(&ScraperStats{time.Now(), err})
	}
}

func (db *Db) scrapePrices() error {
	doc, err := goquery.NewDocument("http://magic.tcgplayer.com/all_magic_sets.asp")
	if err != nil {
		return err
	}

	setLinks := []string{}
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		prefix := "/db/search_result.asp?set_name="
		val, exists := s.Attr("href")
		if exists && strings.HasPrefix(strings.ToLower(val), prefix) {
			if strings.HasSuffix(val, "Magic 2010") {
				// I have no idea why, but the link for M10 is wrong...
				val += " (M10)"
			}
			setLinks = append(setLinks, "http://magic.tcgplayer.com"+val)
		}
	})

	lowestPrice := map[string]*PriceDbEntry{}
	for _, link := range setLinks {
		entries, err := scrapePage(link)
		if err != nil {
			return err
		}
		for _, e := range entries {
			val, exists := lowestPrice[e.ID]
			if !exists || e.Price < val.Price {
				lowestPrice[e.ID] = e
			}
		}
		fmt.Printf("Price scraper: finished %s\n", link)
	}

	allEntries := make([]*PriceDbEntry, len(lowestPrice))
	i := 0
	for _, e := range lowestPrice {
		allEntries[i] = e
		i++
	}

	return db.UpdateAllPrices(allEntries)
}

func scrapePage(url string) ([]*PriceDbEntry, error) {
	time.Sleep(1000 * time.Millisecond) // just so we don't DoS the server too badly

	url = strings.Replace(url, " ", "+", -1) // sometimes the links come in this way (no idea why)

	doc, err := goquery.NewDocument(url)
	if err != nil {
		return nil, err
	}

	entries := []*PriceDbEntry{}
	doc.Find("td").Each(func(i int, s *goquery.Selection) {

		bgColors := []string{"#D1DFFC", "#E6F4FF"}
		val, exists := s.Attr("bgcolor")
		if !exists {
			return
		}

		isCorrectColor := false
		for _, x := range bgColors {
			if val == x {
				isCorrectColor = true
			}
		}

		if !isCorrectColor {
			return
		}

		val, exists = s.Find("a").Attr("href")
		if !exists {
			return
		}

		idx := strings.Index(val, "cn=")
		if idx < 0 {
			return
		}

		val = val[idx+3:]
		idx = strings.Index(val, "&")
		if idx < 0 {
			return
		}
		cardName := val[0:idx]

		price := s.Text()
		price = strings.TrimSpace(price)
		price = strings.Trim(price, "$")
		price = strings.Replace(price, ",", "", -1)
		priceFloat, err := strconv.ParseFloat(price, 64)
		if err != nil {
			panic(err)
		}

		entries = append(
			entries,
			&PriceDbEntry{nameToId(cardName), cardName, Money(priceFloat)})
	})

	return entries, nil
}

// creates an ID from a name by trimming all non-alphanumeric characters
func nameToId(name string) string {
	buffer := bytes.Buffer{}
	for _, c := range name {
		if unicode.IsLetter(c) || unicode.IsNumber(c) {
			buffer.WriteRune(unicode.ToLower(c))
		}
	}
	return buffer.String()
}

// calculates all of the prices for each card
func (s *Snapshot) CalculatePrices(db *Db) {
	if s.Commander.IsPresent {
		n, p, exists := calculateNameAndPrice(db, s.Commander.Name)
		s.Commander.Name = n
		s.Commander.Price = p
		s.Commander.NotFound = !exists
	}
	for _, c := range s.Decklist {
		c.calculateNameAndPrice(db)
	}
	for _, c := range s.Sideboard {
		c.calculateNameAndPrice(db)
	}
}

func (c *CardEntry) calculateNameAndPrice(db *Db) {
	n, p, exists := calculateNameAndPrice(db, c.Name)
	c.Name = n
	c.PricePer = p
	c.NotFound = !exists
}

func calculateNameAndPrice(db *Db, origName string) (string, Money, bool) {
	id := nameToId(origName)
	for _, x := range freeCards {
		if id == nameToId(x) {
			return x, Free, true
		}
	}
	n, p, err := db.NameAndPrice(id)
	if err != nil {
		return origName, Free, false
	}
	return n, p, true
}
