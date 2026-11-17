// SPDX-License-Identifier: AGPL-3.0-only

package cards

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
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
// that it shows ref. The returned text is for the user when the check fails.
func (f *Feature) cardPost(userID, postID, ref string) (*model.Post, string) {
	if !model.IsValidId(postID) || !validRef.MatchString(ref) {
		return nil, "That card is not valid."
	}
	post, appErr := f.a.API.GetPost(postID)
	if appErr != nil || post.DeleteAt != 0 {
		return nil, "That message no longer exists."
	}
	if !f.a.API.HasPermissionToChannel(userID, post.ChannelId, model.PermissionReadChannelContent) {
		return nil, "You cannot see that channel."
	}
	if !slices.Contains(refsOf(post), strings.ToLower(ref)) {
		return nil, "That card is out of date."
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
	reply := func(text string) {
		api.WriteJSON(w, r, http.StatusOK, model.PostActionIntegrationResponse{EphemeralText: text})
	}
	ref, _ := req.Context["ref"].(string)
	act, _ := req.Context["action"].(string)
	to, _ := req.Context["to"].(string)
	ref = strings.ToLower(ref)
	if (act != actTransition && act != actAssign && act != actComment) || (act == actTransition && (to == "" || len(to) > 64)) {
		reply("That button is not valid.")
		return
	}

	rec := plugin.MakeAuditRecordWithContext("tixCardAction", model.AuditStatusFail, api.PluginContext(r), userID, r.URL.Path)
	defer f.a.API.LogAuditRec(rec)
	rec.AddMeta("action", act)

	post, msg := f.cardPost(userID, req.PostId, ref)
	if post == nil {
		reply(msg)
		return
	}
	rec.AddMeta("post_id", post.Id)
	rec.AddMeta("ref", ref)

	tc, link, err := f.a.TixFor(userID)
	if err != nil {
		reply(errText(err))
		return
	}
	ctx, cancel := app.Ctx()
	defer cancel()

	switch act {
	case actComment:
		if err := f.openDialog(req.TriggerId, post.Id, ref, "", nil); err != nil {
			reply("Could not open the comment dialog.")
			api.Log(r).WithError(err).Warnf("opening the comment dialog failed")
			return
		}
		rec.Success()
		api.WriteJSON(w, r, http.StatusOK, model.PostActionIntegrationResponse{})
		return
	case actAssign:
		t, err := getTask(ctx, tc, ref)
		if err != nil {
			reply(errText(err))
			return
		}
		if t.AssigneeActorID == link.ActorID {
			reply(fmt.Sprintf("%s is already assigned to you.", t.Ref))
			return
		}
		if _, err := assignTask(ctx, tc, ref, link.ActorID); err != nil {
			reply(errText(err))
			return
		}
	case actTransition:
		t, err := getTask(ctx, tc, ref)
		if err != nil {
			reply(errText(err))
			return
		}
		wf, _, err := f.a.WorkflowFor(userID, t.ProjectID)
		if err != nil {
			reply(errText(err))
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
			reply(fmt.Sprintf("%s is %s now and cannot move to %s from there.", t.Ref, stateLabel(wf, t.Status), stateLabel(wf, to)))
			f.refreshAsync(ref, "")
			return
		}
		if needsComment {
			label := stateLabel(wf, to)
			if err := f.openDialog(req.TriggerId, post.Id, ref, to, &label); err != nil {
				reply("Could not open the comment dialog.")
				api.Log(r).WithError(err).Warnf("opening the transition dialog failed")
				return
			}
			rec.Success()
			api.WriteJSON(w, r, http.StatusOK, model.PostActionIntegrationResponse{})
			return
		}
		if _, err := transitionTask(ctx, tc, ref, to, ""); err != nil {
			reply(errText(err))
			return
		}
	}
	rec.Success()
	resp := model.PostActionIntegrationResponse{Update: f.redraw(post)}
	f.refreshAsync(ref, post.Id)
	api.WriteJSON(w, r, http.StatusOK, resp)
}

// openDialog asks for a comment, alone or to go with a move to state to (labelled toLabel).
func (f *Feature) openDialog(triggerID, postID, ref, to string, toLabel *string) error {
	state, err := json.Marshal(dialogState{Ref: ref, To: to, Post: postID})
	if err != nil {
		return err
	}
	dlg := model.Dialog{
		CallbackId:  "tix-card",
		Title:       "Comment on " + ref,
		SubmitLabel: "Comment",
		State:       string(state),
		Elements: []model.DialogElement{{
			DisplayName: "Comment", Name: "comment", Type: "textarea", MaxLength: maxCommentLen,
		}},
	}
	if toLabel != nil {
		dlg.Title = "Move " + ref + " to " + *toLabel
		dlg.IntroductionText = "This move needs a comment."
		dlg.SubmitLabel = "Move"
	}
	if len(dlg.Title) > model.DialogTitleMaxLength {
		dlg.Title = dlg.Title[:model.DialogTitleMaxLength-3] + "..."
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
	fail := func(text string) {
		api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{Error: text})
	}
	var st dialogState
	if err := json.Unmarshal([]byte(req.State), &st); err != nil || len(st.To) > 64 {
		fail("That dialog is not valid.")
		return
	}
	st.Ref = strings.ToLower(st.Ref)
	comment, _ := req.Submission["comment"].(string)
	comment = strings.TrimSpace(comment)
	if comment == "" || len(comment) > maxCommentLen {
		api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{Errors: map[string]string{"comment": "Write a comment."}})
		return
	}

	rec := plugin.MakeAuditRecordWithContext("tixCardDialog", model.AuditStatusFail, api.PluginContext(r), userID, r.URL.Path)
	defer f.a.API.LogAuditRec(rec)

	post, msg := f.cardPost(userID, st.Post, st.Ref)
	if post == nil {
		fail(msg)
		return
	}
	rec.AddMeta("post_id", post.Id)
	rec.AddMeta("ref", st.Ref)
	tc, _, err := f.a.TixFor(userID)
	if err != nil {
		fail(errText(err))
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
		fail(errText(err))
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
		f.a.Ephemeral(userID, post.ChannelId, post.RootId, fmt.Sprintf("Commented on %s.", st.Ref))
	}
	api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{})
}
