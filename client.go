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
	"sync"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/mpsd"
	"github.com/df-mc/go-xsapi/rta"
	"github.com/df-mc/go-xsapi/social"
	"github.com/df-mc/go-xsapi/social/chat"
	"github.com/df-mc/go-xsapi/social/notification"
	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/df-mc/go-xsapi/xal/xsts"
	"golang.org/x/text/language"
)

func NewClient(src TokenSource) (*Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	var c ClientConfig
	return c.New(ctx, src)
}

func (config ClientConfig) New(ctx context.Context, src TokenSource) (*Client, error) {
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	c := &Client{
		config: config,
		src:    src,
	}
	c.client = new(http.Client)
	*c.client = *config.HTTPClient
	c.client.Transport = c

	token, err := src.XSTSToken(ctx, internal.XBLRelyingParty)
	if err != nil {
		return nil, fmt.Errorf("request XSTS token: %w", err)
	}
	xui := token.UserInfo()
	if xui.XUID == "" {
		return nil, errors.New("xsapi: authorization token does not claim XUID")
	}
	c.userInfo = xui

	c.defaultTitle, err = nsal.Default(ctx)
	if err != nil {
		return nil, fmt.Errorf("request NSAL default title data: %w", err)
	}
	c.currentTitle, err = nsal.Current(ctx, token, src.ProofKey())
	if err != nil {
		return nil, fmt.Errorf("request NSAL title data for current authenticated title: %w", err)
	}

	c.rta, err = config.RTADialer.DialContext(ctx, c.HTTPClient())
	if err != nil {
		return nil, fmt.Errorf("dial RTA: %w", err)
	}
	if config.EnableChat {
		c.chat, err = chat.Dial(ctx, c.HTTPClient(), c.Log())
		if err != nil {
			return nil, fmt.Errorf("dial chat: %w", err)
		}
	}
	c.notification = notification.New(c.HTTPClient(), c.chat, c.UserInfo(), c.Log())
	c.mpsd = mpsd.New(c.HTTPClient(), c.RTA(), c.UserInfo(), c.Log())
	c.social = social.New(c.HTTPClient(), c.RTA(), c.UserInfo(), c.Log())
	return c, nil
}

type TokenSource interface {
	xsts.TokenSource
	ProofKey() *ecdsa.PrivateKey
}

type ClientConfig struct {
	HTTPClient *http.Client
	Logger     *slog.Logger
	RTADialer  rta.Dialer
	EnableChat bool
}

type Client struct {
	config ClientConfig
	client *http.Client
	src    TokenSource

	defaultTitle, currentTitle *nsal.TitleData
	userInfo                   xsts.UserInfo

	rta          *rta.Conn
	mpsd         *mpsd.Client
	social       *social.Client
	notification *notification.Client

	chat *chat.Conn

	once sync.Once
}

func (c *Client) HTTPClient() *http.Client {
	return c.client
}

func (c *Client) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}

	ctx := req.Context()
	token, policy, err := c.TokenAndSignature(ctx, req.URL)
	if err != nil {
		return nil, fmt.Errorf("request XSTS token and signature: %w", err)
	}

	var (
		req2 = req.Clone(ctx)
		data []byte
	)
	token.SetAuthHeader(req2)
	if req.Body != nil && req.ContentLength > 0 {
		signingBuffer := &bytes.Buffer{}
		if _, err := signingBuffer.ReadFrom(req.Body); err != nil {
			signingBuffer.Reset()
			return nil, fmt.Errorf("clone request body: %w", err)
		}
		data, req2.Body = signingBuffer.Bytes(), io.NopCloser(signingBuffer)
	}
	policy.Sign(req2, data, c.src.ProofKey())

	return c.baseTransport().RoundTrip(req2)
}

func (c *Client) baseTransport() http.RoundTripper {
	if t := c.config.HTTPClient.Transport; t != nil {
		return t
	}
	return http.DefaultTransport
}

func (c *Client) TokenAndSignature(ctx context.Context, u *url.URL) (_ *xsts.Token, policy nsal.SignaturePolicy, _ error) {
	endpoint, policy, ok := c.currentTitle.Match(u)
	if !ok {
		endpoint, policy, ok = c.defaultTitle.Match(u)
		if !ok {
			return nil, policy, fmt.Errorf("no endpoint was found for %s", u)
		}
	}
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

func (c *Client) Social() *social.Client { return c.social }

func (c *Client) RTA() *rta.Conn {
	return c.rta
}

func (c *Client) Chat() *chat.Conn {
	if c.chat == nil {
		panic("xsapi: chat is not enabled")
	}
	return c.chat
}

func (c *Client) Notifications() *notification.Client {
	return c.notification
}

func (c *Client) UserInfo() xsts.UserInfo {
	return c.userInfo
}

func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return c.CloseContext(ctx)
}

func (c *Client) CloseContext(ctx context.Context) (err error) {
	c.once.Do(func() {
		err = errors.Join(
			c.mpsd.CloseContext(ctx),
			c.social.CloseContext(ctx),
			c.chat.Close(),

			c.rta.Close(),
		)
	})
	return err
}

func AcceptLanguage(tags []language.Tag) internal.RequestOption {
	return internal.AcceptLanguage(tags)
}

func RequestHeader(key, value string) internal.RequestOption {
	return internal.RequestHeader(key, value)
}
