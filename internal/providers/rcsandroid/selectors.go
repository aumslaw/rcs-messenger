package rcsandroid

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// Selectors say how to find things on the Google Messages screen. The
// defaults come from how the app is described (resource ids from the open
// source Messages layouts, texts from the app's own wording) and have to be
// checked once against a real phone: run `go run ./cmd/androidprobe calibrate`
// on it, then override whatever differs with RCS_ANDROID_SELECTORS (a JSON
// file holding only the keys to change). Every pattern is a case-insensitive
// regular expression. A wrong selector can only make a chat read as "not
// encrypted", which refuses the send. It can never cause a send.
type Selectors struct {
	// Package is the Google Messages package name.
	Package string `json:"package"`
	// ComposeID is the resource id of the message box.
	ComposeID string `json:"composeId"`
	// SendID and SendDesc find the send button (either one matches).
	SendID   string `json:"sendId"`
	SendDesc string `json:"sendDesc"`
	// HintSMS and HintRCS match the message box's hint when it is empty:
	// "Text message" for SMS, "RCS message" for RCS.
	HintSMS string `json:"hintSms"`
	HintRCS string `json:"hintRcs"`
	// Banner matches the thread line that says the chat is end-to-end encrypted.
	Banner string `json:"banner"`
	// Lock matches the resource id or description of the padlock that sits
	// in the message box area of an encrypted chat.
	Lock string `json:"lock"`
	// SendEncrypted matches the send button's description once text is typed
	// when the app will send end-to-end encrypted ("Send encrypted message" on a
	// real phone). This is the final gate before the tap: the button is the
	// app's own statement of what this send will be, it is in the compose row,
	// and message text cannot forge it.
	SendEncrypted string `json:"sendEncrypted"`
	// EncLabel matches the end of a message's own description when the app
	// labels that message end-to-end encrypted ("..., Read. End-to-end encrypted
	// message"). It must be anchored to the END: the description is
	// "<who> said <text> <time> ., <status>. <label>", so the message text can
	// never be at the end. Used only to decide a long thread is worth typing
	// into (the thread-start lock scrolls away); the send button decides.
	EncLabel string `json:"encLabel"`
	// Status* match the small status text under our outgoing message.
	StatusSent    string `json:"statusSent"`
	StatusFailed  string `json:"statusFailed"`
	StatusSending string `json:"statusSending"`
	// BubbleID is the resource id of a message's text in the thread.
	BubbleID string `json:"bubbleId"`
	// MessageContentID is the wrapper around one message. Its description reads
	// like "You said <text> ., Now, SMS": the tail names the protocol the
	// message went out on (seen on a real Messages screen: ", SMS").
	MessageContentID string `json:"messageContentId"`
	MsgSMS           string `json:"msgSms"`
	MsgRCS           string `json:"msgRcs"`
	MsgEncrypted     string `json:"msgEncrypted"`
	// SendSMS matches the send button's description when it sends as SMS
	// ("Send SMS"); the button keeps that label after text is typed, when the
	// message box hint is hidden.
	SendSMS string `json:"sendSms"`
	// TitleID is the resource id of the chat's title in an open thread (the
	// contact name or the number).
	TitleID string `json:"titleId"`
	// The conversation list: one row per chat, with its name, last message
	// and time. Used to find chats that have something new.
	ConvNameID    string `json:"convNameId"`
	ConvSnippetID string `json:"convSnippetId"`
	ConvTimeID    string `json:"convTimeId"`
}

func DefaultSelectors() Selectors {
	return Selectors{
		Package:   "com.google.android.apps.messaging",
		ComposeID: "compose_message_text",
		SendID:    "send_message_button_icon",
		SendDesc:  `^send\b`,
		HintSMS:   `^text message$`,
		HintRCS:   `^rcs message$`,
		// Banner is supporting evidence only (see ReadChat): it never makes a chat
		// encrypted on its own, because message text can contain the same words.
		Banner: `this chat is now end-to-end encrypted`,
		// Lock must match the padlock node itself (real phone: description
		// "Lock icon"), anchored so it cannot match message text or a message
		// label such as "..., End-to-end encrypted message".
		Lock:          `^(lock icon|padlock|lock)$|(^|_)lock(_icon)?$`,
		SendEncrypted: `^send encrypted message$`,
		EncLabel:      `\., (?:[^.,]*\. )?end-to-end encrypted message$`,
		StatusSent:    `^(sent|delivered|read)\b`,
		StatusFailed:  `(not sent|failed|tap to try again|couldn.t send|not delivered)`,
		StatusSending: `^(sending|now sending|queued)`,
		BubbleID:      "message_text",

		MessageContentID: "message_content",
		MsgSMS:           `\b(sms|mms)\b`,
		MsgRCS:           `\brcs\b`,
		MsgEncrypted:     `encrypt`,
		SendSMS:          `\b(sms|mms)\b`,

		TitleID:       "conversation_title",
		ConvNameID:    "conversation_name",
		ConvSnippetID: "conversation_snippet",
		ConvTimeID:    "conversation_timestamp",
	}
}

// LoadSelectors returns the defaults with any keys in the JSON file at path
// laid over them. An empty path means the defaults.
func LoadSelectors(path string) (Selectors, error) {
	sel := DefaultSelectors()
	if path == "" {
		return sel, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return sel, fmt.Errorf("reading selectors: %w", err)
	}
	if err := json.Unmarshal(raw, &sel); err != nil {
		return sel, fmt.Errorf("selectors file %s is not valid JSON: %w", path, err)
	}
	return sel, nil
}

// matcher is Selectors with its patterns compiled.
type matcher struct {
	Selectors
	sendDesc, hintSMS, hintRCS, banner, lock, sent, failed, sending, msgSMS, msgRCS, msgEnc, sendSMS, sendEnc, encLabel *regexp.Regexp
}

func newMatcher(s Selectors) (*matcher, error) {
	m := &matcher{Selectors: s}
	for _, p := range []struct {
		dst  **regexp.Regexp
		name string
		expr string
	}{
		{&m.sendDesc, "sendDesc", s.SendDesc}, {&m.hintSMS, "hintSms", s.HintSMS}, {&m.hintRCS, "hintRcs", s.HintRCS},
		{&m.banner, "banner", s.Banner}, {&m.lock, "lock", s.Lock}, {&m.sent, "statusSent", s.StatusSent},
		{&m.failed, "statusFailed", s.StatusFailed}, {&m.sending, "statusSending", s.StatusSending},
		{&m.msgSMS, "msgSms", s.MsgSMS}, {&m.msgRCS, "msgRcs", s.MsgRCS}, {&m.msgEnc, "msgEncrypted", s.MsgEncrypted},
		{&m.sendSMS, "sendSms", s.SendSMS}, {&m.sendEnc, "sendEncrypted", s.SendEncrypted}, {&m.encLabel, "encLabel", s.EncLabel},
	} {
		re, err := regexp.Compile("(?i)" + p.expr)
		if err != nil {
			return nil, fmt.Errorf("selector %s: %w", p.name, err)
		}
		*p.dst = re
	}
	return m, nil
}
