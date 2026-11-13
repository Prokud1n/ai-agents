package config

type OpenAI struct {
	APIKey  string `env:"OPENAI_API_KEY"`
	BaseURL string `env:"OPENAI_BASE_URL" envDefault:"https://api.openai.com/v1"`
}
