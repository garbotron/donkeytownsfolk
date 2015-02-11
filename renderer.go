package donkeytownsfolk

import (
	"bytes"
	"code.google.com/p/go.crypto/bcrypt"
	"errors"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
	"html/template"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type templateData struct {
	User         *User
	Subtitle     string
	InfoMessage  string
	ErrorMessage string
	SearchText   string
}

type deckData struct {
	Deck *Deck
	User *User
}

type filterResult struct {
	AllDecks     []*deckData
	CurrentDecks []*deckData
	CurrentPage  int
	NumPages     int
}

const (
	sessionName          = "session"
	filterResultsPerPage = 25
)

func SetupRenderer(db *Db, r *mux.Router) {
	// require our specific subdomain
	s := r.Host(Domain).Subrouter()

	// create the secure cookie store for Gorilla sessions
	store := sessions.NewCookieStore(masterKey())

	// serve files under /static using a standard file system server
	localStaticRoot := os.ExpandEnv("$GOPATH/src/github.com/garbotron/donkeytownsfolk/static")

	s.Handle("/static/{path:.*}", http.StripPrefix("/static/", http.FileServer(http.Dir(localStaticRoot))))

	// hookup all dynamic handlers
	s.HandleFunc("/", createHandler(db, store, renderFilterPage))
	s.HandleFunc("/deck", createHandler(db, store, renderDeckPage))
	s.HandleFunc("/snapshot", createHandler(db, store, renderSnapshotPage))
	s.HandleFunc("/login", createHandler(db, store, performLogin))
	s.HandleFunc("/logout", createHandler(db, store, performLogout))
	s.HandleFunc("/add-user", createHandler(db, store, performAddUser))
	s.HandleFunc("/change-password", createHandler(db, store, performChangePassword))
	s.HandleFunc("/delete-user", createHandler(db, store, performDeleteUser))
	s.HandleFunc("/add-deck", createHandler(db, store, performAddDeck))
	s.HandleFunc("/modify-deck", createHandler(db, store, performModifyDeck))
	s.HandleFunc("/delete-deck", createHandler(db, store, performDeleteDeck))
	s.HandleFunc("/update-decklist", createHandler(db, store, performUpdateDecklist))
	s.HandleFunc("/save-snapshot", createHandler(db, store, performSaveSnapshot))
	s.HandleFunc("/revert-changes", createHandler(db, store, performRevertChanges))
	s.HandleFunc("/clear-history", createHandler(db, store, performClearHistory))
}

func masterKey() []byte {
	return []byte(os.Getenv("DTKEY"))
}

func getCookie(r *http.Request, store *sessions.CookieStore, name string) interface{} {
	session, err := store.Get(r, sessionName)
	if err != nil {
		return struct{}{}
	}

	val, ok := session.Values[name]
	if ok {
		return val
	} else {
		return struct{}{}
	}
}

func setCookie(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore, name string, val interface{}) error {
	session, err := store.Get(r, sessionName)
	if err != nil && err.Error() != securecookie.ErrMacInvalid.Error() {
		// ignore un-decodable saved session (the returned session will still be valid)
		return err
	}

	session.Values[name] = val
	return session.Save(r, w)
}

func deleteCookie(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore, name string) error {
	session, err := store.Get(r, sessionName)
	if err != nil && err.Error() != securecookie.ErrMacInvalid.Error() {
		// ignore un-decodable saved session (the returned session will still be valid)
		return err
	}

	delete(session.Values, name)
	return session.Save(r, w)
}

func findLoggedInUser(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) (*User, error) {
	if userName, ok := getCookie(r, store, "user").(string); ok {
		if sessionKey, ok := getCookie(r, store, "session-key").([]byte); ok {
			if user, err := db.FindUser(userName); err == nil {
				if bytes.Equal(user.SessionKey, sessionKey) {
					// the user is found and the session key matches - this user is logged in
					return user, nil
				}
			}
		}
	}

	return nil, UserNotFoundError
}

func updateSessionKey(user *User, w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	key := securecookie.GenerateRandomKey(32)

	user.SessionKey = key
	if err := db.UpdateUser(user); err != nil {
		return err
	}

	return setCookie(w, r, store, "session-key", key)
}

func renderTemplate(name string, w io.Writer, data interface{}) error {
	localTemplateRoot := os.ExpandEnv("$GOPATH/src/github.com/garbotron/donkeytownsfolk/templates")
	templatePath := path.Join(localTemplateRoot, name)
	headerPath := path.Join(localTemplateRoot, "header.template")
	footerPath := path.Join(localTemplateRoot, "footer.template")
	if t, err := template.ParseFiles(templatePath, headerPath, footerPath); err != nil {
		return err
	} else {
		t.Execute(w, data)
		return nil
	}
}

func redirectForError(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore, err error, page string) {
	// for our error page, we're just going to use the main filter page with an error info text blob
	setCookie(w, r, store, "error", err.Error())
	http.Redirect(w, r, page, http.StatusFound)
}

func createHandler(
	db *Db,
	store *sessions.CookieStore,
	f func(http.ResponseWriter, *http.Request, *Db, *sessions.CookieStore) error) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := f(w, r, db, store); err != nil {
			redirectForError(w, r, store, err, "/")
		}
	}
}

func getFilterResults(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) *filterResult {
	result := &filterResult{}

	// first build the entire sorted list of all decks (sorted by deck name)
	result.AllDecks = []*deckData{}

	searchTerms := strings.Split(r.FormValue("search"), " ")

	allUsers := []*User{}
	if u := r.FormValue("user"); u != "" {
		if user, err := db.FindUser(u); err == nil {
			allUsers = []*User{user}
		}
	} else {
		if u, err := db.AllUsers(); err == nil {
			allUsers = u
		}
	}

	priceLimit := NoMoney
	if p := r.FormValue("price"); p != "" {
		if pp, err := strconv.Atoi(p); err == nil {
			priceLimit = Money(pp)
		}
	}

	for _, u := range allUsers {
		for _, d := range u.AllDecks() {
			// exclude the deck if it doesn't match the price requirement
			if priceLimit != NoMoney && d.PriceLimit != priceLimit {
				continue
			}

			// exclude the deck if it doesn't match at least one of the search terms
			haystack := normalizeString(fmt.Sprintf("%s-%s-%s", u.Name, d.Name, d.PriceLimit.String()))
			matchesSearch := false
			for _, term := range searchTerms {
				if strings.Index(haystack, normalizeString(term)) >= 0 {
					matchesSearch = true
					break
				}
			}
			if !matchesSearch {
				continue
			}

			newDeck := &deckData{d, u}

			// sorted insert
			insertIdx := 0
			for insertIdx < len(result.AllDecks) && d.Name > result.AllDecks[insertIdx].Deck.Name {
				insertIdx++
			}
			result.AllDecks = append(
				result.AllDecks[:insertIdx],
				append([]*deckData{newDeck}, result.AllDecks[insertIdx:]...)...)
		}
	}

	result.NumPages = (len(result.AllDecks) + (filterResultsPerPage - 1)) / filterResultsPerPage

	result.CurrentPage = 0
	if val, err := strconv.Atoi(r.FormValue("page")); err == nil {
		result.CurrentPage = val
	}

	startIdx := result.CurrentPage * filterResultsPerPage
	if startIdx < 0 || startIdx >= len(result.AllDecks) {
		result.CurrentDecks = []*deckData{}
	} else {
		endIdx := startIdx + filterResultsPerPage
		if endIdx >= len(result.AllDecks) {
			endIdx = len(result.AllDecks)
		}
		result.CurrentDecks = result.AllDecks[startIdx:endIdx]
	}
	return result
}

func getStandardTemplateData(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) *templateData {
	searchText := r.FormValue("search")
	currentUser, _ := findLoggedInUser(w, r, db, store)

	data := &templateData{
		User:       currentUser,
		SearchText: searchText,
	}

	if msg, ok := getCookie(r, store, "message").(string); ok {
		data.InfoMessage = msg
		deleteCookie(w, r, store, "message")
	}

	if msg, ok := getCookie(r, store, "error").(string); ok {
		data.ErrorMessage = msg
		deleteCookie(w, r, store, "error")
	}

	return data
}

func renderFilterPage(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	data := struct {
		templateData
		Decks            []*deckData
		QueryWithoutPage template.URL
		CurrentDeckRange string
		PrevPageIndex    string
		NextPageIndex    string
	}{
		templateData: *getStandardTemplateData(w, r, db, store),
	}

	result := getFilterResults(w, r, db, store)
	data.Decks = result.CurrentDecks

	if result.CurrentPage >= result.NumPages {
		// no results - just stub in some zeroes
		data.CurrentDeckRange = "[no results]"
	} else {
		startIdx := result.CurrentPage * filterResultsPerPage
		data.CurrentDeckRange = fmt.Sprintf("[%d-%d out of %d]", startIdx+1, startIdx+len(result.CurrentDecks), len(result.AllDecks))
		if result.CurrentPage >= 1 {
			data.PrevPageIndex = fmt.Sprintf("%d", result.CurrentPage-1)
		}
		if result.CurrentPage < result.NumPages-1 {
			data.NextPageIndex = fmt.Sprintf("%d", result.CurrentPage+1)
		}
	}

	data.QueryWithoutPage = ""
	for k, v := range r.Form {
		if !strings.EqualFold(k, "page") && len(v) == 1 && v[0] != "" {
			if data.QueryWithoutPage != "" {
				data.QueryWithoutPage += "&"
			}
			data.QueryWithoutPage += template.URL(fmt.Sprintf("%s=%s", k, v[0]))
		}
	}

	renderTemplate("filter.template", w, &data) // ignore errors since this page is used to display all errors
	return nil
}

func renderDeckPage(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	data := struct {
		templateData
		Deck           *deckData
		IsLoggedInUser bool
	}{
		templateData: *getStandardTemplateData(w, r, db, store),
	}

	username := r.FormValue("user")
	deckName := r.FormValue("name")

	if username == "" || deckName == "" {
		return errors.New("Username/deck name not included")
	}

	u, err := db.FindUser(username)
	if err != nil {
		return err
	}

	d := u.FindDeck(deckName)
	if d == nil {
		return errors.New("Deck '" + deckName + "'' not found")
	}

	data.Deck = &deckData{d, u}
	data.Subtitle = data.Deck.Deck.Name
	data.IsLoggedInUser = (data.User != nil && u.Name == data.User.Name)

	return renderTemplate("deck.template", w, &data)
}

func renderSnapshotPage(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	data := struct {
		templateData
		Deck     *deckData
		Snapshot *Snapshot
		IsLegal  bool
	}{
		templateData: *getStandardTemplateData(w, r, db, store),
	}

	username := r.FormValue("user")
	deckName := r.FormValue("deck")
	snapIdx := r.FormValue("idx")

	if deckName == "" || snapIdx == "" {
		return errors.New("Deck name / snapshot index not included")
	}

	u, err := db.FindUser(username)
	if err != nil {
		return err
	}

	d := u.FindDeck(deckName)
	if d == nil {
		return errors.New("Deck '" + deckName + "' doesn't exist!")
	}

	snaps := d.SnapshotsReversed()

	snapIdxInt, err := strconv.Atoi(snapIdx)
	if err != nil || snapIdxInt >= len(snaps) {
		return errors.New("Invalid snapshot index")
	}

	data.Deck = &deckData{d, u}
	data.Snapshot = snaps[snapIdxInt]
	data.IsLegal = d.IsSnapshotLegal(data.Snapshot)
	return renderTemplate("snapshot.template", w, &data)
}

func performLogin(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		return errors.New("Username/password not included")
	}

	user, err := db.FindUser(username)
	if err != nil {
		return err
	}

	err = bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(password))
	if err != nil {
		return errors.New("Invalid username/password")
	}

	if err := setCookie(w, r, store, "user", user.Name); err != nil {
		return err
	}

	if err := updateSessionKey(user, w, r, db, store); err != nil {
		return err
	}

	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

func performLogout(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	user.SessionKey = []byte{}
	if err := db.UpdateUser(user); err != nil {
		return err
	}

	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

func performAddUser(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		return errors.New("Username/password not included")
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	user, err := db.AddUser(username, passwordHash)
	if err != nil {
		return err
	}

	if err := setCookie(w, r, store, "user", user.Name); err != nil {
		return err
	}

	if err := updateSessionKey(user, w, r, db, store); err != nil {
		return err
	}

	setCookie(w, r, store, "message", "User '"+user.Name+"' added successfully!")
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

func performChangePassword(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	password := r.FormValue("password")
	if password == "" {
		return errors.New("Password not included")
	}

	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	user.PasswordHash = passwordHash
	if err := db.UpdateUser(user); err != nil {
		return err
	}

	setCookie(w, r, store, "message", "User '"+user.Name+"' password changed successfully!")
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

func performDeleteUser(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	if err := db.DeleteUser(user); err != nil {
		return err
	}

	err = deleteCookie(w, r, store, "user")
	if err != nil {
		return err
	}

	setCookie(w, r, store, "message", "User '"+user.Name+"' deleted successfully!")
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

func performAddDeck(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	name := r.FormValue("name")
	price := r.FormValue("price")

	if name == "" || price == "" {
		return errors.New("Username/password not included")
	}

	priceInt, err := strconv.Atoi(price)
	if err != nil {
		return errors.New("Price incorrectly formatted")
	}

	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	deck := user.FindDeck(name)
	if deck != nil {
		return errors.New("Deck '" + name + "' already exists!")
	}

	deck = &Deck{name, time.Now(), Money(priceInt), Snapshot{}, []*Snapshot{}}
	user.Decks = append(user.Decks, deck)
	err = db.UpdateUser(user)
	if err != nil {
		return err
	}

	http.Redirect(w, r, "/deck?user="+user.NormalizedName()+"&name="+deck.NormalizedName(), http.StatusFound)
	return nil
}

func performModifyDeck(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	origName := r.FormValue("orig-name")
	newName := r.FormValue("name")
	price := r.FormValue("price")

	if origName == "" || newName == "" || price == "" {
		return errors.New("Required fields not included")
	}

	priceInt, err := strconv.Atoi(price)
	if err != nil {
		return errors.New("Price incorrectly formatted")
	}

	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	deck := user.FindDeck(origName)
	if deck == nil {
		return errors.New("Deck '" + origName + "' doesn't exist!")
	}

	if d := user.FindDeck(newName); d != nil && d.Name != deck.Name {
		return errors.New("Deck '" + newName + "' already exists!")
	}

	deck.Name = newName
	deck.PriceLimit = Money(priceInt)
	err = db.UpdateUser(user)
	if err != nil {
		return err
	}

	http.Redirect(w, r, "/deck?user="+user.NormalizedName()+"&name="+deck.NormalizedName(), http.StatusFound)
	return nil
}

func performDeleteDeck(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	deckName := r.FormValue("deck")

	if deckName == "" {
		return errors.New("Deck name not included")
	}

	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	deck := user.FindDeck(deckName)
	if deck == nil {
		return errors.New("Deck '" + deckName + "' doesn't exist!")
	}

	newDecks := []*Deck{}
	for _, d := range user.Decks {
		if d != deck {
			newDecks = append(newDecks, d)
		}
	}

	user.Decks = newDecks
	err = db.UpdateUser(user)
	if err != nil {
		return err
	}

	setCookie(w, r, store, "message", "Deck '"+deck.Name+"' deleted successfully!")
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

func performUpdateDecklist(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	deckName := r.FormValue("deck")
	commander := r.FormValue("commander")
	decklist := r.FormValue("decklist")
	sideboard := r.FormValue("sideboard")
	grandfather := r.FormValue("grandfather")

	if deckName == "" {
		return errors.New("Deck name not included")
	}

	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	deck := user.FindDeck(deckName)
	if deck == nil {
		return errors.New("Deck '" + deckName + "' doesn't exist!")
	}

	if commander == "" {
		deck.StagingArea.Commander.IsPresent = false
	} else {
		deck.StagingArea.Commander.IsPresent = true
		deck.StagingArea.Commander.Name = strings.TrimSpace(commander)
		deck.StagingArea.Commander.Price = Free // not scanned yet
	}

	deck.StagingArea.Decklist = ParseCardEntryLines(decklist)
	deck.StagingArea.Sideboard = ParseCardEntryLines(sideboard)
	deck.StagingArea.IsGrandfatherLegal = (grandfather != "")

	deckUrl := "/deck?user=" + user.NormalizedName() + "&name=" + deck.NormalizedName()
	err = deck.StagingArea.CalculatePrices(db)
	if err != nil {
		// this is not a fatal error - we need to redirect back to the expected page
		redirectForError(w, r, store, err, deckUrl)
		return nil
	}

	err = db.UpdateUser(user)
	if err != nil {
		return err
	}

	http.Redirect(w, r, deckUrl, http.StatusFound)
	return nil
}

func performSaveSnapshot(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	deckName := r.FormValue("deck")

	if deckName == "" {
		return errors.New("Deck name not included")
	}

	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	deck := user.FindDeck(deckName)
	if deck == nil {
		return errors.New("Deck '" + deckName + "' doesn't exist!")
	}

	snap := deck.StagingArea.Clone()
	snap.Date = time.Now()
	deck.Snapshots = append(deck.Snapshots, snap)

	err = db.UpdateUser(user)
	if err != nil {
		return err
	}

	http.Redirect(w, r, "/deck?user="+user.NormalizedName()+"&name="+deck.NormalizedName(), http.StatusFound)
	return nil
}

func performRevertChanges(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	deckName := r.FormValue("deck")

	if deckName == "" {
		return errors.New("Deck name not included")
	}

	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	deck := user.FindDeck(deckName)
	if deck == nil {
		return errors.New("Deck '" + deckName + "' doesn't exist!")
	}

	if len(deck.Snapshots) == 0 {
		return errors.New("Deck has no snapshots!")
	}

	deck.StagingArea = *deck.Snapshots[len(deck.Snapshots)-1].Clone()

	err = db.UpdateUser(user)
	if err != nil {
		return err
	}

	http.Redirect(w, r, "/deck?user="+user.NormalizedName()+"&name="+deck.NormalizedName(), http.StatusFound)
	return nil
}

func performClearHistory(w http.ResponseWriter, r *http.Request, db *Db, store *sessions.CookieStore) error {
	deckName := r.FormValue("deck")

	if deckName == "" {
		return errors.New("Deck name not included")
	}

	user, err := findLoggedInUser(w, r, db, store)
	if err != nil {
		return err
	}

	deck := user.FindDeck(deckName)
	if deck == nil {
		return errors.New("Deck '" + deckName + "' doesn't exist!")
	}

	deck.Snapshots = []*Snapshot{}
	err = db.UpdateUser(user)
	if err != nil {
		return err
	}

	http.Redirect(w, r, "/deck?user="+user.NormalizedName()+"&name="+deck.NormalizedName(), http.StatusFound)
	return nil
}
