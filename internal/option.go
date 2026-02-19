package internal

import (
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

type RequestOption func(req *http.Request)

func Apply(req *http.Request, opts []RequestOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(req)
		}
	}
}

func AcceptLanguage(tags []language.Tag) RequestOption {
	s := make([]string, len(tags))
	for i, tag := range tags {
		s[i] = tag.String()
	}
	return func(req *http.Request) {
		req.Header.Add("Accept-Language", strings.Join(s, ", "))
	}
}

func RequestHeader(key, value string) RequestOption {
	return func(req *http.Request) {
		req.Header.Set(key, value)
	}
}

func ContractVersion(v string) RequestOption {
	return RequestHeader("x-xbl-contract-version", v)
}
