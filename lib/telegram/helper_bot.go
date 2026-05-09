package telegram

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/rs/zerolog/log"
)

func StartHelper(ctx context.Context, token string) error {
	log.Info().Msg("Start helper bot")

	b, err := bot.New(token, bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil {
			return
		}
		if update.Message.From == nil {
			return
		}
		if update.Message.Text != "" {
			log.Info().Str("username", update.Message.From.Username).Int64("id", update.Message.From.ID).Msg("Receive message from")
			chatID := update.Message.Chat.ID
			text := fmt.Sprintf("Your User ID: %d", update.Message.From.ID)
			_, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          chatID,
				MessageThreadID: update.Message.MessageThreadID,
				Text:            text,
			})
			if err != nil {
				log.Error().Err(err).Msg("Failed to send message")
			}
		}
	}))
	if err != nil {
		return err
	}
	b.Start(ctx)
	return nil
}
