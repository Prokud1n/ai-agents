package config

type S3 struct {
	Endpoint  string `env:"S3_ENDPOINT"   envDefault:"localhost:9000"`
	AccessKey string `env:"S3_ACCESS_KEY"`
	SecretKey string `env:"S3_SECRET_KEY"`
	Bucket    string `env:"S3_BUCKET"     envDefault:"media"`
	Region    string `env:"S3_REGION"     envDefault:"us-east-1"`
	UseSSL    bool   `env:"S3_USE_SSL"    envDefault:"false"`
}
