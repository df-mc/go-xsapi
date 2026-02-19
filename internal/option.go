package internal

import (
	"net/http"
)

type RequestOption func(req *http.Request)

func Apply(req *http.Request, opts []RequestOption) {
	for _, opt := range opts {
		opt(req)
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
