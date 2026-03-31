package internal

import (
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

// RequestOption specifies an option to be applied to an outgoing HTTP request.
//
// Callers may accept multiple RequestOptions as a variadic or slice parameter.
// Options must be applied to the request using [Apply].
//
// A RequestOption must be reusable and must not hold any per-request state.
type RequestOption func(req *http.Request)

// Apply applies the given RequestOptions to the request in order.
// Caller-provided opts take precedence over any defaults appended after them.
// For example, append caller opts before defaults like append(opts, internal.DefaultLanguage)
// so that the caller's preferences are evaluated first.
func Apply(req *http.Request, opts []RequestOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(req)
		}
	}
}

// AcceptLanguage returns a [internal.RequestOption] that appends the given
// language tags to the 'Accept-Language' header on outgoing requests,
// preserving any tags already present in the header.
func AcceptLanguage(tags []language.Tag) RequestOption {
	s := make([]string, len(tags))
	for i, tag := range tags {
		s[i] = tag.String()
	}
	return func(req *http.Request) {
		req.Header.Add("Accept-Language", strings.Join(s, ", "))
	}
}

// RequestHeader returns a [internal.RequestOption] that sets a request header
// with the given name and value on outgoing requests.
func RequestHeader(key, value string) RequestOption {
	return func(req *http.Request) {
		req.Header.Set(key, value)
	}
}

// ContractVersion returns a [RequestOption] that sets the X-Xbl-Contract-Version header
// to the specified version string. The appropriate version depends on the target endpoint.
// The value 'vnext' may also be accepted for some endpoint.
func ContractVersion(v string) RequestOption {
	return RequestHeader("x-xbl-contract-version", v)
}

// DefaultLanguage is an [AcceptLanguage] option that appends American English and English
// as fallback language preferences to the 'Accept-Language' header.
// It is intended to be appended after any caller-provided options so that it always
// appears as the lowest-priority suffix in the header value.
var DefaultLanguage = AcceptLanguage([]language.Tag{
	language.AmericanEnglish,
	language.English,
})
