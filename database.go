package donkeytownsfolk

import (
	"bytes"
	"errors"
	"fmt"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type (
	Money float64

	Db struct {
		db *mgo.Session
	}

	User struct {
		Name         string  // username (must be unique)
		PasswordHash []byte  // password as hashed using bcrypt
		SessionKey   []byte  // the last session key that was handed out for this user
		Decks        []*Deck // all of the user's decks
	}

	Deck struct {
		Name         string
		CreationDate time.Time
		PriceLimit   Money
		StagingArea  Snapshot
		Snapshots    []*Snapshot
	}

	Snapshot struct {
		Date               time.Time
		Decklist           []*CardEntry
		Sideboard          []*CardEntry
		Commander          CommanderEntry
		IsGrandfatherLegal bool
	}

	CardEntry struct {
		Name     string
		Count    int
		PricePer Money
		NotFound bool
	}

	CommanderEntry struct {
		Name      string
		Price     Money
		IsPresent bool
		NotFound  bool
	}

	PriceDbEntry struct {
		ID    string // Lower case name without any non-alphanumeric characters
		Name  string
		Price Money
	}

	ScraperStats struct {
		LastPriceUpdate      time.Time
		LastPriceUpdateError error
	}
)

const (
	MongoServerAddress                    = "127.0.0.1"
	MongoDbName                           = "donkeytownsfolk"
	MongoUsersCollectionName              = "users"
	MongoPricesCollectionName             = "prices"
	MongoScraperStatsCollectionName       = "scraperstats"
	Free                            Money = 0
)

var (
	UserAlreadyExistsError = errors.New("User already exists")
	UserNotFoundError      = errors.New("User not found")
)

func ParseCardEntryLine(s string) *CardEntry {
	r, err := regexp.Compile("(?:([0-9]+)[xX]?\\s+)?(\\w.+)")
	if err != nil {
		fmt.Errorf("regex error: %s", err.Error())
		return nil
	}

	matches := r.FindAllStringSubmatch(s, -1)

	if len(matches) != 1 {
		return nil
	}

	if len(matches[0]) != 3 {
		return nil
	}

	count, err := strconv.Atoi(matches[0][1])
	if err != nil {
		count = 1
	}

	return &CardEntry{Count: count, Name: strings.TrimSpace(matches[0][2])}
}

func ParseCardEntryLines(s string) []*CardEntry {
	entries := []*CardEntry{}
	for _, s := range strings.Split(s, "\r\n") {
		entry := ParseCardEntryLine(s)
		if entry != nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

func OpenDb() (*Db, error) {
	db, err := mgo.Dial(MongoServerAddress)
	if err != nil {
		return nil, err
	}

	c := db.DB(MongoDbName).C(MongoPricesCollectionName)
	c.EnsureIndexKey("id")

	return &Db{db}, nil
}

func (db *Db) GetScraperStats() (*ScraperStats, error) {
	c := db.db.DB(MongoDbName).C(MongoScraperStatsCollectionName)
	s := ScraperStats{}
	err := c.Find(nil).One(&s)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (db *Db) SetScraperStats(s *ScraperStats) error {
	c := db.db.DB(MongoDbName).C(MongoScraperStatsCollectionName)
	if _, err := c.RemoveAll(nil); err != nil {
		return err
	}
	return c.Insert(s)
}

func (user *User) NormalizedName() string {
	return normalizeString(user.Name)
}

func (user *User) FindDeck(name string) *Deck {
	for _, d := range user.Decks {
		if d.NormalizedName() == normalizeString(name) {
			return d
		}
	}
	return nil
}

func (user *User) AllDecks() []*Deck {
	decks := decks(user.Decks)
	sort.Sort(decks)
	return decks
}

func (d *Deck) NormalizedName() string {
	return normalizeString(d.Name)
}

func (d *Deck) PrettyCreationDate() string {
	return fmt.Sprintf("%d/%d/%d", d.CreationDate.Year(), d.CreationDate.Month(), d.CreationDate.Day())
}

// gets whether or not the decklist in the staging area exactly matches the most recent snapshot
func (d *Deck) IsSaved() bool {
	if len(d.Snapshots) == 0 {
		return false
	}
	snap := d.Snapshots[len(d.Snapshots)-1]
	return d.StagingArea.HasIdenticalCards(snap) && d.StagingArea.TotalPrice() == snap.TotalPrice()
}

func (d *Deck) SnapshotsReversed() []*Snapshot {
	ret := make([]*Snapshot, len(d.Snapshots), len(d.Snapshots))
	for i := 0; i < len(d.Snapshots); i++ {
		ret[(len(d.Snapshots)-1)-i] = d.Snapshots[i]
	}
	return ret
}

// gets the snapshot that contains the best-case current price (that is, the snapshot that contains the most recent
// decklist and has the lowest price).
func (d *Deck) CurrentPriceSnapshot() *Snapshot {
	if len(d.Snapshots) == 0 {
		return nil
	}

	lastSnap := d.Snapshots[len(d.Snapshots)-1]
	bestSnap := lastSnap

	// we can check against any older snapshots as well that contain exactly the same deck
	for i := len(d.Snapshots) - 2; i >= 0; i-- {
		if !d.Snapshots[i].HasIdenticalCards(lastSnap) {
			break
		}
		if d.Snapshots[i].TotalPrice() < bestSnap.TotalPrice() {
			bestSnap = d.Snapshots[i]
		}
	}

	return bestSnap
}

func (d *Deck) IsGrandfatherLegal() bool {
	s := d.CurrentPriceSnapshot()
	return s != nil && s.IsGrandfatherLegal
}

func (d *Deck) IsSnapshotLegal(s *Snapshot) bool {
	if s.Commander.IsPresent && s.Commander.NotFound {
		return false
	}
	for _, c := range s.Decklist {
		if c.NotFound {
			return false
		}
	}
	for _, c := range s.Sideboard {
		if c.NotFound {
			return false
		}
	}

	return s != nil && (s.IsGrandfatherLegal || (s.TotalPrice() <= d.PriceLimit))
}

func (d *Deck) IsLegal() bool {
	return d.IsSnapshotLegal(d.CurrentPriceSnapshot())
}

func (d *Deck) IsStagingAreaLegal() bool {
	return d.IsSnapshotLegal(&d.StagingArea)
}

func (db *Db) NameAndPrice(id string) (string, Money, error) {
	c := db.db.DB(MongoDbName).C(MongoPricesCollectionName)
	e := PriceDbEntry{}
	err := c.Find(bson.M{"id": id}).One(&e)
	if err != nil {
		return "", Free, err
	}
	return e.Name, e.Price, nil
}

func (db *Db) UpdateAllPrices(prices []*PriceDbEntry) error {
	ins := make([]interface{}, len(prices))
	for i := 0; i < len(prices); i++ {
		ins[i] = prices[i]
	}

	c := db.db.DB(MongoDbName).C(MongoPricesCollectionName)
	if _, err := c.RemoveAll(nil); err != nil {
		return err
	}
	return c.Insert(ins...)
}

func (db *Db) AllUsers() ([]*User, error) {
	c := db.db.DB(MongoDbName).C(MongoUsersCollectionName)
	users := []*User{}
	if err := c.Find(nil).Sort("name").All(&users); err != nil {
		return nil, err
	}
	return users, nil
}

func (db *Db) FindUser(name string) (*User, error) {
	c := db.db.DB(MongoDbName).C(MongoUsersCollectionName)

	user := User{}
	iter := c.Find(nil).Iter()
	for iter.Next(&user) {
		if normalizeString(name) == user.NormalizedName() {
			return &user, nil
		}
	}

	if err := iter.Close(); err != nil {
		return nil, err
	}

	return nil, UserNotFoundError
}

func (db *Db) AddUser(name string, passwordHash []byte) (*User, error) {
	if _, err := db.FindUser(name); err != UserNotFoundError {
		return nil, UserAlreadyExistsError
	}

	c := db.db.DB(MongoDbName).C(MongoUsersCollectionName)
	user := User{name, passwordHash, []byte{}, []*Deck{}}
	c.Insert(user)
	return &user, nil
}

func (db *Db) DeleteUser(user *User) error {
	c := db.db.DB(MongoDbName).C(MongoUsersCollectionName)
	return c.Remove(bson.M{"name": user.Name})
}

func (db *Db) UpdateUser(user *User) error {
	c := db.db.DB(MongoDbName).C(MongoUsersCollectionName)
	return c.Update(bson.M{"name": user.Name}, user)
}

func (m Money) String() string {
	return fmt.Sprintf("$%.2f", m)
}

func (m Money) SortableString() string {
	return fmt.Sprintf("%07.2f", m)
}

func (m Money) SimpleString() string {
	return fmt.Sprintf("%d", int(m))
}

func (c *CardEntry) TotalPrice() Money {
	return c.PricePer * Money(c.Count)
}

func (s *Snapshot) TotalPrice() Money {
	ret := Free
	for _, c := range s.Decklist {
		ret += c.TotalPrice()
	}
	for _, c := range s.Sideboard {
		ret += c.TotalPrice()
	}
	if s.Commander.IsPresent {
		ret += s.Commander.Price
	}
	return ret
}

func (s *Snapshot) TotalDecklistCount() int {
	ret := 0
	for _, c := range s.Decklist {
		ret += c.Count
	}
	if s.Commander.IsPresent {
		ret += 1
	}
	return ret
}

func (s *Snapshot) TotalSideboardCount() int {
	ret := 0
	for _, c := range s.Sideboard {
		ret += c.Count
	}
	return ret
}

func (s *Snapshot) Clone() *Snapshot {
	ret := &Snapshot{}
	ret.Date = s.Date
	ret.Commander = s.Commander
	ret.IsGrandfatherLegal = s.IsGrandfatherLegal

	ret.Decklist = make([]*CardEntry, len(s.Decklist), len(s.Decklist))
	for i := 0; i < len(s.Decklist); i++ {
		card := *s.Decklist[i]
		ret.Decklist[i] = &card
	}

	ret.Sideboard = make([]*CardEntry, len(s.Sideboard), len(s.Sideboard))
	for i := 0; i < len(s.Sideboard); i++ {
		card := *s.Sideboard[i]
		ret.Sideboard[i] = &card
	}

	return ret
}

func (s *Snapshot) DecklistDump() string {
	var buffer bytes.Buffer
	for _, c := range s.Decklist {
		buffer.WriteString(fmt.Sprintf("%d %s\n", c.Count, c.Name))
	}
	return buffer.String()
}

func (s *Snapshot) SideboardDump() string {
	var buffer bytes.Buffer
	for _, c := range s.Sideboard {
		buffer.WriteString(fmt.Sprintf("%d %s\n", c.Count, c.Name))
	}
	return buffer.String()
}

func (s *Snapshot) PrettyDate() string {
	return fmt.Sprintf("%d/%d/%d", s.Date.Year(), s.Date.Month(), s.Date.Day())
}

func (s *Snapshot) HasIdenticalCards(other *Snapshot) bool {
	return s.DecklistDump() == other.DecklistDump() &&
		s.SideboardDump() == other.SideboardDump() &&
		s.Commander.IsPresent == other.Commander.IsPresent &&
		s.Commander.Name == other.Commander.Name
}

func normalizeString(s string) string {
	buffer := bytes.Buffer{}
	for _, c := range s {
		if c == '$' {
			continue
		}
		buffer.WriteRune(normalizeRune(c))
	}
	return buffer.String()
}

func normalizeRune(r rune) rune {
	if unicode.IsLower(r) || unicode.IsNumber(r) {
		return r
	} else if unicode.IsUpper(r) {
		return unicode.ToLower(r)
	} else {
		return '-'
	}
}

// --- auxillary types/functions for sorting ---

type decks []*Deck

func (d decks) Len() int {
	return len(d)
}

func (d decks) Swap(i, j int) {
	d[i], d[j] = d[j], d[i]
}

func (d decks) Less(i, j int) bool {
	return d[i].Name < d[i].Name
}
