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

	"github.com/df-mc/go-xsapi/xal"
)

// Default returns the default TitleData used for generic Xbox Live services.
//
// The returned TitleData describes endpoints, signature policies, and
// certificates for common Xbox Live services (which is typically hosted
// under *.xboxlive.com).
//
// Default caches the result in memory. Subsequence calls return the
// cached value without revalidation.
func Default(ctx context.Context) (*TitleData, error) {
	defaultTitleMu.Lock()
	defer defaultTitleMu.Unlock()
	if defaultTitle != nil {
		// When the default title has already been cached, we reuse it.
		// Currently, there is no revalidation and it just reuses the data forever.
		return defaultTitle, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://title.mgt.xboxlive.com/titles/default/endpoints?type=1", nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("x-xbl-contract-version", "1")

	resp, err := xal.ContextClient(ctx).Do(req)
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

// Title retrieves TitleData for the specified title ID from the NSAL endpoint.
//
// The titleID identifies the title whose endpoint configuration should
// be returned. A special value of "current" refers to the title associated
// with the XSTS token used in the request.
//
// Unlike [Default], Title does not cache the response. Callers that repeatedly
// access the same title should cache the returned TitleData to reduce latency
// and avoid unnecessary network requests.
//
// The provided context controls the lifetime of the HTTP request.
func Title(ctx context.Context, token interface{ SetAuthHeader(req *http.Request) }, proofKey *ecdsa.PrivateKey, titleID string) (*TitleData, error) {
	requestURL := endpoint.JoinPath(
		"titles", titleID, "endpoints",
	).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("xal/nsal: make request: %w", err)
	}
	req.Header.Set("x-xbl-contract-version", "1")
	token.SetAuthHeader(req)
	AuthPolicy.Sign(req, nil, proofKey)

	resp, err := xal.ContextClient(ctx).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
	var t *TitleData
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("xal/nsal: decode response body: %w", err)
	}
	if t == nil {
		return nil, errors.New("xal/nsal: invalid title data response")
	}
	return t, nil
}

// Current retrieves TitleData for the title authenticated by the provided XSTS token.
// Current is equivalent to calling Title with the special title ID "current".
//
// Unlike [Default], Current does not cache the response. Callers that repeatedly
// access the same title should cache the returned TitleData to reduce latency
// and avoid unnecessary network requests.
//
// The provided context controls the lifetime of the HTTP request.
func Current(ctx context.Context, token Token, proofKey *ecdsa.PrivateKey) (*TitleData, error) {
	return Title(ctx, token, proofKey, "current")
}

// A Token represents an XSTS token that supports attaching an 'Authorization'
// header to the request to authenticate a request to the NSAL. It is used in
// the [Title] method for retrieving title-specific endpoints from NSAL.
type Token interface {
	SetAuthHeader(req *http.Request)
}

// TitleData describes the network configuration for a specific title as returned
// by NSAL. It contains the set of endpoints the title is allowed to call in Xbox
// Live, along with the signature policies and certificates required to access those
// endpoints.
//
// For general Xbox Live services (for example, services under *.xboxlive.com), [Default]
// should be used for most cases. For title-specific services such as PlayFab
// or Minecraft Realms, [Current] or [Title] should be used to retrieve configuration scoped
// to the title.
//
// A TitleData can be used to resolve the correct relying party and signature policy
// for an outgoing request based solely on its URL. This allows callers to make HTTP
// requests without manually determining which relying party to use when requesting
// an XSTS token.
type TitleData struct {
	// Endpoints lists the endpoints supported by this title. Each endpoint
	// defines matching rules and the associated relying party and security
	// configuration for requests targeting that endpoint.
	Endpoints []Endpoint `json:"EndPoints"`

	// SignaturePolicies contains the signature policies referenced by
	// Endpoints. Each endpoint may refer to a policy by index. If an
	// Endpoint does not reference a valid index, the default AuthPolicy
	// is used.
	SignaturePolicies []SignaturePolicy

	// Certs contains certificates that may be referenced by Endpoints through
	// ClientCertIndex or ServerCertIndex. This field is currently reserved for
	// compatibility with the actual NSAL schema and may not be populated
	// for most titles.
	Certs []Certificate

	// RootCerts contains root certificate that may be referenced by Certs
	// by index. This field is currently reserved for compatibility with
	// the actual NSAL schema and may not be populated for most titles.
	RootCerts []string
}

// Match resolves the Endpoint and SignaturePolicy that apply to the provided URL.
//
// It iterates through the configured Endpoints in order. If an Endpoint with
// HostTypeFQDN matches, it is returned immediately. Otherwise, the last
// matching Endpoint is returned. As a result, an Endpoint with HostTypeFQDN
// effectively takes precedence over one with HostTypeWildcard only if it
// appears after the wildcard Endpoint in the list.
//
// If the referenced SignaturePolicyIndex is invalid or absent, the default
// AuthPolicy is used.
//
// The returned bool value reports whether a matching Endpoint was found.
func (t *TitleData) Match(u *url.URL) (endpoint Endpoint, policy SignaturePolicy, ok bool) {
	for _, e := range t.Endpoints {
		if e.Match(u) {
			if e.SignaturePolicyIndex != nil && *e.SignaturePolicyIndex >= 0 && *e.SignaturePolicyIndex < len(t.SignaturePolicies) {
				policy = t.SignaturePolicies[*e.SignaturePolicyIndex]
			} else {
				policy = AuthPolicy
			}
			endpoint, ok = e, true

			// An Endpoint with HostTypeFQDN is an exact match, so stop
			// searching and return it immediately.
			if e.HostType == HostTypeFQDN {
				break
			}
		}
	}
	return endpoint, policy, ok
}

// Endpoint describes a single network endpoint available to a title.
// It defines how an outgoing request URL is matched and specifies the
// relying party and security configuration that must be applied when
// accessing the endpoint.
type Endpoint struct {
	// Protocol specifies the required URL scheme, such as "https" or "http".
	Protocol string

	// Host defines the hostname or pattern used for matching.
	// Its interpretation depends on HostType.
	Host string

	// Port specifies the required port for matching. If zero, any port
	// is accepted.
	Port uint16 `json:",omitempty"`

	// HostType describes how Host should be interpreted. Supported
	// values include the values defined by the constants below.
	HostType string

	// Path optionally restricts matching to a specific request path.
	// If empty, any path is accepted.
	Path string `json:",omitempty"`

	// RelyingParty specifies the relying party that must be used when
	// requesting an XSTS token for this endpoint. The relying party
	// determines which service the token will be valid and which
	// claims are included in the token.
	RelyingParty string

	// SubRelyingParty optionally provides an alternate relying party
	// alias that resolves to the same effective relying party.
	SubRelyingParty string `json:",omitempty"`

	// TokenType is always 'JWT'. It is unknown if other format is supported.
	TokenType string

	// SignaturePolicyIndex refers to a signature policy in the parent TitleData
	// by index. If nil or absent, the default AuthPolicy will be used in [TitleData.Match].
	SignaturePolicyIndex *int

	// ClientCertIndex lists indices into [TitleData.Certs] identifying client
	// certificates that should be presented when connecting to this endpoint.
	ClientCertIndex []int `json:",omitempty"`

	// ServerCertIndex lists indices into [TitleData.Certs] identifying server
	// certificates expected for this endpoint.
	ServerCertIndex []int `json:",omitempty"`

	// MinTLSVersion specifies the minimum TLS version required when
	// establishing a connection to this endpoint.
	MinTLSVersion string `json:"MinTlsVersion"`
}

// Match reports whether the provided URL matches the rules defined by Endpoint.
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
		matchHost = e.Host == u.Hostname()
	case HostTypeWildcard:
		matchHost = e.matchWildcard(u.Hostname())
	case HostTypeCIDR:
		matchHost = e.matchCIDR(u.Hostname())
	}
	return matchHost &&
		(e.Port == 0 || strconv.Itoa(int(e.Port)) == effectivePort(u)) &&
		(e.Path == "" || e.Path == u.Path)
}

// effectivePort returns the port from u, falling back to the default
// port for the scheme if u.Port() is empty.
// TODO: It is unclear whether the NSAL endpoint port field refers to
// the effective port (where 443 matches https://foo.xboxlive.com) or
// the explicit port (where 443 only matches https://foo.xboxlive.com:443).
// For now, we treat missing ports as their scheme defaults.
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
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
	pattern := "^" + strings.ReplaceAll(regexp.QuoteMeta(e.Host), "\\*", ".*") + "$"
	ok, _ := regexp.MatchString(pattern, host)
	return ok
}

const (
	// HostTypeFQDN indicates that [Endpoint.Host] is a fully qualified
	// domain name and must match the request host exactly.
	HostTypeFQDN = "fqdn"

	// HostTypeWildcard indicates that [Endpoint.Host] contains a leading
	// wildcard (for example, "*.xboxlive.com") matches multiple subdomains.
	HostTypeWildcard = "wildcard"

	// HostTypeCIDR indicates that [Endpoint.Host] is a CIDR notation
	// IP prefix and matches hosts whose resolved IP address is included
	// within that range. It is currently not used by most titles.
	HostTypeCIDR = "cidr"
)

// Certificate represents a TLS certificate referenced by a TitleData.
// Certificates may be required for client authentication or used to
// validate server connection depending on endpoint configuration.
type Certificate struct {
	// Thumbprint identifies the certificate.
	Thumbprint string

	// Issuer reports whether this certificate represents
	// an issuing authority rather than a leaf certificate.
	Issuer bool `json:"IsIssuer"`

	// RootCertIndex optionally refers to a root certificate in [TitleData.RootCerts]
	// by index.
	RootCertIndex int
}

// endpoint is the base URL used for NSAL requests.
var endpoint = &url.URL{
	Scheme: "https",
	Host:   "title.mgt.xboxlive.com",
}
