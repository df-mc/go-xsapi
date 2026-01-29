package internal

import (
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

type RequestOption interface {
	setRequest(req *http.Request)
}

func Apply(req *http.Request, opts []RequestOption) {
	for _, opt := range opts {
		opt.setRequest(req)
	}
}

type AcceptLanguage []language.Tag

func (l AcceptLanguage) setRequest(req *http.Request) {
	s := make([]string, len(l))
	for i, tag := range l {
		s[i] = tag.String()
	}
	req.Header.Add("Accept-Language", strings.Join(s, ", "))
}

func RequestHeader(key, value string) RequestOption {
	return requestHeader{key, value}
}

type requestHeader struct{ k, v string }

func (opt requestHeader) setRequest(req *http.Request) {
	req.Header.Set(opt.k, opt.v)
}

func ContractVersion(v string) RequestOption {
	return RequestHeader("x-xbl-contract-version", v)
}

type RequestOptionFunc func(req *http.Request)

func (f RequestOptionFunc) setRequest(req *http.Request) {
	f(req)
}
