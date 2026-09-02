package titlestorage

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal/nsal"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"github.com/google/uuid"
)

// New returns a new Client using the provided components.
func New(client *http.Client, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	return &Client{
		client:   client,
		userInfo: userInfo,
		log:      log,

		friendlyName: generateFriendlyName(),
		lockExt:      strconv.FormatUint(uint64(mathrand.Uint32()), 10) + "_" + userInfo.XUID,
	}
}

// generateFriendlyName generates a name for the device identification
// in the same format seen in Windows devices: 'DESKTOP-XXXXXXX'.
func generateFriendlyName() string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("DESKTOP-%07X", b[:])
}

// Client implements an API client for Xbox Live Title Storage.
type Client struct {
	client   *http.Client
	userInfo xsts.UserInfo
	log      *slog.Logger

	friendlyName string
	lockExt      string
}

func (c *Client) Open(ctx context.Context, serviceConfigID uuid.UUID, packageFamilyName string) (*Storage, error) {
	storage := &Storage{
		serviceConfigID:   serviceConfigID,
		packageFamilyName: packageFamilyName,

		client: c,

		closed: make(chan struct{}),
	}
	if err := storage.lock(ctx); err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	return storage, nil
}

type Storage struct {
	serviceConfigID   uuid.UUID
	packageFamilyName string
	friendlyName      string

	// client is the API client bound to this Storage.
	client *Client

	closed chan struct{}
	once   sync.Once
}

func (storage *Storage) Containers(ctx context.Context, _ ContainerFilter) (*ContainerResult, error) {
	select {
	case <-storage.closed:
		return nil, net.ErrClosed
	default:
	}

	requestURL := endpointURL.JoinPath(
		"/connectedstorage/users/xuid("+storage.client.userInfo.XUID+")",
		"/scids", strings.ToUpper(storage.serviceConfigID.String()),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := storage.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var result *ContainerResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		if result == nil {
			return nil, errors.New("titlestorage: invalid list container result")
		}
		return result, nil
	default:
		return nil, unexpectedStatusCode(resp)
	}
}

func (storage *Storage) Atoms(ctx context.Context, containerID string) ([]Atom, error) {
	select {
	case <-storage.closed:
		return nil, net.ErrClosed
	default:
	}

	requestURL := endpointURL.JoinPath(
		"/connectedstorage/users/xuid("+storage.client.userInfo.XUID+")",
		"/scids", strings.ToUpper(storage.serviceConfigID.String()),
		"/savedgames", strings.TrimSuffix(containerID, ",savedgame"),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := storage.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var result struct {
			Atoms []Atom `json:"atoms"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		// Untested but I think a container should have at least one atom associated with it.
		// I think it would be good to guarantee that atoms contain at least one element in
		// case the caller is expecting a container that contains only a single atom, such as
		// game settings
		if len(result.Atoms) == 0 {
			return nil, errors.New("titlestorage: no atom included in container")
		}
		return result.Atoms, nil
	default:
		return nil, unexpectedStatusCode(resp)
	}
}

func (storage *Storage) Atom(ctx context.Context, atomID string) (io.ReadCloser, error) {
	select {
	case <-storage.closed:
		return nil, net.ErrClosed
	default:
	}

	requestURL := endpointURL.JoinPath(
		"/connectedstorage/users/xuid("+storage.client.userInfo.XUID+")",
		"/scids", strings.ToUpper(storage.serviceConfigID.String()), atomID+",binary", // TODO: Find out other supported atom type.
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := storage.do(req)
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	default:
		err := unexpectedStatusCode(resp)
		if err2 := resp.Body.Close(); err2 != nil {
			err = errors.Join(err, fmt.Errorf("close response body: %w", err2))
		}
		return nil, err
	}
}

// lock sends a request to hold a 'lock' to ensure that no other devices are
// using this storage.
func (storage *Storage) lock(ctx context.Context) error {
	// won't work unless we have methods to sign in as windows device/title
	return nil

	requestURL := endpointURL.JoinPath(
		"/connectedstorage/users/xuid("+storage.client.userInfo.XUID+")",
		"scids", strings.ToUpper(storage.serviceConfigID.String()),
		"lock",
	)
	q := requestURL.Query()
	q.Set("friendlyName", storage.client.friendlyName)
	requestURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}

	resp, err := storage.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	default:
		return unexpectedStatusCode(resp)
	}
}

func unexpectedStatusCode(resp *http.Response) error {
	return fmt.Errorf("%s %s: %s", resp.Request.Method, resp.Request.URL, resp.Status)
}

func (storage *Storage) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("x-xbl-pfn", storage.packageFamilyName)
	req.Header.Set("x-xbl-lock-ext", storage.client.lockExt)
	req.Header.Set("x-xbl-lock-ver", "1")
	return storage.client.client.Do(nsal.WithoutAuthHeaders(req, "Signature"))
}

func (storage *Storage) CloseContext(ctx context.Context) error {
	select {
	case <-storage.closed:
		return net.ErrClosed
	default:
	}

	if err := storage.unlock(ctx); err != nil {
		return err
	}
	storage.once.Do(func() {
		close(storage.closed)
	})
	return nil
}

func (storage *Storage) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return storage.CloseContext(ctx)
}

// unlock sends a request to delete a 'lock' currently held on this storage.
// This allows other devices logged in with the same user to start using this storage.
func (storage *Storage) unlock(ctx context.Context) error {
	return nil

	requestURL := endpointURL.JoinPath(
		"/connectedstorage/users/xuid("+storage.client.userInfo.XUID+")",
		"scids", storage.serviceConfigID.String(),
		"lock",
	)
	q := requestURL.Query()
	// TODO: Set this from storage.newSavesUploaded when we support uploading containers to the storage.
	q.Set("newSavesUploaded", "false")
	requestURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}

	resp, err := storage.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	default:
		return unexpectedStatusCode(resp)
	}
}

var endpointURL = &url.URL{
	Scheme: "https",
	Host:   "titlestorage.xboxlive.com",
}
