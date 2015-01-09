package donkeytownsfolk

import (
	"math/rand"
	"time"
)

var random = rand.New(rand.NewSource(time.Now().UTC().UnixNano()))

// calculates all of the prices for each card using a web API (hopefully tcgplayer)
func (s *Snapshot) CalculatePrices() error {
	var err error
	if s.Commander.IsPresent {
		s.Commander.Price, err = calculatePrice(s.Commander.Name)
		if err != nil {
			return err
		}
	}
	for _, c := range s.Decklist {
		err = c.calculatePrice()
		if err != nil {
			return err
		}
	}
	for _, c := range s.Sideboard {
		err = c.calculatePrice()
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *CardEntry) calculatePrice() error {
	var err error
	c.PricePer, err = calculatePrice(c.Name)
	return err
}

func calculatePrice(card string) (Money, error) {
	// until we have a real calculation, use a random number between 1-30c
	return Money(random.Int()%30+1) / 100, nil
}
