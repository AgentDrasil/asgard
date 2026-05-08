package telegram

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func StartHelper(ctx context.Context, token string) error {
	fmt.Println("Start helper bot")

	b, err := bot.New(token, bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil {
			return
		}
		if update.Message.From == nil {
			return
		}
		if update.Message.Text != "" {
			fmt.Printf("Receive message from username=%s, id=%d\n", update.Message.From.Username, update.Message.From.ID)
			chatID := update.Message.Chat.ID
			text := fmt.Sprintf("Your User ID: %d", update.Message.From.ID)
			_, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          chatID,
				MessageThreadID: update.Message.MessageThreadID,
				Text:            text,
			})
			if err != nil {
				fmt.Printf("Failed to send message: %v\n", err)
			}
		}
	}))
	if err != nil {
		return err
	}
	b.Start(ctx)
	return nil
}
