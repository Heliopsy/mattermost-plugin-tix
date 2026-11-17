// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))))
	return b.Bytes()
}

func TestBotDisplayName(t *testing.T) {
	assert.Equal(t, "tix", botDisplayName("  "))
	assert.Equal(t, "Task bot", botDisplayName(" Task   bot "))
	assert.Equal(t, 64, len([]rune(botDisplayName(strings.Repeat("я", 80)))))
}

func TestIconFetcher(t *testing.T) {
	small, big := pngOf(t, 64, 64), pngOf(t, 2000, 10)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/icon.png":
			_, _ = w.Write(small)
		case "/big.png":
			_, _ = w.Write(big)
		case "/huge.png":
			_, _ = w.Write(append(append([]byte{}, small...), make([]byte, maxIconBytes)...))
		case "/icon.gif":
			_, _ = w.Write([]byte("GIF89a......"))
		case "/away":
			http.Redirect(w, r, "http://example.com/icon.png", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	local := &iconFetcher{allowPrivate: true, tlsConfig: &tls.Config{RootCAs: srvPool(srv), MinVersion: tls.VersionTLS12}}

	data, err := local.fetch(srv.URL + "/icon.png")
	require.NoError(t, err)
	assert.Equal(t, small, data)
	for path, why := range map[string]string{
		"/big.png": "too many pixels", "/huge.png": "too many bytes", "/icon.gif": "not a PNG",
		"/missing": "not found", "/away": "redirect off https",
	} {
		_, ferr := local.fetch(srv.URL + path)
		assert.Error(t, ferr, why)
	}
	_, err = local.fetch(strings.Replace(srv.URL, "https:", "http:", 1) + "/icon.png")
	assert.Error(t, err, "plain http")

	// The real fetcher never dials a loopback or private address.
	_, err = (&iconFetcher{tlsConfig: local.tlsConfig}).fetch(srv.URL + "/icon.png")
	assert.ErrorIs(t, err, errPrivateAddress)
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "192.168.42.2", "169.254.169.254", "::1", "fd00::1", "0.0.0.0"} {
		assert.False(t, publicIP(net.ParseIP(ip)), ip)
	}
	assert.True(t, publicIP(net.ParseIP("1.1.1.1")))
}

func srvPool(srv *httptest.Server) *x509.CertPool {
	return srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
}
