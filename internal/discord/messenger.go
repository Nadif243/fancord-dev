// Package discord is the only place that talks to the discordgo library.
// If the library changes, this is the package to fix.
package discord

import (
	"github.com/bwmarrin/discordgo"

	"github.com/Nadif243/fancord-bot/internal/outside"
)

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
