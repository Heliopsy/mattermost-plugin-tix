// SPDX-License-Identifier: AGPL-3.0-only

package link

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
)

// A pasted token's whoami names the token (tix answers for the credential), so the link must take
// the person's handle from their actor record, or @mentions of them never reach them.
func TestLinkWithTokenStoresThePersonsHandle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "act-carol", "handle": "carol-pasted", "kind": "agent", "token_id": "tok-1", "tenant_name": "Default"})
		case "/api/v1/actors/act-carol":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "act-carol", "handle": "carol", "display_name": "Carol Cole", "kind": "user"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	papi := &plugintest.API{}
	var mu sync.Mutex
	kv := map[string][]byte{}
	papi.On("KVGet", mock.Anything).Return(func(key string) ([]byte, *model.AppError) {
		mu.Lock()
		defer mu.Unlock()
		return kv[key], nil
	}).Maybe()
	papi.On("KVSetWithOptions", mock.Anything, mock.Anything, mock.Anything).Return(func(key string, v []byte, _ model.PluginKVSetOptions) (bool, *model.AppError) {
		mu.Lock()
		defer mu.Unlock()
		kv[key] = v
		return true, nil
	}).Maybe()
	client := pluginapi.NewClient(papi, nil)
	st := store.New(&client.KV, func() string { return "test-key" })
	a := app.New(papi, client, st, "bot", func() *app.Config { return &app.Config{TixURL: srv.URL, EncryptionKey: "test-key"} })

	userID := model.NewId()
	msg, err := New(a).linkWithToken(userID, "tix_pasted")
	require.NoError(t, err)
	require.Contains(t, msg, "Carol Cole")
	l, err := st.GetLink(userID)
	require.NoError(t, err)
	require.Equal(t, "carol", l.Handle)
	require.Equal(t, "act-carol", l.ActorID)
	require.Equal(t, "tok-1", l.TokenID)
}
