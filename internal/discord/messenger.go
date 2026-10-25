// Package discord is the only place that talks to the discordgo library.
// If the library changes, this is the package to fix.
package discord

import "github.com/bwmarrin/discordgo"

// SessionMessenger lets the ops package send and edit messages without knowing about discordgo.
type SessionMessenger struct {
	S *discordgo.Session
}

func (m SessionMessenger) Send(channelID, content string) (string, error) {
	sent, err := m.S.ChannelMessageSend(channelID, content)
	if err != nil {
		return "", err
	}
	return sent.ID, nil
}

func (m SessionMessenger) Edit(channelID, messageID, content string) error {
	_, err := m.S.ChannelMessageEdit(channelID, messageID, content)
	return err
}
