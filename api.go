package main

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/fiam/gounidecode/unidecode"
	"github.com/fzzy/radix/redis"
	"github.com/mholt/binding"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func encodeString(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	return base64.URLEncoding.EncodeToString(h.Sum(nil))[0:6]
}

// With this we can remove all apostrophes that are used like
// quotation marks. E.g. "The 'one' word" or "'One' is a word".
// However, we'll want to keep apostrophes when they're in a word
// like "they're" or "there's" or "o'clock" as if it's 1 word.
var quotationMarksRegex = regexp.MustCompile(`\B'|'\B`)

var junkRegex = regexp.MustCompile(`[\[\](){}"\.?!,:;,-]`)

func cleanWords(query string) ([]string, bool) {
	query = quotationMarksRegex.ReplaceAllString(query, "")
	query = junkRegex.ReplaceAllString(query, " ")
	split := strings.Fields(strings.Trim(query, " "))
	terms := make([]string, len(split))

	var asciiTerm string
	unidecodeExpanded := false
	for i, term := range split {
		terms[i] = strings.ToLower(strings.Trim(strings.Trim(term, " "), "."))
		asciiTerm = unidecode.Unidecode(terms[i])
		if asciiTerm != terms[i] {
			terms = append(terms, asciiTerm)
			unidecodeExpanded = true
		}
	}
	return terms, unidecodeExpanded
}

func getPrefixes(title string) []string {
	var prefixes []string
	words, _ := cleanWords(title)
	for _, word := range words {
		for i := range word {
			if i > 0 {
				prefixes = append(prefixes, word[0:i])
			}
		}
		prefixes = append(prefixes, word) // should we keep this?!
		prefixes = append(prefixes, word+"$")
	}
	return prefixes
}

func pingHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	fmt.Fprintf(w, "pong\n")
}

type updateForm struct {
	URL        string
	Title      string
	ItemType   string
	Group      string
	Popularity float64
}

func (f *updateForm) FieldMap() binding.FieldMap {
	return binding.FieldMap{
		&f.URL: binding.Field{
			Form:     "url",
			Required: true,
		},
		&f.Title: binding.Field{
			Form:     "title",
			Required: true,
		},
		&f.ItemType: binding.Field{
			Form:     "item_type",
			Required: true,
		},
		&f.Group:      "group",
		&f.Popularity: "popularity",
	}
}

func (f updateForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	if strings.Trim(f.Title, " ") == "" {
		errs = append(errs, binding.Error{
			FieldNames:     []string{"title"},
			Classification: "ComplaintError",
			Message:        "Can't be empty",
		})
	}
	if strings.Trim(f.ItemType, " ") == "" {
		errs = append(errs, binding.Error{
			FieldNames:     []string{"item_type"},
			Classification: "ComplaintError",
			Message:        "Can't be empty",
		})
	}
	if strings.Trim(f.URL, " ") == "" {
		errs = append(errs, binding.Error{
			FieldNames:     []string{"url"},
			Classification: "ComplaintError",
			Message:        "Can't be empty",
		})
	}
	return errs
}

func updateHandler(w http.ResponseWriter, req *http.Request) {
	key := req.Header.Get("AUTH-KEY")
	if key == "" {
		output := map[string]string{"error": "Auth-Key header not set"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}
	form := new(updateForm)
	errs := binding.Bind(req, form)
	if errs.Handle(w) {
		return
	}
	form.Title = strings.Trim(form.Title, " ")
	form.ItemType = strings.Trim(form.ItemType, " ")
	form.URL = strings.Trim(form.URL, " ")
	// group := form.Group

	c, err := redisPool.Get()
	errorHandler(err)
	defer redisPool.Put(c)

	domain, err := GetDomain(key, c)
	if domain == "" {
		output := map[string]string{"error": "Auth-Key not recognized"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}

	insertDocument(
		domain,
		form.Title,
		form.ItemType,
		form.URL,
		form.Group,
		form.Popularity,
		c,
	)

	output := map[string]string{"message": "OK"}
	renderer.JSON(w, http.StatusCreated, output)
}

func insertDocument(domain, title, itemType, url, group string, popularity float64, c *redis.Client) {
	encoded := encodeString(domain)
	encodedURL := encodeString(url)

	existingTitle, _ := c.Cmd("HGET", encoded+"$titles", encodedURL).Str()
	if existingTitle == "" {
		err := c.Cmd("HINCRBY", "$domaindocuments", domain, 1).Err
		errorHandler(err)
	}

	pipedCommands := 0
	for _, prefix := range getPrefixes(title) {
		if group != "" {
			encodedGroup := encodeString(group)
			c.Append("ZADD", encoded+encodedGroup+prefix, popularity, encodedURL)
			c.Append("HSET", encoded+"$groups", encodedURL, encodedGroup)
			pipedCommands++
		} else {
			c.Append("ZADD", encoded+prefix, popularity, encodedURL)
		}
		pipedCommands++
	}
	c.Append("HSET", encoded+"$titles", encodedURL, title)
	c.Append("HSET", encoded+"$itemTypes", encodedURL, itemType)
	pipedCommands++
	c.Append("HSET", encoded+"$urls", encodedURL, url)
	pipedCommands++
	for i := 1; i <= pipedCommands; i++ {
		if err := c.GetReply().Err; err != nil {
			errorHandler(err)
		}
	}

}

type bulkDocuments struct {
	Documents []bulkDocument `json:"documents"`
}

type bulkDocument struct {
	URL        string  `json:"url"`
	Title      string  `json:"title"`
	ItemType   string  `json:"item_type"`
	Popularity float64 `json:"popularity"`
	Group      string  `json:"group"`
}

func bulkHandler(w http.ResponseWriter, req *http.Request) {
	key := req.Header.Get("AUTH-KEY")
	if key == "" {
		output := map[string]string{"error": "Auth-Key header not set"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}
	c, err := redisPool.Get()
	errorHandler(err)
	defer redisPool.Put(c)

	domain, err := GetDomain(key, c)
	if domain == "" {
		output := map[string]string{"error": "Auth-Key not recognized"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}

	// encoded := encodeString(domain)

	decoder := json.NewDecoder(req.Body)
	var bs bulkDocuments
	err = decoder.Decode(&bs)
	errorHandler(err)
	for _, b := range bs.Documents {
		insertDocument(
			domain,
			b.Title,
			b.ItemType,
			b.URL,
			b.Group,
			b.Popularity,
			c,
		)
	}
	output := map[string]string{"message": "OK"}
	renderer.JSON(w, http.StatusCreated, output)
}

type deleteForm struct {
	// Domain string
	URL string
}

// FieldMap defines the bindings for deleteForm
func (f *deleteForm) FieldMap() binding.FieldMap {
	return binding.FieldMap{
		// &f.Domain: binding.Field{
		// 	Form:     "domain",
		// 	Required: true,
		// },
		&f.URL: binding.Field{
			Form:     "url",
			Required: true,
		},
	}
}

// Validate checks the values for deleteForm
func (f deleteForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	// if strings.Trim(f.Domain, " ") == "" {
	// 	errs = append(errs, binding.Error{
	// 		FieldNames:     []string{"domain"},
	// 		Classification: "ComplaintError",
	// 		Message:        "Can't be empty",
	// 	})
	// }
	if strings.Trim(f.URL, " ") == "" {
		errs = append(errs, binding.Error{
			FieldNames:     []string{"url"},
			Classification: "ComplaintError",
			Message:        "Can't be empty",
		})
	}
	return errs
}

func deleteHandler(w http.ResponseWriter, req *http.Request) {
	key := req.Header.Get("AUTH-KEY")
	if key == "" {
		output := map[string]string{"error": "Auth-Key header not set"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}
	form := new(deleteForm)
	errs := binding.Bind(req, form)
	if errs.Handle(w) {
		return
	}
	form.URL = strings.Trim(form.URL, " ")

	c, err := redisPool.Get()
	errorHandler(err)
	defer redisPool.CarefullyPut(c, &err)

	domain, err := GetDomain(key, c)
	if domain == "" {
		output := map[string]string{"error": "Auth-Key not recognized"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}

	encoded := encodeString(domain)
	encodedURL := encodeString(form.URL)
	var title string
	reply := c.Cmd("HGET", encoded+"$titles", encodedURL)
	if reply.Type == redis.NilReply {
		output := map[string]string{"error": "URL not recognized"}
		renderer.JSON(w, http.StatusNotFound, output)
		return
	}
	title, err = reply.Str()

	errorHandler(err)
	if title != "" {
		err = c.Cmd("HINCRBY", "$domaindocuments", domain, -1).Err
		errorHandler(err)
	}

	reply = c.Cmd("HGET", encoded+"$groups", encodedURL)
	encodedGroup := ""
	if reply.Type != redis.NilReply {
		encodedGroup, _ = reply.Str()
	}
	prefixes := getPrefixes(title)
	pipedCommands := 0
	for _, prefix := range prefixes {
		if encodedGroup != "" {
			c.Append("ZREM", encoded+encodedGroup+prefix, encodedURL)
			c.Append("HDEL", encoded+"$groups", encodedURL)
			pipedCommands++
		} else {
			c.Append("ZREM", encoded+prefix, encodedURL)
		}

		pipedCommands++
	}

	c.Append("HDEL", encoded+"$titles", encodedURL)
	pipedCommands++
	c.Append("HDEL", encoded+"$urls", encodedURL)
	pipedCommands++

	for i := 1; i <= pipedCommands; i++ {
		if err := c.GetReply().Err; err != nil {
			errorHandler(err)
		}
	}
	output := map[string]string{"message": "OK"}
	renderer.JSON(w, http.StatusNoContent, output)
}

// Reply isn't a great name :)
type Reply struct {
	URL   string
	Score string
}

type fetchForm struct {
	Number int
	Groups string
	Query  string
	Domain string
}

// FieldMap defines the bindings for fetchForm
func (f *fetchForm) FieldMap() binding.FieldMap {
	return binding.FieldMap{
		&f.Number: "n",
		&f.Groups: "g",
		&f.Query: binding.Field{
			Form:     "q",
			Required: true,
		},
		&f.Domain: binding.Field{
			Form:     "d",
			Required: true,
		},
	}
}

func fetchHandler(w http.ResponseWriter, req *http.Request) {
	// time.Sleep(1 * time.Second)
	// time.Sleep(60  *   time.Millisecond)
	form := new(fetchForm)
	errs := binding.Bind(req, form)
	if errs.Handle(w) {
		return
	}
	n := form.Number
	if n <= 0 {
		n = 10 // default
	}

	form.Domain = strings.Trim(form.Domain, " ")
	groups := []string{}
	if len(form.Groups) != 0 {
		groups = append(groups, strings.Split(form.Groups, ",")...)
	}
	sort.Strings(groups)

	encoded := encodeString(form.Domain)

	form.Query = strings.Trim(form.Query, " ")
	terms, unidecodeExpanded := cleanWords(form.Query)
	searchedTerms := make([]string, len(terms))
	copy(searchedTerms, terms)

	// this is only temporary logging
	ts := time.Now().Unix()
	log.Printf("%v$%v$%v", form.Domain, form.Query, ts)

	// If the queryhas more than one term, e.g "one spo" then we have
	// completed the first word and don't expect autocompletion on that.
	// For example, it should now find "one spotless thing" but not
	// "oneanother sport".
	// And because every word has been indexed in its complete form
	// we should have a key called "one$" and a key called "spo" that
	// we can search on.
	if len(terms) > 1 && !unidecodeExpanded {
		for i, term := range terms {
			if i+1 < len(terms) {
				terms[i] = term + "$"
			}
		}
	}

	c, err := redisPool.Get()
	errorHandler(err)
	defer redisPool.CarefullyPut(c, &err)

	now := time.Now()
	thisMonthFetchesKey := fmt.Sprintf("$domainfetches$%v$%v", now.Year(), int(now.Month()))
	err = c.Cmd("HINCRBY", thisMonthFetchesKey, form.Domain, 1).Err
	errorHandler(err)

	getReplies := func(terms []string, group string) ([]string, error) {
		encodedTerms := make([]string, len(terms))
		encodedGroup := ""
		if group != "" {
			encodedGroup = encodeString(group)
		}
		for i, term := range terms {
			encodedTerms[i] = encoded + encodedGroup + term
		}
		var replies []string
		if len(terms) > 1 {
			c.Append("ZINTERSTORE", "$tmp", len(terms), encodedTerms, "AGGREGATE", "max")
			c.Append("ZREVRANGE", "$tmp", 0, n-1, "WITHSCORES")
			c.GetReply() // the ZINTERSTORE
			replies, err = c.GetReply().List()
		} else {
			replies, err = c.Cmd("ZREVRANGE", encodedTerms[0], 0, n-1, "WITHSCORES").List()
		}
		return replies, err
	}

	var replies []string
	if len(terms) > 0 {
		replies, err = getReplies(terms, "")
		errorHandler(err)
	}
	var replyStructs []Reply
	for i, element := range replies {
		if i%2 == 0 {
			replyStructs = append(replyStructs, Reply{element, replies[i+1]})
		}
	}
	for _, group := range groups {
		replies, err = getReplies(terms, group)
		errorHandler(err)
		for i, element := range replies {
			if i%2 == 0 {
				replyStructs = append(replyStructs, Reply{element, replies[i+1]})
			}
		}
	}
	RemoveDuplicates := func(xs *[]Reply) {
		found := make(map[string]bool)
		j := 0
		for i, x := range *xs {
			if !found[x.URL] {
				found[x.URL] = true
				(*xs)[j] = (*xs)[i]
				j++
			}
		}
		*xs = (*xs)[:j]
	}
	RemoveDuplicates(&replyStructs)
	if len(replyStructs) > n {
		replyStructs = replyStructs[:n]
	}

	// We might want to sort this here by the extra business logic
	// on sorting.
	encodedUrls := make([]string, len(replyStructs))
	for i, each := range replyStructs {
		encodedUrls[i] = each.URL
	}

	var titles []string
	var urls []string
	var itemTypes []string

	if len(encodedUrls) == 0 {
	} else {
		titles, err = c.Cmd("HMGET", encoded+"$titles", encodedUrls).List()
		errorHandler(err)
		itemTypes, err = c.Cmd("HMGET", encoded+"$itemTypes", encodedUrls).List()
		errorHandler(err)
		urls, err = c.Cmd("HMGET", encoded+"$urls", encodedUrls).List()
		errorHandler(err)
	}
	rows := make([]interface{}, len(titles))
	for i, title := range titles {
		row := make([]string, 3)
		row[0] = urls[i]
		row[1] = title
		row[2] = itemTypes[i]
		rows[i] = row
	}
	rows = rows[:len(titles)]

	output := make(map[string]interface{})
	output["terms"] = searchedTerms
	output["results"] = rows
	w.Header().Set("Access-Control-Allow-Origin", "*")
	renderer.JSON(w, http.StatusOK, output)
}

func privateStatsHandler(w http.ResponseWriter, req *http.Request) {
	key := req.Header.Get("AUTH-KEY")
	if key == "" {
		output := map[string]string{"error": "Auth-Key header not set"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}

	c, err := redisPool.Get()
	errorHandler(err)
	defer redisPool.Put(c)

	domain, err := GetDomain(key, c)
	if err != nil {
		output := map[string]string{"error": "Auth-Key not recognized"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}

	documents := 0
	documentsStr, err := c.Cmd("HGET", "$domaindocuments", domain).Str()
	if documentsStr != "" {
		documents, err = strconv.Atoi(documentsStr)
		errorHandler(err)
	}

	now := time.Now()
	var dt time.Time
	allFetches := make(map[string]interface{})
	var fetchKey string
	var fetchesStr string
	var fetches int
	// starting on the year 2015 because that's when it all started
	for y := 2015; y <= now.Year(); y++ {
		thisYearFetches := make(map[string]int)
		for m := 1; m <= 12; m++ {
			dt = time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
			if dt.Before(now) {
				fetchKey = fmt.Sprintf("$domainfetches$%v$%v", dt.Year(), int(dt.Month()))
				fetchesStr, err = c.Cmd("HGET", fetchKey, domain).Str()
				if err == nil {
					fetches, err = strconv.Atoi(fetchesStr)
					errorHandler(err)
					thisYearFetches[fmt.Sprintf("%v", m)] = fetches
				}
			}
		}
		allFetches[fmt.Sprintf("%v", y)] = thisYearFetches
	}
	output := make(map[string]interface{})
	output["fetches"] = allFetches
	output["documents"] = documents
	w.Header().Set("Access-Control-Allow-Origin", "*")
	renderer.JSON(w, http.StatusOK, output)

}

func flushHandler(w http.ResponseWriter, req *http.Request) {
	key := req.Header.Get("AUTH-KEY")
	if key == "" {
		output := map[string]string{"error": "Auth-Key header not set"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}

	c, err := redisPool.Get()
	errorHandler(err)
	defer redisPool.Put(c)

	domain, err := GetDomain(key, c)
	if err != nil {
		output := map[string]string{"error": "Auth-Key not recognized"}
		renderer.JSON(w, http.StatusForbidden, output)
		return
	}

	encoded := encodeString(domain)

	all, err := c.Cmd("HGETALL", encoded+"$titles").List()
	errorHandler(err)
	pipedCommands := 0
	var encodedURL string
	for i, each := range all {
		if i%2 == 0 {
			encodedURL = each
		} else {
			encodedGroup := ""
			reply := c.Cmd("HGET", encoded+"$groups", encodedURL)
			if reply.Type != redis.NilReply {
				encodedGroup, err = reply.Str()
				errorHandler(err)
			}
			prefixes := getPrefixes(each)
			for _, prefix := range prefixes {
				if encodedGroup != "" {
					c.Append("ZREM", encoded+encodedGroup+prefix, encodedURL)
				} else {
					c.Append("ZREM", encoded+prefix, encodedURL)
				}
				pipedCommands++
			}
			c.Append("HDEL", encoded+"$titles", encodedURL)
			pipedCommands++
			c.Append("HDEL", encoded+"$urls", encodedURL)
			pipedCommands++
			if encodedGroup != "" {
				c.Append("HDEL", encoded+"$groups", encodedURL)
				pipedCommands++
			}
		}
	}

	for i := 1; i <= pipedCommands; i++ {
		if err := c.GetReply().Err; err != nil {
			errorHandler(err)
		}
	}

	err = c.Cmd("HSET", "$domaindocuments", domain, 0).Err
	errorHandler(err)

	output := map[string]string{"message": "OK"}
	renderer.JSON(w, http.StatusNoContent, output)
}
