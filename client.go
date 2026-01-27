package xsapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/mpsd"
	"github.com/df-mc/go-xsapi/rta"
	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/df-mc/go-xsapi/xal/xsts"
)

func NewClient(src TokenSource, config *ClientConfig) (*Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return NewClientWithContext(ctx, src, config)
}

func NewClientWithContext(ctx context.Context, src TokenSource, config *ClientConfig) (*Client, error) {
	if config == nil {
		config = &ClientConfig{}
	}
	if config.Transport == nil {
		config.Transport = http.DefaultTransport
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	c := &Client{
		config: config,
		src:    src,
	}
	c.client = &http.Client{Transport: c}
	token, err := src.XSTSToken(ctx, internal.XBLRelyingParty)
	if err != nil {
		return nil, fmt.Errorf("request XSTS token: %w", err)
	}
	if token.UserInfo().XUID == "" {
		return nil, errors.New("xsapi: authorization token does not claim XUID")
	}
	c.userInfo = token.UserInfo()
	c.defaultTitle, err = nsal.Default(ctx)
	if err != nil {
		return nil, fmt.Errorf("request NSAL default title data: %w", err)
	}
	c.currentTitle, err = nsal.Current(ctx, token, src.ProofKey())
	if err != nil {
		return nil, fmt.Errorf("request NSAL title data for current authenticated title: %w", err)
	}

	c.rta, err = config.RTADialer.DialContext(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("dial RTA: %w", err)
	}
	c.mpsd = mpsd.New(c)
	return c, nil
}

type TokenSource interface {
	xsts.TokenSource
	ProofKey() *ecdsa.PrivateKey
}

type ClientConfig struct {
	Transport http.RoundTripper
	Logger    *slog.Logger
	RTADialer rta.Dialer
}

type Client struct {
	config *ClientConfig
	client *http.Client
	src    TokenSource

	defaultTitle, currentTitle *nsal.TitleData
	userInfo                   xsts.UserInfo

	mpsd *mpsd.Client
	rta  *rta.Conn
}

func (c *Client) HTTPClient() *http.Client {
	return c.client
}

func (c *Client) RoundTrip(req *http.Request) (*http.Response, error) {
	reqBody, reqBodyClosed := req.Body, false
	if reqBody != nil {
		defer func() {
			if !reqBodyClosed {
				_ = reqBody.Close()
			}
		}()
	}

	ctx := req.Context()
	token, policy, err := c.TokenAndSignature(ctx, req.URL)
	if err != nil {
		return nil, fmt.Errorf("request XSTS token and signature: %w", err)
	}

	req2 := req.Clone(ctx)
	token.SetAuthHeader(req2)
	if reqBody != nil && req.ContentLength > 0 {
		signingBuffer := &bytes.Buffer{}
		if _, err := signingBuffer.ReadFrom(reqBody); err != nil {
			signingBuffer.Reset()
			return nil, fmt.Errorf("clone request body: %w", err)
		}
		policy.Sign(req2, signingBuffer.Bytes(), c.src.ProofKey())
		req2.Body = io.NopCloser(signingBuffer)
	}

	reqBodyClosed = true
	return c.config.Transport.RoundTrip(req2)
}

func (c *Client) TokenAndSignature(ctx context.Context, u *url.URL) (_ *xsts.Token, policy nsal.SignaturePolicy, _ error) {
	endpoint, policy, ok := c.currentTitle.Match(u)
	if !ok {
		endpoint, policy, ok = c.defaultTitle.Match(u)
		if !ok {
			return nil, policy, fmt.Errorf("no endpoint was found for %s", u)
		}
	}
	fmt.Printf("resolved an XSTS token that relies on the party %q for %s\n", endpoint.RelyingParty, u)
	token, err := c.src.XSTSToken(ctx, endpoint.RelyingParty)
	if err != nil {
		return nil, policy, fmt.Errorf("request XSTS token: %w", err)
	}
	return token, policy, nil
}

func (c *Client) Log() *slog.Logger {
	return c.config.Logger
}

func (c *Client) TokenSource() TokenSource {
	return c.src
}

func (c *Client) MPSD() *mpsd.Client {
	return c.mpsd
}

func (c *Client) RTA() *rta.Conn {
	return c.rta
}

func (c *Client) UserInfo() xsts.UserInfo {
	return c.userInfo
}
