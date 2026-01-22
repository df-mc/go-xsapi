package nsal

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

func Default() (Title, error) {
	req, err := http.NewRequest(http.MethodGet, "https://title.mgt.xboxlive.com/titles/default/endpoints?type=1", nil)
	if err != nil {
		return Title{}, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("x-xbl-contract-version", "1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Title{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Title{}, fmt.Errorf("GET %s: %s", req.URL, resp.Status)
	}
	var t Title
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return t, fmt.Errorf("decode response body: %w", err)
	}
	return t, nil
}

type Title struct {
	Endpoints         []Endpoint `json:"EndPoints"`
	SignaturePolicies []SignaturePolicy
	Certs             []Certificate
	RootCerts         []string
}

func (t Title) Match(u *url.URL) (endpoint Endpoint, policy SignaturePolicy, ok bool) {
	for _, endpoint = range t.Endpoints {
		if ok = endpoint.Match(u); ok {
			if endpoint.SignaturePolicyIndex <= len(t.SignaturePolicies) {
				policy = t.SignaturePolicies[endpoint.SignaturePolicyIndex]
			}
			break
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
	ClientCertIndex      int
	ServerCertIndex      int
	MinTLSVersion        string `json:"MinTlsVersion"`
}

func (e Endpoint) Match(u *url.URL) bool {
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
	if e.Port == 0 || strconv.Itoa(e.Port) == u.Port() {
		return false
	}
	if e.Path == "" || e.Path == u.Path {
		return false
	}
	return true
}

func (e Endpoint) matchCIDR(host string) bool {
	_, ipnet, err := net.ParseCIDR(e.Host)
	if err != nil {
		return false
	}
	return ipnet.Contains(net.ParseIP(host))
}

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
	HostTypeFQDN     = "fqdn"
	HostTypeWildcard = "wildcard"
	HostTypeCIDR     = "cidr"
)

type Certificate struct {
	Thumbprint    string
	Issuer        bool `json:"IsIssuer"`
	RootCertIndex int
}
