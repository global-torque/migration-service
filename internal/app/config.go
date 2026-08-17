package app

type Config struct {
	Dir string `required:"true"`
}

type GeneralConfig struct {
	EnvName string `required:"true" split_words:"true"`
}
