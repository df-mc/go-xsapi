package achievements

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"golang.org/x/text/language"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip serves an authored response without contacting Xbox Live.
func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// testClient creates an achievements client with a synthetic user and transport.
func testClient(transport roundTripFunc) *Client {
	return New(&http.Client{Transport: transport}, xsts.UserInfo{XUID: "123"})
}

// response returns a complete HTTP response associated with the given request.
func response(request *http.Request, status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Request: request, Body: body}
}

type trackedBody struct {
	io.Reader
	closed bool
}

// Close records whether the client released the response body.
func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestAchievementsRequest(t *testing.T) {
	const token = "a+/=?& snow ☃"
	filter := Filter{TitleID: ^uint32(0), Type: "Challenge", MaxItems: math.MaxInt32, ContinuationToken: token}
	query := url.Values{"titleId": {"4294967295"}, "types": {"Challenge"}, "maxItems": {"2147483647"}, "continuationToken": {token}}
	for _, test := range []struct {
		name     string
		filter   Filter
		query    url.Values
		opts     []internal.RequestOption
		language []string
	}{
		{name: "empty filter", query: url.Values{}, language: []string{"en-US, en"}},
		{name: "full filter", filter: filter, query: query, language: []string{"en-US, en"}},
		{name: "caller language", filter: filter, query: query, opts: []internal.RequestOption{internal.AcceptLanguage([]language.Tag{language.French, language.German})}, language: []string{"fr, de", "en-US, en"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := testClient(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "achievements.xboxlive.com" || request.URL.Path != "/users/xuid(123)/achievements" {
					t.Fatalf("request = %s %s", request.Method, request.URL)
				}
				if query := request.URL.Query(); !reflect.DeepEqual(query, test.query) {
					t.Errorf("query = %v, want %v", query, test.query)
				}
				if request.Header.Get("x-xbl-contract-version") != "2" || request.Header.Get("Accept") != "application/json" || !reflect.DeepEqual(request.Header.Values("Accept-Language"), test.language) {
					t.Errorf("headers = %v", request.Header)
				}
				if request.Body != nil {
					t.Error("GET has a request body")
				}
				return response(request, http.StatusOK, io.NopCloser(strings.NewReader(`{"achievements":[],"pagingInfo":{"continuationToken":"next","totalRecords":3}}`))), nil
			})
			page, err := client.Achievements(context.Background(), test.filter, test.opts...)
			if err != nil || page.Achievements == nil || len(page.Achievements) != 0 || page.PagingInfo.ContinuationToken != "next" || page.PagingInfo.TotalRecords != 3 || calls != 1 {
				t.Fatalf("page = %+v, error = %v, requests = %d", page, err, calls)
			}
		})
	}
}

func TestAchievementsPreservesTypedValues(t *testing.T) {
	const body = `{"achievements":[{
		"id":"one","serviceConfigId":"scid","name":"First","description":"Unlocked",
		"lockedDescription":"Locked","progressState":"FutureState",
		"progression":{"timeUnlocked":"unusual timestamp","requirements":[{"id":"r","current":"not numeric","target":"184467440737095516160"}]},
		"mediaAssets":[{"name":"Icon","type":"Icon","url":"https://example.test/icon.png"}],
		"rewards":[
			{"name":"Score","description":"Points","type":"Gamerscore","value":"184467440737095516160","valueType":"Int"},
			{"type":"InApp","value":"not numeric","mediaAsset":{"name":"Reward","type":"Art","url":"https://example.test/reward.png"}},
			{"type":"Gamerscore"}
		]
	},{"id":"two"}],"pagingInfo":{"continuationToken":null}}`
	client := testClient(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusOK, io.NopCloser(strings.NewReader(body))), nil
	})
	page, err := client.Achievements(context.Background(), Filter{TitleID: 42})
	if err != nil {
		t.Fatal(err)
	}
	want := []Achievement{
		{ID: "one", ServiceConfigID: "scid", Name: "First", Description: "Unlocked", LockedDescription: "Locked", ProgressState: "FutureState",
			Progression: Progression{TimeUnlocked: "unusual timestamp", Requirements: []Requirement{{ID: "r", Current: "not numeric", Target: "184467440737095516160"}}},
			MediaAssets: []MediaAsset{{Name: "Icon", Type: "Icon", URL: "https://example.test/icon.png"}},
			Rewards: []Reward{
				{Name: "Score", Description: "Points", Type: "Gamerscore", Value: "184467440737095516160", ValueType: "Int"},
				{Type: "InApp", Value: "not numeric", MediaAsset: &MediaAsset{Name: "Reward", Type: "Art", URL: "https://example.test/reward.png"}},
				{Type: "Gamerscore"},
			}},
		{ID: "two"},
	}
	if !reflect.DeepEqual(page.Achievements, want) || page.PagingInfo.ContinuationToken != "" {
		t.Fatalf("page = %+v, want achievements %+v", page, want)
	}
}

func TestAllPagination(t *testing.T) {
	for _, token := range []string{"", "start +/&=?"} {
		t.Run("start="+token, func(t *testing.T) {
			testAllPagination(t, token)
		})
	}
}

// testAllPagination checks that each page keeps the filter and deduplicates by service and ID.
// An ID matching a continuation token must not be mistaken for a repeated page.
func testAllPagination(t *testing.T, start string) {
	t.Helper()
	const token = "next +/&=?"
	bodies := []string{
		`{"achievements":[{"id":"one","serviceConfigId":"a","name":"First"},{"id":"next +/&=?"},{"name":"No ID"}],"pagingInfo":{"continuationToken":"next +/&=?"}}`,
		`{"achievements":[{"id":"one","serviceConfigId":"a","name":"Duplicate"},{"id":"one","serviceConfigId":"b"},{"id":"two"}],"pagingInfo":{"continuationToken":null}}`,
	}
	calls := 0
	client := testClient(func(request *http.Request) (*http.Response, error) {
		if calls >= len(bodies) {
			t.Fatal("unexpected extra page")
		}
		query := request.URL.Query()
		if query.Get("titleId") != "42" || query.Get("types") != "Persistent" || query.Get("maxItems") != "100" ||
			(calls == 0 && (query.Get("continuationToken") != start || query.Has("continuationToken") != (start != ""))) ||
			(calls == 1 && query.Get("continuationToken") != token) {
			t.Errorf("page %d query = %v", calls, query)
		}
		if got := request.Header.Values("Accept-Language"); !reflect.DeepEqual(got, []string{"fr", "en-US, en"}) {
			t.Errorf("page %d language = %v", calls, got)
		}
		body := bodies[calls]
		calls++
		return response(request, http.StatusOK, io.NopCloser(strings.NewReader(body))), nil
	})
	filter := Filter{TitleID: 42, Type: "Persistent", MaxItems: 100, ContinuationToken: start}
	got, err := client.All(context.Background(), filter, internal.AcceptLanguage([]language.Tag{language.French}))
	want := []Achievement{{ID: "one", ServiceConfigID: "a", Name: "First"}, {ID: token}, {ID: "one", ServiceConfigID: "b"}, {ID: "two"}}
	if err != nil || !reflect.DeepEqual(got, want) || calls != len(bodies) {
		t.Fatalf("achievements = %+v, error = %v, requests = %d", got, err, calls)
	}
}

func TestAllRejectsContinuationCycles(t *testing.T) {
	for _, test := range []struct {
		name   string
		start  string
		tokens []string
	}{
		{name: "repeat", tokens: []string{"a", "a"}},
		{name: "cycle", tokens: []string{"a", "b", "a"}},
		{name: "starting token repeated", start: "a", tokens: []string{"a"}},
		{name: "cycle to starting token", start: "a", tokens: []string{"b", "a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := testClient(func(request *http.Request) (*http.Response, error) {
				if calls >= len(test.tokens) {
					t.Fatal("requested another page after repeated token")
				}
				body := fmt.Sprintf(`{"achievements":[{"id":"%d"}],"pagingInfo":{"continuationToken":%q}}`, calls, test.tokens[calls])
				calls++
				return response(request, http.StatusOK, io.NopCloser(strings.NewReader(body))), nil
			})
			got, err := client.All(context.Background(), Filter{TitleID: 42, ContinuationToken: test.start})
			if err == nil || got != nil || calls != len(test.tokens) {
				t.Fatalf("achievements = %+v, error = %v, requests = %d", got, err, calls)
			}
		})
	}
}

func TestAllPageLimit(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("overflow=%t", overflow), func(t *testing.T) {
			calls := 0
			client := testClient(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls > maxPages {
					t.Fatal("requested a page beyond the limit")
				}
				token := strconv.Itoa(calls)
				if calls == maxPages && !overflow {
					token = ""
				}
				body := fmt.Sprintf(`{"achievements":[],"pagingInfo":{"continuationToken":%q}}`, token)
				return response(request, http.StatusOK, io.NopCloser(strings.NewReader(body))), nil
			})
			got, err := client.All(context.Background(), Filter{TitleID: 42})
			if (err != nil) != overflow || (got == nil) != overflow || calls != maxPages {
				t.Fatalf("achievements = %+v, error = %v, requests = %d", got, err, calls)
			}
		})
	}
}

func TestAllAchievementLimit(t *testing.T) {
	for _, count := range []int{maxAchievements, maxAchievements + 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			offset := 0
			client := testClient(func(request *http.Request) (*http.Response, error) {
				end := min(offset+2048, count)
				var body strings.Builder
				body.WriteString(`{"achievements":[`)
				for i := offset; i < end; i++ {
					if i != offset {
						body.WriteByte(',')
					}
					fmt.Fprintf(&body, `{"id":"%d"}`, i)
				}
				body.WriteString(`,{"id":"0"},{}],"pagingInfo":{"continuationToken":`)
				token := ""
				if end != count {
					token = strconv.Itoa(end)
				}
				fmt.Fprintf(&body, "%q}}", token)
				offset = end
				return response(request, http.StatusOK, io.NopCloser(strings.NewReader(body.String()))), nil
			})
			got, err := client.All(context.Background(), Filter{TitleID: 42})
			if count > maxAchievements {
				if err == nil || got != nil {
					t.Fatalf("overflow returned %d achievements, error = %v", len(got), err)
				}
			} else if err != nil || len(got) != count || got[count-1].ID != strconv.Itoa(count-1) {
				t.Fatalf("received %d achievements, error = %v", len(got), err)
			}
		})
	}
}

func TestAchievementsResponseValidation(t *testing.T) {
	const empty = `{"achievements":[]}`
	for _, test := range []struct {
		name   string
		body   string
		status int
		valid  bool
	}{
		{name: "empty collection", body: empty, status: http.StatusOK, valid: true},
		{name: "exact byte limit", body: empty + strings.Repeat(" ", maxPageBytes-len(empty)), status: http.StatusOK, valid: true},
		{name: "whitespace exceeds limit", body: empty + strings.Repeat(" ", maxPageBytes-len(empty)+1), status: http.StatusOK},
		{name: "missing collection", body: `{}`, status: http.StatusOK},
		{name: "null collection", body: `{"achievements":null}`, status: http.StatusOK},
		{name: "malformed JSON", body: `{"achievements":[`, status: http.StatusOK},
		{name: "extra JSON", body: empty + `{}`, status: http.StatusOK},
		{name: "forbidden", body: `{"error":"forbidden"}`, status: http.StatusForbidden},
		{name: "unexpected success status", body: empty, status: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(test.body)}
			client := testClient(func(request *http.Request) (*http.Response, error) {
				return response(request, test.status, body), nil
			})
			page, err := client.Achievements(context.Background(), Filter{TitleID: 42})
			if (err == nil) != test.valid || !body.closed {
				t.Fatalf("page = %+v, error = %v, body closed = %t", page, err, body.closed)
			}
			if test.status != http.StatusOK && !strings.Contains(err.Error(), strconv.Itoa(test.status)) {
				t.Errorf("status absent from error: %v", err)
			}
		})
	}
}

type failingReader struct{ err error }

// Read returns the supplied error to exercise response read failures.
func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestAchievementsReadError(t *testing.T) {
	want := errors.New("response read failed")
	body := &trackedBody{Reader: failingReader{err: want}}
	client := testClient(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusOK, body), nil
	})
	if _, err := client.Achievements(context.Background(), Filter{TitleID: 42}); !errors.Is(err, want) || !body.closed {
		t.Fatalf("error = %v, body closed = %t", err, body.closed)
	}
}

func TestAllPropagatesRequestErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{name: "transport", ctx: context.Background(), err: errors.New("transport failed")},
		{name: "cancelled", ctx: cancelled, err: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(func(request *http.Request) (*http.Response, error) {
				if request.Context() != test.ctx {
					t.Fatal("request lost its caller context")
				}
				if err := request.Context().Err(); err != nil {
					return nil, err
				}
				return nil, test.err
			})
			if got, err := client.All(test.ctx, Filter{TitleID: 42}); !errors.Is(err, test.err) || got != nil {
				t.Fatalf("achievements = %+v, error = %v", got, err)
			}
		})
	}
}

func TestAchievementsRejectsInvalidXUID(t *testing.T) {
	for _, xuid := range []string{"", "not-a-number", "1/../2", "-1", "18446744073709551616"} {
		t.Run(xuid, func(t *testing.T) {
			client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("sent a request for an invalid user ID")
				return nil, nil
			})}, xsts.UserInfo{XUID: xuid})
			if _, err := client.Achievements(context.Background(), Filter{TitleID: 42}); err == nil {
				t.Fatal("accepted invalid user ID")
			}
		})
	}
}

func TestAchievementsRejectsInvalidMaxItems(t *testing.T) {
	values := []int{-1}
	if strconv.IntSize > 32 {
		overflow := int64(math.MaxInt32) + 1
		values = append(values, int(overflow))
	}
	for _, value := range values {
		for _, all := range []bool{false, true} {
			t.Run(fmt.Sprintf("maxItems=%d/all=%t", value, all), func(t *testing.T) {
				client := testClient(func(*http.Request) (*http.Response, error) {
					t.Fatal("sent a request for an invalid maximum item count")
					return nil, nil
				})
				var err error
				if all {
					_, err = client.All(context.Background(), Filter{MaxItems: value})
				} else {
					_, err = client.Achievements(context.Background(), Filter{MaxItems: value})
				}
				if err == nil {
					t.Fatal("accepted invalid maximum item count")
				}
			})
		}
	}
}
