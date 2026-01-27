package nsal

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Default returns a default title data applied to the Xbox Live services.
// The resulting TitleData contains endpoints for various Xbox Live services,
// such as MPSD and RTA, and other endpoints that live on *.xboxlive.com.
// Note that authentication requests like XSTS will use AuthPolicy instead
// for signing. The TitleData may be reused from the cache if it has already
// been retrieved from the remote resource to reduce network request time.
func Default(ctx context.Context) (*TitleData, error) {
	defaultTitleMu.Lock()
	defer defaultTitleMu.Unlock()
	if defaultTitle != nil {
		// When the default title has already been cached, we re-use themselves.
		// Currently, there is no revalidation and it just reuses the data forever.
		return defaultTitle, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://title.mgt.xboxlive.com/titles/default/endpoints?type=1", nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("x-xbl-contract-version", "1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", req.URL, resp.Status)
	}
	var t *TitleData
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response body: %w", err)
	}
	if t == nil {
		return nil, errors.New("xal/nsal: invalid title data")
	}
	defaultTitle = t
	return t, nil
}

var (
	// defaultTitle holds the latest known default TitleData to be used by various services.
	// Note that authentication requests should still use AuthPolicy to sign the request using
	// their proof key.
	defaultTitle *TitleData
	// defaultTitleMu guards defaultTitle from concurrent read/write access.
	defaultTitleMu sync.Mutex
)

// Current returns a TitleData for the currently-authenticated title of the provided
// XSTS token.
// The resulting TitleData contains endpoints specific to the title authenticated
// in the XSTS token. Callers may cache TitleData once it has retrieved from the
// remote resource to reduce network request time and avoid incurring rate limits
// from NSAL.
func Current(ctx context.Context, token Token, proofKey *ecdsa.PrivateKey) (*TitleData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://title.mgt.xboxlive.com/titles/current/endpoints", nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("x-xbl-contract-version", "1")
	token.SetAuthHeader(req)
	AuthPolicy.Sign(req, nil, proofKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
	var t *TitleData
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response body: %w", err)
	}
	if t == nil {
		return nil, errors.New("xal/nsal: invalid title data")
	}
	return t, nil
}

// A Token represents an XSTS token that supports attaching an 'Authorization'
// header to the request to authenticate a request to the NSAL. It is used in
// the [Current] method for retrieving title-specific endpoints from NSAL.
type Token interface {
	SetAuthHeader(req *http.Request)
}

// TitleData contains endpoints along with its policies and certificates for
// a specific title. For Xbox Live services (*.xboxlive.com), [Default] contains
// most of the endpoints. For title-specific APIs and endpoints (like PlayFab or
// Minecraft Realms), [Current] can be used to request title-specific TitleData.
// A TitleData may be used for resolving relying parties based on the request URL
// so the caller can just make a request without noticing about the relying party.
type TitleData struct {
	// Endpoints contains the endpoints supported by this title.
	Endpoints []Endpoint `json:"EndPoints"`
	// SignaturePolicies lists signature policies that are associated
	// to the Endpoints with an index.
	SignaturePolicies []SignaturePolicy
	// Certs returns a list of Certificates which may be referenced
	// by one of the Endpoints by an index for specifying which certificate
	// should be used for the request. It is not currently used in the TitleData API.
	Certs []Certificate
	// RootCerts ...
	RootCerts []string
}

// Match resolves an Endpoint and a SignaturePolicy based on the request URL.
func (t *TitleData) Match(u *url.URL) (endpoint Endpoint, policy SignaturePolicy, ok bool) {
	for _, e := range t.Endpoints {
		if e.Match(u) {
			if e.SignaturePolicyIndex < len(t.SignaturePolicies) {
				policy = t.SignaturePolicies[e.SignaturePolicyIndex]
			} else {
				policy = AuthPolicy
			}
			endpoint, ok = e, true

			// Endpoint with HostTypeFQDN should be preferred over
			// HostTypeWildcard. If we match an endpoint for this,
			// we immediately break return the matching endpoint.
			if e.HostType == HostTypeFQDN {
				break
			}
		}
	}
	return endpoint, policy, ok
}

type Endpoint struct {
	// Protocol specifies the URL scheme that request URL should use for the endpoint.
	// It is typically 'https' and 'http'.
	Protocol string
	// Host is the hostname or the pattern used to match the request URL against.
	// The format and semantics of Host depends on HostType. For example, if the
	// HostType is HostTypeFQDN, the request URL should be exactly same as the Host.
	Host string
	// Port indicates the port that requests should use for this Endpoint.
	// It is an optional field and rarely used in most titles.
	Port uint16 `json:",omitempty"`
	// HostType indicates the format of the Host.
	// It is one of the constants above.
	HostType string
	// Path is an optional path that the requests should use for this Endpoint.
	Path string `json:",omitempty"`
	// RelyingParty specifies the relying party which the XSTS (Xbox Secure Token Service)
	// token should use it for their relying party. The relying party specifies which token
	// is valid for which service.
	RelyingParty string
	// SubRelyingParty is an optional alias of RelyingParty, which can be used alternatively to request
	// a same token that relies on the same party for this Endpoint.
	SubRelyingParty string `json:",omitempty"`
	// TokenType is always 'JWT'. It is unknown if other format is supported.
	TokenType string
	// SignaturePolicyIndex specifies the index for SignaturePolicy in the parent TitleData.
	SignaturePolicyIndex int
	// ClientCertIndex is an optional indices for the certificates that should be applied on the client.
	ClientCertIndex []int `json:",omitempty"`
	// ServerCertIndex is an optional indices for the certificates that should be applied on the server.
	ServerCertIndex []int `json:",omitempty"`
	// MinTLSVersion is the minimum version of the TLS that should be used on the requests on this Endpoint.
	// It is rarely used in most titles in Xbox Live.
	MinTLSVersion string `json:"MinTlsVersion"`
}

// Match matches the URL with the Endpoint.
func (e Endpoint) Match(u *url.URL) bool {
	if e.RelyingParty == "" {
		// Some endpoint reports an empty relying party with a Host of '*'.
		// We haven't researched on this yet, but we don't want to request
		// an XSTS token for an empty relying party so we will skip matching.
		return false
	}
	if e.Protocol != u.Scheme {
		return false
	}
	var matchHost bool
	switch e.HostType {
	case HostTypeFQDN:
		matchHost = u.Host == e.Host
	case HostTypeWildcard:
		matchHost = e.matchWildcard(u.Host)
	case HostTypeCIDR:
		matchHost = e.matchCIDR(u.Host)
	}
	return matchHost &&
		(e.Port == 0 || strconv.Itoa(int(e.Port)) == u.Port()) &&
		(e.Path == "" || e.Path == u.Path)
}

// matchCIDR returns true if the given host is in the CIDR prefix.
// Returns false otherwise.
func (e Endpoint) matchCIDR(host string) bool {
	_, ipnet, err := net.ParseCIDR(e.Host)
	if err != nil {
		return false
	}
	addr, err := net.ResolveIPAddr("ip", host)
	if err != nil {
		return false
	}
	return ipnet.Contains(addr.IP)
}

// matchWildcard matches a wildcard host which are prefixed by '*'.
// An example might include '*.xboxlive.com' or '*.playfabapi.com'.
// It internally converts the Host to a regex pattern and matches
// the given string.
func (e Endpoint) matchWildcard(host string) bool {
	if len(e.Host) == 0 || e.Host[0] != '*' {
		// The host should always start with '*'.
		return false
	}
	// Convert the host to a regexp pattern, this is also done on the original implementation.
	pattern := strings.ReplaceAll(regexp.QuoteMeta(e.Host), "\\*", ".*")
	ok, _ := regexp.MatchString(pattern, host)
	return ok
}

const (
	// HostTypeFQDN indicates that [Endpoint.Host] is a Fully Qualified Domain Name (FQDN)
	// and is only valid for requests which host matches the exact same domain.
	HostTypeFQDN = "fqdn"
	// HostTypeWildcard indicates that an [Endpoint] is valid for multiple subdomains prefixed
	// by '*'. An example might include '*.xboxlive.com' or '*.playfabapi.com'.
	HostTypeWildcard = "wildcard"
	// HostTypeCIDR indicates that the host is notated as a CIDR IP address with prefix
	// and is only valid for a specific set of IP addresses. The usage is currently unknown
	// since it is unused for most title configurations in NSAL.
	HostTypeCIDR = "cidr"
)

// Certificate represents a TLS certificate that should be used on the requests on the Endpoints
// listed on a TitleData for a specific title.
type Certificate struct {
	// Thumbprint is the thumbprint for the Certificate.
	Thumbprint string
	// Issuer is the issuer of the Certificate.
	Issuer bool `json:"IsIssuer"`
	// RootCertIndex optionally indicates the index of root certificate
	// listed in a TitleData.
	RootCertIndex int
}
