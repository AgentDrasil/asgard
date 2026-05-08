# Telegram Setup

## Create a Bot

1. Open Telegram and search for [@BotFather](https://telegram.me/BotFather)
2. Send `/newbot` to create a new bot
3. Follow the prompts to set a name and username
4. Copy the bot token provided

## Configuration

Add the following to your `config.yaml`:

```yaml
telegram:
  bot_token: "your-bot-token-here"
  # allowed_senders: leave empty initially, see below
```

### Getting Your User ID

1. Start Asgard with the config above
2. Send any message to your bot
3. The bot will reply with your user ID
4. Add your user ID to `allowed_senders`:

```yaml
telegram:
  bot_token: "your-bot-token-here"
  allowed_senders:
    - 123456789
```

5. Restart Asgard

## Group Setup

1. Create a new group with only yourself and the bot
2. Go to **Group Info** → **Manage Group**
3. Enable **Topics**
4. Add the bot as an **Administrator**
5. Grant only the **Manage Topics** permission (remove all other permissions for security)

## Advanced

You can add friends and other bots to the group as viewers. If you want them to control Asgard, add their user IDs to `allowed_senders`.

> **Warning**: Be cautious about who you add to `allowed_senders` — they will have full access to control Asgard.
