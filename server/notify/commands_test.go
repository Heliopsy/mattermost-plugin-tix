// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"encoding/json"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
)

// cmdEnv runs notify's commands as a user with the given Mattermost locale.
func cmdEnv(t *testing.T, locale string, subs []store.Subscription) (*Feature, *command.Ctx) {
	t.Helper()
	api := &plugintest.API{}
	caller, creator := model.NewId(), model.NewId()
	for i := range subs {
		subs[i].CreatedBy = creator
	}
	raw, err := json.Marshal(subs)
	require.NoError(t, err)
	api.On("GetUser", caller).Return(&model.User{Id: caller, Username: "ana", Locale: locale}, nil)
	api.On("GetUser", creator).Return(&model.User{Id: creator, Username: "boris"}, nil)
	api.On("GetConfig").Return(&model.Config{}).Maybe()
	api.On("KVGet", "subs").Return(raw, nil).Maybe()
	api.On("KVGet", mock.Anything).Return(nil, nil).Maybe()
	api.On("HasPermissionTo", caller, model.PermissionManageSystem).Return(false).Maybe()
	client := pluginapi.NewClient(api, nil)
	st := store.New(&client.KV, func() string { return "k" })
	a := app.New(api, client, st, "bot", func() *app.Config { return &app.Config{} })
	return New(a, nil), &command.Ctx{App: a, Args: &model.CommandArgs{UserId: caller, ChannelId: "ch1"}}
}

func TestSubscriptionsCommandLanguage(t *testing.T) {
	subs := []store.Subscription{{ChannelID: "ch1", Project: "work", Events: []string{kindCreated, kindDone}, Detail: "card"}, {ChannelID: "ch1", Events: []string{kindDeleted}}}
	f, c := cmdEnv(t, "en", subs)
	assert.Equal(t, "| Project | Events | Format | By |\n| --- | --- | --- | --- |\n"+
		"| work | created, done | `--detail card` | @boris |\n| all | deleted | default | @boris |\n", f.subscriptions(c, nil).Text)
	f, c = cmdEnv(t, "bg", subs)
	assert.Equal(t, "| Проект | Събития | Формат | От |\n| --- | --- | --- | --- |\n"+
		"| work | created, done | `--detail card` | @boris |\n| all | deleted | по подразбиране | @boris |\n", f.subscriptions(c, nil).Text)
	assert.Equal(t, "Само системни администратори могат да видят абонаментите на всички канали.", f.subscriptions(c, []string{"all"}).Text)

	f, c = cmdEnv(t, "bg", nil)
	assert.Equal(t, "Този канал няма tix абонаменти. `/tix subscribe <project>` добавя такъв.", f.subscriptions(c, nil).Text)
	assert.Contains(t, f.subscribe(c, nil).Text, "Употреба: `/tix subscribe <project|all> [created transitioned assigned commented done deleted]")
	assert.Equal(t, "Личните ти съобщения от tix са **включени**. `/tix notifications on|off` променя това.", f.notifications(c, nil).Text)
	assert.Equal(t, "Първо задай администраторския токен (с `webhook:admin`) в System Console > Plugins > tix.", f.webhookCmd(c, nil).Text)
}

func TestParseKindsError(t *testing.T) {
	_, err := parseKinds([]string{"starred"})
	require.Error(t, err)
	assert.Equal(t, "unknown event `starred`; use created, transitioned, assigned, commented, done, deleted", app.ErrText(i18n.For("en"), err))
	_, c := cmdEnv(t, "bg", nil)
	assert.Equal(t, "непознато събитие `starred`; използвай created, transitioned, assigned, commented, done, deleted", app.ErrText(c.L(), err))
}
