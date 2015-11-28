/*
Author: rodr <rodr<at>dpustudios.com>
Binary 2kcookies implements a simple web scraper and dynamodb populator for 2k.com cookeis and csrf state tokens.

The intent of this code was not to create a really fast efficient scraper, it was instead to politely store tens of thousands of operational security data points from a given website.

Author is not responsible for your use of this software, and reminds you to be nice to others.
*/
package main

import (
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"golang.org/x/net/html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	table_name   = "2kcookies"
	timeLongForm = "Mon, 2 Jan 2006 15:04:05 GMT"
)

// This is just a simple container for our cookie jar
type CookieManager struct {
	jar map[string][]*http.Cookie
}

// This is an unused result
type CookieResult struct {
	Name  string
	Value string
	Time  int64
}

// SetCookies - implements a set cookie method to be executed by net/http
func (p *CookieManager) SetCookies(u *url.URL, cookies []*http.Cookie) {
	p.jar[u.Host] = cookies
}

// Cookies - returns the cookies for a given url
func (p *CookieManager) Cookies(u *url.URL) []*http.Cookie {
	return p.jar[u.Host]
}

// GetCookie - given a CookieManager this retrieves teh cookies from 2k.com
func GetCookie(j *CookieManager) (*http.Response, error) {
	client := &http.Client{}
	client.Jar = j
	req, err := http.NewRequest("GET", "http://2k.com", nil)
	if err != nil {
		fmt.Println("We have an error in request")
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("We have an error in request")
		return nil, err
	}

	return resp, nil
}

// InputItem - this is the final object we pass to ProcessCookies
type InputItem struct {
	CookieManager
	Time      string
	TableName string
	HrefData  map[string]string
}

// ProcessCookies - this parses the 2kgames cookies passed extracing neccessary values
// given an InputItem return a dynamodb.PutItem object
func ProcessCookies(inputItem *InputItem) *dynamodb.PutItemInput {
	var params *dynamodb.PutItemInput
	for _, item := range inputItem.CookieManager.jar {
		for _, cookie := range item {
			if cookie.Name != "2K" {
				continue
			}
			state := inputItem.HrefData["state"]
			client_id := inputItem.HrefData["client_id"]
			params = &dynamodb.PutItemInput{
				TableName: &inputItem.TableName,
				Item: map[string]*dynamodb.AttributeValue{
					"timestamp": {
						N: &inputItem.Time,
					},
					"cookie_name": {
						S: &cookie.Name,
					},
					"cookie_value": {
						S: &cookie.Value,
					},
					"state": {
						S: &state,
					},
					"client_id": {
						S: &client_id,
					},
				},
			} // parms end
		}
	}
	return params
}

// scrapePageWorker -- this is the function that does most of the work in parsing the HTML
func scrapePageWorker(page *io.ReadCloser, out chan [2]string, chFinished chan bool) {
	defer func() {
		chFinished <- true
	}()
	z := html.NewTokenizer(*page)
	// infinite loop to toss state tokens into a url map
	for {
		var result [2]string
		tt := z.Next()
		switch {
		case tt == html.ErrorToken:
			return
		case tt == html.StartTagToken:
			t := z.Token()

			isAnchor := t.Data == "a"
			if !isAnchor {
				continue
			}
			if isAnchor {
				for _, attr := range t.Attr {
					if attr.Key == "id" {
						result[0] = attr.Val
					}
					if attr.Key == "data-href" {
						result[1] = attr.Val
						out <- result
					}
				}
			}
		}
	} // end for
}

// ScrapePage - This grabs the values we care about from the 2kgames html itself
func ScrapePage(page *io.ReadCloser) map[string]string {
	out := make(chan [2]string)
	chFinished := make(chan bool)
	go scrapePageWorker(page, out, chFinished)
	urls := make(map[string]string)
	// iterate thru channel results
	// close when worker function closes
	select {
	case u := <-out:
		actual_url, _ := url.Parse(u[1])
		url_vals := actual_url.Query()
		// set the values we care about
		urls["state"] = url_vals["state"][0]
		urls["client_id"] = url_vals["client_id"][0]
	case <-chFinished:
		break
	}
	return urls
}

func main() {
	svc := dynamodb.New(session.New(&aws.Config{Region: aws.String("us-east-1")}))
	jar := &CookieManager{}
	var cookieCount int
	var sleepTime int64
	flag.IntVar(&cookieCount, "count", 10, "collect this many cookies")
	flag.Int64Var(&sleepTime, "sleep", 2, "sleep this many between executions")
	flag.Parse()
	for i := 0; i <= cookieCount; i++ {
		jar.jar = make(map[string][]*http.Cookie)
		if resp, err := GetCookie(jar); err == nil {
			t, _ := time.Parse(timeLongForm, resp.Header["Date"][0])
			time_string := strconv.FormatInt(t.Unix(), 10)
			body := resp.Body
			params := ProcessCookies(&InputItem{*jar, time_string, table_name, ScrapePage(&body)})
			svc.PutItem(params)
		} else {
			fmt.Println("Failed to get a response body.  Will retry after timeout.")
		}
		if i%5 == 0 && i != 0 {
			fmt.Printf("Got %d cookies.\n", i)
		}
		time.Sleep(time.Duration(sleepTime) * time.Second) // lets hold firm 2s for niceness
	}
}
