package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/tgruben-circuit/percy/claudetool"
	memtool "github.com/tgruben-circuit/percy/claudetool/memory"
	"github.com/tgruben-circuit/percy/db"
	"github.com/tgruben-circuit/percy/memory"
	"github.com/tgruben-circuit/percy/memory/muninn"
	"github.com/tgruben-circuit/percy/models"
	"github.com/tgruben-circuit/percy/server"
	_ "github.com/tgruben-circuit/percy/server/notifications/channels" // register channel types
	"github.com/tgruben-circuit/percy/templates"
	"github.com/tgruben-circuit/percy/tui"
	"github.com/tgruben-circuit/percy/version"
)

type GlobalConfig struct {
	DBPath          string
	Debug           bool
	Model           string
	PredictableOnly bool
	ConfigPath      string
	TerminalURL     string
	DefaultModel    string
	DefaultModelSet bool
}

func main() {
	// Define global flags
	var global GlobalConfig
	defaultModelID := models.Default().ID
	flag.StringVar(&global.DBPath, "db", "percy.db", "Path to SQLite database file")
	flag.BoolVar(&global.Debug, "debug", false, "Enable debug logging")
	flag.StringVar(&global.Model, "model", defaultModelID, "LLM model to use (use 'predictable' for testing)")
	flag.BoolVar(&global.PredictableOnly, "predictable-only", false, "Use only the predictable service, ignoring all other models")
	flag.StringVar(&global.ConfigPath, "config", "", "Path to percy.json configuration file (optional)")
	flag.StringVar(&global.DefaultModel, "default-model", defaultModelID, "Default model for web UI")

	// Custom usage function
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [global-flags] <command> [command-flags]\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "Global flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nCommands:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  serve [flags]                 Start the web server\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  tui [flags]                   Start the terminal UI client\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  unpack-template <name> <dir>  Unpack a project template to a directory\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  version                       Print version information as JSON\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\nUse '%s <command> -h' for command-specific help\n", os.Args[0])
	}

	// Parse all flags first
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "default-model" {
			global.DefaultModelSet = true
		}
	})
	args := flag.Args()

	if len(args) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	command := args[0]
	switch command {
	case "serve":
		runServe(global, args[1:])
	case "tui":
		runTUI(args[1:])
	case "unpack-template":
		runUnpackTemplate(args[1:])
	case "version":
		runVersion()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		flag.Usage()
		os.Exit(1)
	}
}

func runServe(global GlobalConfig, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.String("port", "9000", "Port to listen on")
	systemdActivation := fs.Bool("systemd-activation", false, "Use systemd socket activation (listen on fd from systemd)")
	requireHeader := fs.String("require-header", "", "Require this header on all API requests (e.g., X-Exedev-Userid)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing serve flags: %v\n", err)
		os.Exit(1)
	}

	logger := setupLogging(global.Debug)

	database := setupDatabase(global.DBPath, logger)
	defer database.Close()

	// Open memory database (non-fatal if it fails)
	var memoryDB *memory.DB
	memDBPath := memory.MemoryDBPath(global.DBPath)
	memoryDB, err := memory.Open(memDBPath)
	if err != nil {
		logger.Warn("Failed to open memory database", "error", err)
	} else {
		defer memoryDB.Close()
		logger.Info("Opened memory database", "path", memDBPath)
	}

	// Set the database path for system prompt generation
	server.DBPath = global.DBPath

	// Build LLM configuration
	llmConfig := buildLLMConfig(logger, global.ConfigPath, global.TerminalURL, global.DefaultModel, global.DefaultModelSet, database)

	// Initialize LLM service manager (includes custom model support via database)
	llmManager := server.NewLLMServiceManager(llmConfig)

	// Log available models
	availableModels := llmManager.GetAvailableModels()
	logger.Info("Available models", "models", strings.Join(availableModels, ", "))

	toolSetConfig := setupToolSetConfig(llmManager)
	toolSetConfig.TodoVerifierModel = llmConfig.TodoVerifierModel

	// Create embedder if configured
	var embedder memory.Embedder
	switch provider := os.Getenv("PERCY_EMBED_PROVIDER"); provider {
	case "ollama":
		embedURL := os.Getenv("PERCY_EMBED_URL")
		if embedURL == "" {
			embedURL = "http://localhost:11434"
		}
		embedModel := os.Getenv("PERCY_EMBED_MODEL")
		if embedModel == "" {
			embedModel = "nomic-embed-text"
		}
		embedder = memory.NewOllamaEmbedder(embedURL, embedModel)
		logger.Info("Embedder configured", "provider", "ollama", "url", embedURL, "model", embedModel)
	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			logger.Warn("PERCY_EMBED_PROVIDER=openai but OPENAI_API_KEY is not set")
		} else {
			embedder = memory.NewOpenAIEmbedder(apiKey)
			logger.Info("Embedder configured", "provider", "openai", "model", "text-embedding-3-small")
		}
	case "":
		// Auto-detect: use OpenAI if an API key is available
		if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
			embedder = memory.NewOpenAIEmbedder(apiKey)
			logger.Info("Embedder configured", "provider", "openai (auto-detected)", "model", "text-embedding-3-small")
		}
	}

	// MuninnDB augmentation (optional)
	var muninnClient *muninn.Client
	if muninnURL := os.Getenv("PERCY_MUNINN_URL"); muninnURL != "" {
		vault := os.Getenv("PERCY_MUNINN_VAULT")
		if vault == "" {
			vault = "percy"
		}
		token := os.Getenv("PERCY_MUNINN_TOKEN")
		muninnClient = muninn.NewClient(muninnURL, vault, token)
		if !muninnClient.Healthy(context.Background()) {
			logger.Warn("MuninnDB unreachable, running without augmentation", "url", muninnURL)
			muninnClient = nil
		} else {
			logger.Info("MuninnDB connected", "url", muninnURL, "vault", vault)
		}
	}

	// Wire up memory search tool if memory DB is available
	if memoryDB != nil {
		var src *muninn.Source
		if muninnClient != nil {
			src = muninn.NewSource(muninnClient)
		}
		toolSetConfig.MemorySearchTool = memtool.NewMemorySearchTool(memoryDB, embedder, src).Tool()
	}

	// Create server
	svr := server.NewServer(database, llmManager, toolSetConfig, logger, global.PredictableOnly, llmConfig.TerminalURL, llmConfig.DefaultModel, *requireHeader, llmConfig.Links)

	// Pass memory DB and embedder to server for post-conversation indexing
	svr.SetMemoryDB(memoryDB)
	svr.SetEmbedder(embedder)

	// Set MuninnDB sink if client is available
	if muninnClient != nil {
		svr.SetMuninnSink(muninn.NewSink(muninnClient))
	}

	// Seed notification channels from config file if DB is empty (one-time migration)
	svr.SeedNotificationChannelsFromConfig(llmConfig.NotificationChannels)
	// Load notification channels from DB
	svr.ReloadNotificationChannels()

	if *systemdActivation {
		listener, listenerErr := systemdListener()
		if listenerErr != nil {
			logger.Error("Failed to get systemd listener", "error", listenerErr)
			os.Exit(1)
		}
		logger.Info("Using systemd socket activation")
		err = svr.StartWithListener(listener)
	} else {
		err = svr.Start(*port)
	}

	if err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

func setupLogging(debug bool) *slog.Logger {
	logLevel := slog.LevelInfo
	if debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)
	return logger
}

func setupDatabase(dbPath string, logger *slog.Logger) *db.DB {
	database, err := db.New(db.Config{DSN: dbPath})
	if err != nil {
		logger.Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}

	// Run database migrations
	if err := database.Migrate(context.Background()); err != nil {
		logger.Error("Failed to run database migrations", "error", err)
		os.Exit(1)
	}
	logger.Debug("Database migrations completed successfully")
	return database
}

// runUnpackTemplate unpacks a project template to a directory
func runUnpackTemplate(args []string) {
	fs := flag.NewFlagSet("unpack-template", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: percy unpack-template <template-name> <directory>\n\n")
		fmt.Fprintf(fs.Output(), "Unpacks a project template to the specified directory.\n\n")
		fmt.Fprintf(fs.Output(), "Available templates:\n")
		names, err := templates.List()
		if err != nil {
			fmt.Fprintf(fs.Output(), "  (error listing templates: %v)\n", err)
		} else if len(names) == 0 {
			fmt.Fprintf(fs.Output(), "  (no templates available)\n")
		} else {
			for _, name := range names {
				fmt.Fprintf(fs.Output(), "  %s\n", name)
			}
		}
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing unpack-template flags: %v\n", err)
		os.Exit(1)
	}

	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}

	templateName := fs.Arg(0)
	destDir := fs.Arg(1)

	// Verify template exists
	names, err := templates.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing templates: %v\n", err)
		os.Exit(1)
	}
	found := false
	for _, name := range names {
		if name == templateName {
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "Error: template %q not found\n", templateName)
		fmt.Fprintf(os.Stderr, "Available templates: %s\n", strings.Join(names, ", "))
		os.Exit(1)
	}

	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %q: %v\n", destDir, err)
		os.Exit(1)
	}

	// Unpack the template
	if err := templates.Unpack(templateName, destDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error unpacking template: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Template %q unpacked to %s\n", templateName, destDir)
}

// runTUI starts the terminal UI client.
func runTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	server := fs.String("server", "http://localhost:9000", "Percy server URL")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing tui flags: %v\n", err)
		os.Exit(1)
	}
	if err := tui.Run(*server); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

// runVersion prints version information as JSON
func runVersion() {
	info := version.GetInfo()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding version: %v\n", err)
		os.Exit(1)
	}
}

func setupToolSetConfig(llmProvider claudetool.LLMServiceProvider) claudetool.ToolSetConfig {
	wd, err := os.Getwd()
	if err != nil {
		// Fallback to "/" if we can't get working directory
		wd = "/"
	}
	return claudetool.ToolSetConfig{
		WorkingDir:             wd,
		LLMProvider:            llmProvider,
		EnableJITInstall:       claudetool.EnableBashToolJITInstall,
		EnableBrowser:          true,
		EnableCodeIntelligence: true,
	}
}

// buildLLMConfig constructs LLMConfig from environment variables and optional config file
func buildLLMConfig(logger *slog.Logger, configPath, terminalURL, defaultModel string, defaultModelSet bool, database *db.DB) *server.LLMConfig {
	// Ollama URL: env var > default
	ollamaURL := os.Getenv("OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}

	llmCfg := &server.LLMConfig{
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),
		FireworksAPIKey: os.Getenv("FIREWORKS_API_KEY"),
		OllamaURL:       ollamaURL,
		TerminalURL:     terminalURL,
		DefaultModel:    defaultModel,
		DB:              database,
		Logger:          logger,
	}

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			if !os.IsNotExist(err) {
				logger.Warn("Failed to read config file", "path", configPath, "error", err)
			}
			return llmCfg
		}

		var cfg struct {
			LLMGateway           string           `json:"llm_gateway"`
			TerminalURL          string           `json:"terminal_url"`
			DefaultModel         string           `json:"default_model"`
			TodoVerifierModel    string           `json:"todo_verifier_model"`
			Links                []server.Link    `json:"links"`
			NotificationChannels []map[string]any `json:"notification_channels"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			logger.Warn("Failed to parse config file", "path", configPath, "error", err)
			return llmCfg
		}

		if cfg.LLMGateway != "" {
			gateway := strings.TrimSuffix(cfg.LLMGateway, "/")
			llmCfg.Gateway = gateway
			logger.Info("Using LLM gateway", "gateway", gateway)

			// When using a gateway, default all API keys to "implicit" unless otherwise set
			if llmCfg.AnthropicAPIKey == "" {
				llmCfg.AnthropicAPIKey = "implicit"
			}
			if llmCfg.OpenAIAPIKey == "" {
				llmCfg.OpenAIAPIKey = "implicit"
			}
			if llmCfg.GeminiAPIKey == "" {
				llmCfg.GeminiAPIKey = "implicit"
			}
			if llmCfg.FireworksAPIKey == "" {
				llmCfg.FireworksAPIKey = "implicit"
			}
		}

		// Override terminal URL from config file if present and not already set via flag
		if cfg.TerminalURL != "" && llmCfg.TerminalURL == "" {
			llmCfg.TerminalURL = cfg.TerminalURL
			logger.Info("Using terminal URL from config", "url", cfg.TerminalURL)
		}

		// Override default model from config file if present and not already set via flag
		if cfg.DefaultModel != "" && !defaultModelSet {
			llmCfg.DefaultModel = cfg.DefaultModel
			logger.Info("Using default model from config", "model", cfg.DefaultModel)
		}

		if cfg.TodoVerifierModel != "" {
			llmCfg.TodoVerifierModel = cfg.TodoVerifierModel
			logger.Info("Using todo verifier model from config", "model", cfg.TodoVerifierModel)
		}

		// Load links from config file if present
		if len(cfg.Links) > 0 {
			llmCfg.Links = cfg.Links
			logger.Info("Loaded links from config", "count", len(cfg.Links))
		}

		if len(cfg.NotificationChannels) > 0 {
			llmCfg.NotificationChannels = cfg.NotificationChannels
			logger.Info("Notification channels configured", "count", len(cfg.NotificationChannels))
		}
	}

	return llmCfg
}

// systemdListener returns a net.Listener from systemd socket activation.
// Systemd passes file descriptors starting at fd 3, with LISTEN_FDS indicating the count.
func systemdListener() (net.Listener, error) {
	// Check LISTEN_PID matches our PID (optional but recommended)
	pidStr := os.Getenv("LISTEN_PID")
	if pidStr != "" {
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			return nil, fmt.Errorf("invalid LISTEN_PID: %w", err)
		}
		if pid != os.Getpid() {
			return nil, fmt.Errorf("LISTEN_PID %d does not match current PID %d", pid, os.Getpid())
		}
	}

	// Get the number of file descriptors passed
	fdsStr := os.Getenv("LISTEN_FDS")
	if fdsStr == "" {
		return nil, fmt.Errorf("LISTEN_FDS not set; not running under systemd socket activation")
	}
	nfds, err := strconv.Atoi(fdsStr)
	if err != nil {
		return nil, fmt.Errorf("invalid LISTEN_FDS: %w", err)
	}
	if nfds < 1 {
		return nil, fmt.Errorf("LISTEN_FDS=%d; expected at least 1", nfds)
	}

	// Systemd passes file descriptors starting at fd 3
	const listenFDsStart = 3
	fd := listenFDsStart

	// Create a file from the descriptor
	f := os.NewFile(uintptr(fd), "systemd-socket")
	if f == nil {
		return nil, fmt.Errorf("failed to create file from fd %d", fd)
	}

	// Create a listener from the file
	listener, err := net.FileListener(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to create listener from fd %d: %w", fd, err)
	}

	// Close the original file; the listener now owns the descriptor
	f.Close()

	return listener, nil
}
