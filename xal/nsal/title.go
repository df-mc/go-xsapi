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

func Default(ctx context.Context) (*TitleData, error) {
	defaultTitleMu.Lock()
	defer defaultTitleMu.Unlock()
	if defaultTitle != nil {
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
	defaultTitle   *TitleData
	defaultTitleMu sync.Mutex
)

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

type Token interface {
	SetAuthHeader(req *http.Request)
}

type TitleData struct {
	Endpoints         []Endpoint `json:"EndPoints"`
	SignaturePolicies []SignaturePolicy
	Certs             []Certificate
	RootCerts         []string
}

func (t *TitleData) Match(u *url.URL) (endpoint Endpoint, policy SignaturePolicy, ok bool) {
	for _, e := range t.Endpoints {
		if e.Match(u) {
			if e.SignaturePolicyIndex < len(t.SignaturePolicies) {
				policy = t.SignaturePolicies[e.SignaturePolicyIndex]
			} else {
				policy = AuthPolicy
			}
			endpoint, ok = e, true
			if e.HostType == HostTypeFQDN {
				break
			}
		}
	}
	return endpoint, policy, ok
}

type Endpoint struct {
	Protocol             string
	Host                 string
	Port                 int
	HostType             string
	Path                 string
	RelyingParty         string
	SubRelyingParty      string
	TokenType            string
	SignaturePolicyIndex int
	ClientCertIndex      []int
	ServerCertIndex      []int
	MinTLSVersion        string `json:"MinTlsVersion"`
}

// Match matches the URL with the Endpoint.
func (e Endpoint) Match(u *url.URL) bool {
	if e.Host == "*" {
		// haven't researched on this yet
		return false
	}
	if e.RelyingParty == "" {
		return false
	}
	if e.Protocol != u.Scheme {
		return false
	}
	switch e.HostType {
	case HostTypeFQDN:
		if u.Host != e.Host {
			return false
		}
	case HostTypeWildcard:
		if !e.matchWildcard(u.Host) {
			return false
		}
	case HostTypeCIDR:
		if !e.matchCIDR(u.Host) {
			return false
		}
	}
	if e.Port != 0 && strconv.Itoa(e.Port) == u.Port() {
		return false
	}
	if e.Path != "" && e.Path != u.Path {
		return false
	}
	return true
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

type Certificate struct {
	Thumbprint    string
	Issuer        bool `json:"IsIssuer"`
	RootCertIndex int
}
