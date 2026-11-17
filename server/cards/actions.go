// SPDX-License-Identifier: AGPL-3.0-only

package cards

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
)

const maxCommentLen = 10000

// dialogState travels through the comment dialog.
type dialogState struct {
	Ref  string `json:"r"`
	To   string `json:"t,omitempty"`
	Post string `json:"p"`
}

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// cardPost loads the post a button or dialog belongs to and checks that userID may read it and
// that it shows ref. The returned message id is for the user when the check fails.
func (f *Feature) cardPost(userID, postID, ref string) (*model.Post, string) {
	if !model.IsValidId(postID) || !validRef.MatchString(ref) {
		return nil, i18n.M("cards.err.card_invalid")
	}
	post, appErr := f.a.API.GetPost(postID)
	if appErr != nil || post.DeleteAt != 0 {
		return nil, i18n.M("cards.err.post_gone")
	}
	if !f.a.API.HasPermissionToChannel(userID, post.ChannelId, model.PermissionReadChannelContent) {
		return nil, i18n.M("cards.err.no_channel")
	}
	if !slices.Contains(refsOf(post), strings.ToLower(ref)) {
		return nil, i18n.M("cards.err.out_of_date")
	}
	return post, ""
}

// action handles a click on a card button. It acts with the clicking person's own token.
func (f *Feature) action(w http.ResponseWriter, r *http.Request) {
	userID := api.UserID(r)
	var req model.PostActionIntegrationRequest
	if err := decode(r, &req); err != nil || req.UserId != userID {
		api.WriteError(w, r, http.StatusBadRequest, "bad action request", err)
		return
	}
	l := f.a.Locale(userID)
	reply := func(text string) {
		api.WriteJSON(w, r, http.StatusOK, model.PostActionIntegrationResponse{EphemeralText: text})
	}
	ref, _ := req.Context["ref"].(string)
	act, _ := req.Context["action"].(string)
	to, _ := req.Context["to"].(string)
	ref = strings.ToLower(ref)
	if (act != actTransition && act != actAssign && act != actComment) || (act == actTransition && (to == "" || len(to) > 64)) {
		reply(l.T("cards.err.button_invalid"))
		return
	}

	rec := plugin.MakeAuditRecordWithContext("tixCardAction", model.AuditStatusFail, api.PluginContext(r), userID, r.URL.Path)
	defer f.a.API.LogAuditRec(rec)
	rec.AddMeta("action", act)

	post, msgID := f.cardPost(userID, req.PostId, ref)
	if post == nil {
		reply(l.T(msgID))
		return
	}
	rec.AddMeta("post_id", post.Id)
	rec.AddMeta("ref", ref)

	tc, link, err := f.a.TixFor(userID)
	if err != nil {
		reply(errText(l, err))
		return
	}
	ctx, cancel := app.Ctx()
	defer cancel()

	switch act {
	case actComment:
		if err := f.openDialog(l, req.TriggerId, post.Id, ref, "", nil); err != nil {
			reply(l.T("cards.err.dialog"))
			api.Log(r).WithError(err).Warnf("opening the comment dialog failed")
			return
		}
		rec.Success()
		api.WriteJSON(w, r, http.StatusOK, model.PostActionIntegrationResponse{})
		return
	case actAssign:
		t, err := getTask(ctx, tc, ref)
		if err != nil {
			reply(errText(l, err))
			return
		}
		if t.AssigneeActorID == link.ActorID {
			reply(l.T("cards.action.already_yours", "ref", t.Ref))
			return
		}
		if _, err := assignTask(ctx, tc, ref, link.ActorID); err != nil {
			reply(errText(l, err))
			return
		}
	case actTransition:
		t, err := getTask(ctx, tc, ref)
		if err != nil {
			reply(errText(l, err))
			return
		}
		wf, _, err := f.a.WorkflowFor(userID, t.ProjectID)
		if err != nil {
			reply(errText(l, err))
			return
		}
		var allowed, needsComment bool
		for _, tr := range wf.NextStates(t.Status) {
			if tr.To == to {
				allowed = true
				needsComment = needsComment || tr.RequiresComment
			}
		}
		if !allowed {
			reply(l.T("cards.action.cannot_move", "ref", t.Ref, "state", stateLabel(wf, t.Status), "to", stateLabel(wf, to)))
			f.refreshAsync(ref, "")
			return
		}
		if needsComment {
			label := stateLabel(wf, to)
			if err := f.openDialog(l, req.TriggerId, post.Id, ref, to, &label); err != nil {
				reply(l.T("cards.err.dialog"))
				api.Log(r).WithError(err).Warnf("opening the transition dialog failed")
				return
			}
			rec.Success()
			api.WriteJSON(w, r, http.StatusOK, model.PostActionIntegrationResponse{})
			return
		}
		if _, err := transitionTask(ctx, tc, ref, to, ""); err != nil {
			reply(errText(l, err))
			return
		}
	}
	rec.Success()
	resp := model.PostActionIntegrationResponse{Update: f.redraw(post)}
	f.refreshAsync(ref, post.Id)
	api.WriteJSON(w, r, http.StatusOK, resp)
}

// openDialog asks for a comment, alone or to go with a move to state to (labelled toLabel), in l's language.
func (f *Feature) openDialog(l *i18n.Localizer, triggerID, postID, ref, to string, toLabel *string) error {
	state, err := json.Marshal(dialogState{Ref: ref, To: to, Post: postID})
	if err != nil {
		return err
	}
	dlg := model.Dialog{
		CallbackId:  "tix-card",
		Title:       l.T("cards.dialog.comment_title", "ref", ref),
		SubmitLabel: l.T("cards.dialog.comment_submit"),
		State:       string(state),
		Elements: []model.DialogElement{{
			DisplayName: l.T("cards.dialog.comment_field"), Name: "comment", Type: "textarea", MaxLength: maxCommentLen,
		}},
	}
	if toLabel != nil {
		dlg.Title = l.T("cards.dialog.move_title", "ref", ref, "state", *toLabel)
		dlg.IntroductionText = l.T("cards.dialog.move_intro")
		dlg.SubmitLabel = l.T("cards.dialog.move_submit")
	}
	if utf8.RuneCountInString(dlg.Title) > model.DialogTitleMaxLength {
		dlg.Title = string([]rune(dlg.Title)[:model.DialogTitleMaxLength-3]) + "..."
	}
	if appErr := f.a.API.OpenInteractiveDialog(model.OpenDialogRequest{
		TriggerId: triggerID,
		URL:       f.a.PluginURL(dialogPath),
		Dialog:    dlg,
	}); appErr != nil {
		return appErr
	}
	return nil
}

// dialogSubmit comments on a task, or moves it with a comment, as the person who submitted.
func (f *Feature) dialogSubmit(w http.ResponseWriter, r *http.Request) {
	userID := api.UserID(r)
	var req model.SubmitDialogRequest
	if err := decode(r, &req); err != nil || req.UserId != userID {
		api.WriteError(w, r, http.StatusBadRequest, "bad dialog submission", err)
		return
	}
	if req.Cancelled {
		api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{})
		return
	}
	l := f.a.Locale(userID)
	fail := func(text string) {
		api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{Error: text})
	}
	var st dialogState
	if err := json.Unmarshal([]byte(req.State), &st); err != nil || len(st.To) > 64 {
		fail(l.T("cards.err.dialog_invalid"))
		return
	}
	st.Ref = strings.ToLower(st.Ref)
	comment, _ := req.Submission["comment"].(string)
	comment = strings.TrimSpace(comment)
	if comment == "" || len(comment) > maxCommentLen {
		api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{Errors: map[string]string{"comment": l.T("cards.dialog.write_comment")}})
		return
	}

	rec := plugin.MakeAuditRecordWithContext("tixCardDialog", model.AuditStatusFail, api.PluginContext(r), userID, r.URL.Path)
	defer f.a.API.LogAuditRec(rec)

	post, msgID := f.cardPost(userID, st.Post, st.Ref)
	if post == nil {
		fail(l.T(msgID))
		return
	}
	rec.AddMeta("post_id", post.Id)
	rec.AddMeta("ref", st.Ref)
	tc, _, err := f.a.TixFor(userID)
	if err != nil {
		fail(errText(l, err))
		return
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	if st.To != "" {
		rec.AddMeta("action", actTransition)
		_, err = transitionTask(ctx, tc, st.Ref, st.To, comment)
	} else {
		rec.AddMeta("action", actComment)
		_, err = tc.AddComment(ctx, st.Ref, comment)
	}
	if err != nil {
		fail(errText(l, err))
		return
	}
	rec.Success()
	if updated := f.redraw(post); updated != nil {
		if _, appErr := f.a.API.UpdatePost(updated); appErr != nil {
			api.Log(r).WithError(appErr).Warnf("redrawing a task card failed")
		}
	}
	f.refreshAsync(st.Ref, post.Id)
	if st.To == "" {
		f.a.Ephemeral(userID, post.ChannelId, post.RootId, l.T("cards.dialog.commented", "ref", st.Ref))
	}
	api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{})
}
