package aiagents

type AgentConfig struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	CLI         string   `yaml:"cli"`
	Args        []string `yaml:"args"`
	RunDirs     []string `yaml:"run_dirs"`
	AllowDirs   []string `yaml:"allow_dirs"`
}
