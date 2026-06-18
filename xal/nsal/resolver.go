package nsal

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sync"
)

const authorizationRelyingParty = "http://xboxlive.com"

var defaultTitleIDs = []string{"current", "default"}

// TokenSource supplies authorization tokens and the proof key used to resolve
// NSAL title data, XSTS tokens, and request signatures.
type TokenSource interface {
	Token(ctx context.Context, relyingParty string) (Token, error)
	ProofKey() *ecdsa.PrivateKey
}

// ResolverConfig configures a [Resolver].
type ResolverConfig struct {
	// TitleIDs lists title data sources to resolve lazily in precedence order.
	// Supported special values are "current" and "default". A nil TitleIDs
	// slice defaults to "current" followed by "default"; an empty non-nil
	// slice disables lazy title data resolution. Other values are passed to
	// [Title] as explicit title IDs.
	TitleIDs []string

	// Titles lists already known title data sources in precedence order. These
	// entries are matched before lazily resolved title data.
	Titles []*TitleData
}

// New creates a Resolver using conf and src.
//
// New panics if src is nil.
func (conf ResolverConfig) New(src TokenSource) *Resolver {
	if src == nil {
		panic("xal/nsal: nil TokenSource")
	}
	if conf.TitleIDs == nil {
		conf.TitleIDs = defaultTitleIDs
	}
	return &Resolver{
		conf: ResolverConfig{
			TitleIDs: slices.Clone(conf.TitleIDs),
			Titles:   slices.Clone(conf.Titles),
		},
		src:     src,
		cached:  make(map[string]*TitleData),
		loading: make(map[string]*titleRequest),
	}
}

// Resolver resolves the Xbox Live endpoint, relying party, authorization
// token, and signature policy for outgoing request URLs.
//
// A Resolver owns NSAL title data lookup and caching. Use [Resolve] when only
// the endpoint and signature policy are needed, or [TokenAndSignature] when a
// request also needs the XSTS token for the resolved relying party.
//
// Lazy NSAL title-data requests use the HTTP client stored in ctx under
// [github.com/df-mc/go-xsapi/v2/xal.HTTPClient], or http.DefaultClient when no
// client is present. [Transport.Base] only applies to the final request made
// after a URL has been resolved.
type Resolver struct {
	conf ResolverConfig
	src  TokenSource

	mu      sync.Mutex
	cached  map[string]*TitleData
	loading map[string]*titleRequest
}

type titleRequest struct {
	done  chan struct{}
	title *TitleData
	err   error
}

// NewResolver returns a Resolver that lazily resolves title-specific NSAL data
// for the authenticated title before the default NSAL title data.
func NewResolver(src TokenSource) *Resolver {
	return ResolverConfig{}.New(src)
}

// Resolve resolves the endpoint and signature policy that apply to u, loading
// configured title data as needed.
func (r *Resolver) Resolve(ctx context.Context, u *url.URL) (endpoint Endpoint, policy SignaturePolicy, _ error) {
	if endpoint, policy, ok := matchTitleData(r.titles(), u); ok {
		return endpoint, policy, nil
	}
	var errs []error
	for _, titleID := range r.conf.TitleIDs {
		title, err := r.title(ctx, titleID)
		if err != nil {
			errs = append(errs, fmt.Errorf("load title data for %q: %w", titleID, err))
			continue
		}
		if endpoint, policy, ok := title.Match(u); ok {
			return endpoint, policy, nil
		}
	}
	if err := errors.Join(errs...); err != nil {
		return endpoint, policy, fmt.Errorf("no endpoint was found for %s: %w", u, err)
	}
	return endpoint, policy, fmt.Errorf("no endpoint was found for %s", u)
}

// TokenAndSignature resolves an XSTS token and signature policy for the given URL.
func (r *Resolver) TokenAndSignature(ctx context.Context, u *url.URL) (_ Token, policy SignaturePolicy, _ error) {
	endpoint, policy, err := r.Resolve(ctx, u)
	if err != nil {
		return nil, policy, err
	}
	token, err := r.src.Token(ctx, endpoint.RelyingParty)
	if err != nil {
		return nil, policy, fmt.Errorf("request XSTS token: %w", err)
	}
	return token, policy, nil
}

func (r *Resolver) proofKey() *ecdsa.PrivateKey {
	return r.src.ProofKey()
}

func (r *Resolver) title(ctx context.Context, titleID string) (*TitleData, error) {
	if titleID == "" {
		return nil, errors.New("xal/nsal: empty title ID")
	}
	r.mu.Lock()
	if title, ok := r.cached[titleID]; ok {
		r.mu.Unlock()
		return title, nil
	}
	if req, ok := r.loading[titleID]; ok {
		r.mu.Unlock()
		select {
		case <-req.done:
			if req.err != nil {
				return nil, req.err
			}
			return req.title, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	req := &titleRequest{done: make(chan struct{})}
	r.loading[titleID] = req
	r.mu.Unlock()

	title, err := r.loadTitle(ctx, titleID)

	r.mu.Lock()
	if err == nil {
		r.cached[titleID] = title
	}
	req.title, req.err = title, err
	delete(r.loading, titleID)
	close(req.done)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return title, nil
}

func (r *Resolver) loadTitle(ctx context.Context, titleID string) (*TitleData, error) {
	switch titleID {
	case "default":
		title, err := Default(ctx)
		if err != nil {
			return nil, fmt.Errorf("request default title data: %w", err)
		}
		return title, nil
	case "current":
		token, err := r.authorizationToken(ctx)
		if err != nil {
			return nil, err
		}
		title, err := Current(ctx, token, r.src.ProofKey())
		if err != nil {
			return nil, fmt.Errorf("request current title data: %w", err)
		}
		return title, nil
	default:
		token, err := r.authorizationToken(ctx)
		if err != nil {
			return nil, err
		}
		title, err := Title(ctx, token, r.src.ProofKey(), titleID)
		if err != nil {
			return nil, fmt.Errorf("request title data for %q: %w", titleID, err)
		}
		return title, nil
	}
}

func (r *Resolver) authorizationToken(ctx context.Context) (Token, error) {
	token, err := r.src.Token(ctx, authorizationRelyingParty)
	if err != nil {
		return nil, fmt.Errorf("request authorization token: %w", err)
	}
	return token, nil
}

func (r *Resolver) titles() []*TitleData {
	r.mu.Lock()
	defer r.mu.Unlock()
	titles := slices.Clone(r.conf.Titles)
	for _, titleID := range r.conf.TitleIDs {
		if title := r.cached[titleID]; title != nil {
			titles = append(titles, title)
		}
	}
	return titles
}

func matchTitleData(titles []*TitleData, u *url.URL) (endpoint Endpoint, policy SignaturePolicy, ok bool) {
	for _, title := range titles {
		if endpoint, policy, ok = title.Match(u); ok {
			return endpoint, policy, true
		}
	}
	return endpoint, policy, false
}
