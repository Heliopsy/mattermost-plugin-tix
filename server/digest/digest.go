// SPDX-License-Identifier: AGPL-3.0-only

// Package digest sends each person who asks for it a daily DM summary of their tix tasks and
// reminders when a task assigned to them falls due or becomes overdue. Every tix call acts as
// that person through their own token; one cluster-wide job does the scheduling.
package digest

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
)

const (
	feature    = "digest"
	indexKey   = "users" // x_digest_users -> []user id with digest or reminders on
	userPrefix = "u_"    // x_digest_u_<user id> -> record
	claimPre   = "rm_"   // x_digest_rm_<user>_<task>_<kind>_<due unix> -> true, with expiry

	defaultAt = "09:00"
)

// record is one person's choices and schedule state. The zero value is "everything off".
type record struct {
	Digest    bool   `json:"digest,omitempty"`
	At        string `json:"at,omitempty"`    // HH:MM in the person's Mattermost time zone
	Daily     bool   `json:"daily,omitempty"` // every day instead of Monday to Friday
	Quiet     bool   `json:"quiet,omitempty"` // skip a digest with nothing to report
	Reminders bool   `json:"reminders,omitempty"`

	// NextRun is the next morning run (digest, due-today reminders); NextCheck the next
	// overdue check. Both are absolute instants so a restart or a missed tick changes nothing.
	NextRun    time.Time `json:"next_run,omitzero"`
	NextCheck  time.Time `json:"next_check,omitzero"`
	RetryAt    time.Time `json:"retry_at,omitzero"`
	LastDigest time.Time `json:"last_digest,omitzero"`
	// Seen holds the ids of the open tasks assigned at the last digest, to tell what is new.
	Seen     []string `json:"seen,omitempty"`
	Baseline bool     `json:"baseline,omitempty"`
}

func (r *record) at() string {
	if r.At == "" {
		return defaultAt
	}
	return r.At
}

func (r *record) active() bool { return r.Digest || r.Reminders }

// Feature is the digest and reminders feature.
type Feature struct {
	a   *app.App
	now func() time.Time
	job *cluster.Job
}

// New returns the feature.
func New(a *app.App) *Feature { return &Feature{a: a, now: time.Now} }

// Routes: the feature has none.
func (f *Feature) Routes(*api.Handler) {}

// Start schedules the cluster-wide tick: one node runs it at a time, once a minute.
func (f *Feature) Start(jobAPI cluster.JobPluginAPI) error {
	job, err := cluster.Schedule(jobAPI, "tix_digest_tick", cluster.MakeWaitForRoundedInterval(time.Minute), f.Tick)
	if err != nil {
		return fmt.Errorf("scheduling the digest job: %w", err)
	}
	f.job = job
	return nil
}

// Stop ends the job on this node.
func (f *Feature) Stop() error {
	if f.job == nil {
		return nil
	}
	err := f.job.Close()
	f.job = nil
	return err
}

// load reads a person's record and its raw bytes for a compare-and-set.
func (f *Feature) load(userID string) (*record, []byte, error) {
	var raw []byte
	if err := f.a.Store.Get(feature, userPrefix+userID, &raw); err != nil {
		return nil, nil, err
	}
	var r record
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, nil, err
		}
	}
	return &r, raw, nil
}

var errChanged = errors.New("record changed meanwhile")

// update changes a person's record atomically. change returns errChanged to give up; a
// record that ends up inactive is deleted. The index follows the record.
func (f *Feature) update(userID string, change func(r *record) error) (*record, error) {
	for range 5 {
		r, raw, err := f.load(userID)
		if err != nil {
			return nil, err
		}
		if cerr := change(r); cerr != nil {
			return nil, cerr
		}
		var v any = r
		if !r.active() {
			if raw == nil {
				return r, f.setIndexed(userID, false)
			}
			v = nil
		}
		var old any
		if raw != nil {
			old = raw
		}
		ok, err := f.a.Store.Set(feature, userPrefix+userID, v, pluginapi.SetAtomic(old))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if err := f.setIndexed(userID, r.active()); err != nil {
			return nil, err
		}
		return r, nil
	}
	return nil, errors.New("saving digest settings: too much contention")
}

// index lists the people with a digest or reminders on, so a tick reads only them.
func (f *Feature) index() ([]string, error) {
	var ids []string
	if err := f.a.Store.Get(feature, indexKey, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (f *Feature) setIndexed(userID string, on bool) error {
	for range 5 {
		var raw []byte
		if err := f.a.Store.Get(feature, indexKey, &raw); err != nil {
			return err
		}
		var ids []string
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &ids); err != nil {
				return err
			}
		}
		has := slices.Contains(ids, userID)
		if has == on {
			return nil
		}
		if on {
			ids = append(ids, userID)
		} else {
			ids = slices.DeleteFunc(ids, func(s string) bool { return s == userID })
		}
		var old any
		if raw != nil {
			old = raw
		}
		ok, err := f.a.Store.Set(feature, indexKey, ids, pluginapi.SetAtomic(old))
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errors.New("saving the digest index: too much contention")
}

// forget drops a person who is no longer linked.
func (f *Feature) forget(userID string) {
	if _, err := f.a.Store.Set(feature, userPrefix+userID, nil); err != nil {
		f.a.API.LogWarn("tix digest: dropping an unlinked user", "user_id", userID, "err", err.Error())
	}
	if err := f.setIndexed(userID, false); err != nil {
		f.a.API.LogWarn("tix digest: dropping an unlinked user from the index", "user_id", userID, "err", err.Error())
	}
}

// claim records that a reminder went out, once per user, task, kind and due date. It reports
// whether this caller is the first; the record expires after ttl.
func (f *Feature) claim(userID, taskID, kind string, due time.Time, ttl time.Duration) bool {
	key := fmt.Sprintf("%s%s_%s_%s_%d", claimPre, userID, taskID, kind, due.Unix())
	ok, err := f.a.Store.Set(feature, key, true, pluginapi.SetAtomic(nil), pluginapi.SetExpiry(ttl))
	if err != nil {
		f.a.API.LogWarn("tix digest: recording a reminder", "user_id", userID, "err", err.Error())
		return false
	}
	return ok
}
