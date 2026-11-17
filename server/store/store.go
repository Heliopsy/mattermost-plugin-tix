// SPDX-License-Identifier: AGPL-3.0-only

// Package store keeps the plugin's records in the Mattermost KV store. Nothing
// else builds keys or decodes values.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi"
)

// Link ties a Mattermost user to a tix actor through that person's own token.
type Link struct {
	UserID    string `json:"user_id"`
	ActorID   string `json:"actor_id"`
	Handle    string `json:"handle"`
	Email     string `json:"email"`
	TenantKey string `json:"tenant_key"`
	TokenEnc  string `json:"token_enc"`
	TokenID   string `json:"token_id"`
	// Minted is true when the plugin issued the token (and so may revoke it).
	Minted     bool      `json:"minted"`
	LinkedAt   time.Time `json:"linked_at"`
	LastUsedAt time.Time `json:"last_used_at,omitzero"`
}

// Subscription posts tix events about one project (or all, when Project is "") into one channel.
type Subscription struct {
	ChannelID string    `json:"channel_id"`
	Project   string    `json:"project"`
	Events    []string  `json:"events"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	// Detail, Comments and People override the feed formatting settings for this feed (compact,
	// detailed or card; full, excerpt or none; names or mentions). Empty, as in records saved
	// before they existed, keeps the System Console setting.
	Detail   string `json:"detail,omitempty"`
	Comments string `json:"comments,omitempty"`
	People   string `json:"people,omitempty"`
}

// CardRef is one post that shows a task card, kept so the card can be redrawn when the task changes.
type CardRef struct {
	PostID    string    `json:"post_id"`
	ChannelID string    `json:"channel_id"`
	CreatedAt time.Time `json:"created_at"`
	// RootID is the thread the card post is in (the post itself when it is a root).
	RootID string `json:"root_id,omitempty"`
	// SourceID is the person's post a bot card answers; empty for a card drawn on the person's own post.
	SourceID string `json:"source_id,omitempty"`
}

// Mirror is a tix comment the plugin wrote from a Mattermost reply (task-cards thread sync). notify
// consults it so the comment is not posted back into the channel it came from.
type Mirror struct {
	ChannelID string `json:"channel_id"`
	RootID    string `json:"root_id"`
	PostID    string `json:"post_id"`
}

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

const (
	linkPrefix     = "ln_"  // ln_<mm user id> -> Link
	actorPrefix    = "la_"  // la_<tix actor id> -> mm user id
	subsKey        = "subs" // all subscriptions
	cardPrefix     = "cd_"  // cd_<task ref> -> []CardRef
	mirrorPrefix   = "mc_"  // mc_<comment id> or mc_<hash of ref, actor, body> -> Mirror
	maxCardsPerRef = 50
	mirrorTTL      = 24 * time.Hour
)

// Store reads and writes plugin records.
type Store struct {
	kv  *pluginapi.KVService
	key func() string
}

// New returns a store. encryptionKey is read on every use so a changed setting applies at once.
func New(kv *pluginapi.KVService, encryptionKey func() string) *Store {
	return &Store{kv: kv, key: encryptionKey}
}

// GetLink returns the link of a Mattermost user, or ErrNotFound.
func (s *Store) GetLink(userID string) (*Link, error) {
	var l Link
	if err := s.kv.Get(linkPrefix+userID, &l); err != nil {
		return nil, fmt.Errorf("reading link: %w", err)
	}
	if l.UserID == "" {
		return nil, ErrNotFound
	}
	return &l, nil
}

// UserForActor returns the Mattermost user linked to a tix actor, or ErrNotFound.
func (s *Store) UserForActor(actorID string) (string, error) {
	var userID string
	if err := s.kv.Get(actorPrefix+actorID, &userID); err != nil {
		return "", fmt.Errorf("reading actor link: %w", err)
	}
	if userID == "" {
		return "", ErrNotFound
	}
	return userID, nil
}

// SaveLink stores a link with token encrypted, replacing any earlier link of the user.
func (s *Store) SaveLink(l *Link, token string) error {
	enc, err := s.encrypt(token)
	if err != nil {
		return err
	}
	if old, err := s.GetLink(l.UserID); err == nil && old.ActorID != l.ActorID {
		_ = s.kv.Delete(actorPrefix + old.ActorID)
	}
	l.TokenEnc = enc
	if _, err := s.kv.Set(linkPrefix+l.UserID, l); err != nil {
		return fmt.Errorf("saving link: %w", err)
	}
	if _, err := s.kv.Set(actorPrefix+l.ActorID, l.UserID); err != nil {
		return fmt.Errorf("saving actor link: %w", err)
	}
	return nil
}

// DeleteLink removes a user's link.
func (s *Store) DeleteLink(userID string) error {
	l, err := s.GetLink(userID)
	if errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	_ = s.kv.Delete(actorPrefix + l.ActorID)
	return s.kv.Delete(linkPrefix + userID)
}

// Token decrypts the token of a link.
func (s *Store) Token(l *Link) (string, error) {
	return s.decrypt(l.TokenEnc)
}

// ListLinks returns every link (for admin status and notification fan-out).
func (s *Store) ListLinks() ([]Link, error) {
	var out []Link
	for page := 0; ; page++ {
		keys, err := s.kv.ListKeys(page, 200, pluginapi.WithPrefix(linkPrefix))
		if err != nil {
			return nil, fmt.Errorf("listing links: %w", err)
		}
		for _, k := range keys {
			var l Link
			if err := s.kv.Get(k, &l); err == nil && l.UserID != "" {
				out = append(out, l)
			}
		}
		if len(keys) < 200 {
			return out, nil
		}
	}
}

// Subscriptions returns every channel subscription.
func (s *Store) Subscriptions() ([]Subscription, error) {
	var subs []Subscription
	if err := s.kv.Get(subsKey, &subs); err != nil {
		return nil, fmt.Errorf("reading subscriptions: %w", err)
	}
	return subs, nil
}

// SaveSubscriptions replaces the subscription list atomically against concurrent writers.
func (s *Store) SaveSubscriptions(update func([]Subscription) []Subscription) error {
	for range 5 {
		var raw []byte
		if err := s.kv.Get(subsKey, &raw); err != nil {
			return fmt.Errorf("reading subscriptions: %w", err)
		}
		var cur []Subscription
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &cur); err != nil {
				return fmt.Errorf("decoding subscriptions: %w", err)
			}
		}
		next := update(cur)
		ok, err := s.kv.Set(subsKey, next, pluginapi.SetAtomic(raw))
		if err != nil {
			return fmt.Errorf("saving subscriptions: %w", err)
		}
		if ok {
			return nil
		}
	}
	return errors.New("saving subscriptions: too much contention")
}

// Cards returns the posts showing a task card.
func (s *Store) Cards(ref string) ([]CardRef, error) {
	var cards []CardRef
	if err := s.kv.Get(cardPrefix+strings.ToLower(ref), &cards); err != nil {
		return nil, fmt.Errorf("reading cards: %w", err)
	}
	return cards, nil
}

// AddCard remembers that a post shows a card for ref. The oldest are dropped past a cap.
func (s *Store) AddCard(ref string, c CardRef) error {
	key := cardPrefix + strings.ToLower(ref)
	for range 5 {
		var raw []byte
		if err := s.kv.Get(key, &raw); err != nil {
			return err
		}
		var cards []CardRef
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &cards)
		}
		for _, e := range cards {
			if e.PostID == c.PostID {
				return nil
			}
		}
		cards = append(cards, c)
		if len(cards) > maxCardsPerRef {
			cards = cards[len(cards)-maxCardsPerRef:]
		}
		ok, err := s.kv.Set(key, cards, pluginapi.SetAtomic(raw))
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errors.New("saving card: too much contention")
}

// RemoveCard forgets a post.
func (s *Store) RemoveCard(ref, postID string) error {
	cards, err := s.Cards(ref)
	if err != nil {
		return err
	}
	out := cards[:0]
	for _, c := range cards {
		if c.PostID != postID {
			out = append(out, c)
		}
	}
	_, err = s.kv.Set(cardPrefix+strings.ToLower(ref), out)
	return err
}

// mirrorBodyKey names a mirrored comment before tix has given it an id.
func mirrorBodyKey(ref, actorID, body string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(ref) + "\x00" + actorID + "\x00" + strings.TrimSpace(body)))
	return mirrorPrefix + "b" + base64.RawURLEncoding.EncodeToString(sum[:18])
}

// MarkMirrored records a comment the plugin is writing to tix from a Mattermost post. Call it before
// the comment is created (commentID "") so a fast webhook already finds it, and again with the id.
func (s *Store) MarkMirrored(ref, actorID, body, commentID string, m Mirror) error {
	key := mirrorBodyKey(ref, actorID, body)
	if commentID != "" {
		key = mirrorPrefix + commentID
	}
	_, err := s.kv.Set(key, m, pluginapi.SetExpiry(mirrorTTL))
	return err
}

// MirroredComment returns where a tix comment came from when the plugin wrote it from a Mattermost
// post, or nil. It looks the comment up by id, then by task, author and text.
func (s *Store) MirroredComment(ref, actorID, body, commentID string) *Mirror {
	keys := []string{}
	if commentID != "" {
		keys = append(keys, mirrorPrefix+commentID)
	}
	if body != "" {
		keys = append(keys, mirrorBodyKey(ref, actorID, body))
	}
	for _, k := range keys {
		var m Mirror
		if err := s.kv.Get(k, &m); err == nil && m.ChannelID != "" {
			return &m
		}
	}
	return nil
}

// Get and Set give features a namespaced place for small records of their own.
// Keys are prefixed with "x_" plus the feature name.
func (s *Store) Get(feature, key string, out any) error {
	return s.kv.Get("x_"+feature+"_"+key, out)
}

// Set stores a feature record; a nil value deletes it.
func (s *Store) Set(feature, key string, v any, opts ...pluginapi.KVSetOption) (bool, error) {
	return s.kv.Set("x_"+feature+"_"+key, v, opts...)
}

func (s *Store) gcm() (cipher.AEAD, error) {
	k := s.key()
	if k == "" {
		return nil, errors.New("the plugin's encryption key is not set; an admin must generate it in the System Console")
	}
	sum := sha256.Sum256([]byte(k))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Store) encrypt(plain string) (string, error) {
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Store) decrypt(enc string) (string, error) {
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < g.NonceSize() {
		return "", errors.New("stored token is unreadable; reconnect with /tix connect")
	}
	plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	if err != nil {
		return "", errors.New("stored token cannot be decrypted (was the encryption key regenerated?); reconnect with /tix connect")
	}
	return string(plain), nil
}
