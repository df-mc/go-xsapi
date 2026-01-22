package mpsd2

type Client struct {
}

type API interface {
	rta.Provider
}

type Provider interface {
	MPSD() *Client
}
