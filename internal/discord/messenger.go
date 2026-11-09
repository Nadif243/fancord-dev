// Package discord is the only place that talks to the discordgo library.
// If the library changes, this is the package to fix.
package discord

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/bwmarrin/discordgo"

	"github.com/Nadif243/fancord-bot/internal/broadcast"
	"github.com/Nadif243/fancord-bot/internal/outside"
	"github.com/Nadif243/fancord-bot/internal/tweetcard"
)

// Discord's error codes for a message or a channel that does not exist.
const (
	codeUnknownChannel = 10003
	codeUnknownMessage = 10008
)

// isGone reports whether Discord answered that the message (or its channel) does not exist, as opposed to any other failure.
func isGone(err error) bool {
	var rest *discordgo.RESTError
	if !errors.As(err, &rest) || rest.Message == nil {
		return false
	}
	return rest.Message.Code == codeUnknownMessage || rest.Message.Code == codeUnknownChannel
}

// SessionMessenger lets the other package send and edit messages without knowing about discordgo.
//
// Every message goes out with explicit mention rules. By default nobody can be mentioned, so text we did not write (a YouTube title containing "@everyone", say) can never ping anyone. Only Ping allows one role.
type SessionMessenger struct {
	S *discordgo.Session
}

// nobody is "mention no one": an empty parse list switches off everything Discord would otherwise find in the text.
func nobody() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func (m SessionMessenger) Send(channelID, content string) (string, error) {
	sent, err := m.S.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: nobody(),
	})
	if err != nil {
		return "", err
	}
	return sent.ID, nil
}

func (m SessionMessenger) Edit(channelID, messageID, content string) error {
	_, err := m.S.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:         channelID,
		ID:              messageID,
		Content:         &content,
		AllowedMentions: nobody(),
	})
	return wrapGone(err)
}

// wrapGone marks a "message does not exist" answer with broadcast.ErrGone and passes every other error (and nil) through unchanged.
func wrapGone(err error) error {
	if isGone(err) {
		return fmt.Errorf("%w: %v", broadcast.ErrGone, err)
	}
	return err
}

// Post sends a message with buttons under it. The buttons are in one row (at most five fit in a row; the review message uses three).
func (m SessionMessenger) Post(channelID, content string, buttons []outside.Button) (string, error) {
	sent, err := m.S.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: nobody(),
		Components:      rowOf(buttons),
	})
	if err != nil {
		return "", err
	}
	return sent.ID, nil
}

func rowOf(buttons []outside.Button) []discordgo.MessageComponent {
	row := discordgo.ActionsRow{}
	for _, b := range buttons {
		style := discordgo.SuccessButton
		if b.Danger {
			style = discordgo.DangerButton
		}
		row.Components = append(row.Components, discordgo.Button{Label: b.Label, Style: style, CustomID: b.CustomID})
	}
	return []discordgo.MessageComponent{row}
}

// Ping sends a message that may mention exactly one role, roleID, and nobody else. The text should contain <@&roleID> for the mention to appear.
func (m SessionMessenger) Ping(channelID, content, roleID string) error {
	_, err := m.S.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: content,
		AllowedMentions: &discordgo.MessageAllowedMentions{
			Parse: []discordgo.AllowedMentionType{},
			Roles: []string{roleID},
		},
	})
	return err
}

// SendCard posts a tweet card: a message built from Discord's newer display components, which discordgo's ordinary message type does not carry. The body is already finished JSON-ready data, so it goes out through the library's plain request call (which still handles rate limits).

// Discord answering 400 means it did not accept the card as built; that is reported as tweetcard.ErrRejected so the caller can send a plain link instead. Any other failure is returned as it is.
func (m SessionMessenger) SendCard(channelID string, card tweetcard.Payload) (string, error) {
	body, err := m.S.Request(http.MethodPost, discordgo.EndpointChannelMessages(channelID), card)
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == http.StatusBadRequest {
			return "", fmt.Errorf("%w: %v", tweetcard.ErrRejected, err)
		}
		return "", err
	}
	var sent struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &sent) // the ID is only for the caller's records
	return sent.ID, nil
}
