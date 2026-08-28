package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/SleepyMario/streamchat/internal/chat"
	"github.com/SleepyMario/streamchat/internal/clientruntime"
	"github.com/SleepyMario/streamchat/internal/config"
	"github.com/SleepyMario/streamchat/internal/gui"
	"github.com/SleepyMario/streamchat/internal/launcher"
	"github.com/SleepyMario/streamchat/internal/relay"
)

var version = "development"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "streamchat-gui:", err)
		os.Exit(2)
	}
}
func run() error {
	configPath := flag.String("config", "", "Streamchat JSON configuration file")
	listen := flag.String("listen", "127.0.0.1:8792", "GUI HTTP listen address")
	noOpen := flag.Bool("no-open", false, "do not open the GUI in the default browser")
	demo := flag.Bool("demo", false, "show an offline visual demonstration")
	connection := flag.String("connection", "auto", "connection mode: auto, local, or remote")
	serverURL := flag.String("server-url", "", "remote Streamchat relay WebSocket URL")
	showVersion := flag.Bool("version", false, "show version")
	flag.Parse()
	if *showVersion {
		fmt.Println("streamchat-gui", version)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var runtime *clientruntime.Runtime
	var cfg config.Config
	var err error
	var localServer *localServerProcess
	if !*demo {
		cfg, err = config.Load(*configPath)
		if err != nil {
			return err
		}
		config.ApplyEnv(&cfg, os.Getenv)
		mode := *connection
		if mode == "auto" {
			if cfg.Client.ServerURL == "" {
				mode = "local"
			} else {
				mode = "remote"
			}
		}
		switch mode {
		case "local":
			localServer, err = startLocalServer(ctx, &cfg)
		case "remote":
			if *serverURL != "" {
				cfg.Client.ServerURL = *serverURL
			}
			if cfg.Client.ServerURL == "" {
				err = fmt.Errorf("remote connection mode requires a server URL")
			}
		default:
			err = fmt.Errorf("invalid connection mode %q", mode)
		}
		if err != nil {
			return err
		}
		if localServer != nil {
			defer stopLocalServer(localServer)
		}
		runtime, err = clientruntime.New(ctx, cfg)
		if err != nil {
			return err
		}
	} else {
		cfg = config.Defaults()
		cfg.Path = "demo"
		runtime, err = clientruntime.New(ctx, cfg)
		if err != nil {
			return err
		}
		runtime.SetRelayState("demo")
	}
	server, err := gui.New(gui.Config{Listen: *listen, Password: os.Getenv("STREAMCHAT_GUI_PASSWORD"), Runtime: runtime, Shutdown: stop})
	if err != nil {
		return err
	}
	if *demo {
		go publishDemo(ctx, server)
	} else {
		if cfg.Client.ServerURL != "" && cfg.RelayAuthToken != "" {
			messages := make(chan chat.Message, cfg.QueueSize)
			client := relay.NewClient(cfg.Client.ServerURL, cfg.RelayAuthToken)
			client.OnState = runtime.SetRelayState
			go func() {
				if err := client.Run(ctx, messages); err != nil {
					runtime.SetRelayState("failed: " + err.Error())
				}
			}()
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case message, open := <-messages:
						if !open {
							return
						}
						_ = server.Publish(message)
					}
				}
			}()
		} else {
			runtime.SetRelayState("not configured")
		}
	}
	fmt.Printf("Streamchat GUI %s listening at %s\n", version, server.URL())
	if !*noOpen {
		_ = launcher.New().Open(server.URL())
	}
	return server.Run(ctx)
}

type localServerProcess struct {
	command     *exec.Cmd
	shutdownURL string
	token       string
	done        chan struct{}
}

func startLocalServer(ctx context.Context, cfg *config.Config) (*localServerProcess, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("generate local relay token: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve local relay port: %w", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	dataDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate Streamchat data directory: %w", err)
	}
	dataDir = filepath.Join(dataDir, "Streamchat")
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create Streamchat data directory: %w", err)
	}

	temporaryDir, err := os.MkdirTemp("", "streamchat-local-server-")
	if err != nil {
		return nil, err
	}
	serverConfig := *cfg
	serverConfig.Server.Listen = address
	serverConfig.Server.WebSocketPath = "/relay"
	serverConfig.Client.ServerURL = ""
	serverConfig.RelayAuthToken = hex.EncodeToString(token)
	serverConfig.Storage.SQLitePath = filepath.Join(dataDir, "streamchat.db")
	temporaryConfig := filepath.Join(temporaryDir, "config.json")
	if err = config.Save(temporaryConfig, serverConfig); err != nil {
		_ = os.RemoveAll(temporaryDir)
		return nil, err
	}

	executable, err := os.Executable()
	if err != nil {
		_ = os.RemoveAll(temporaryDir)
		return nil, err
	}
	core := filepath.Join(filepath.Dir(executable), "streamchat-core")
	if _, statErr := os.Stat(core); statErr != nil {
		if _, windowsErr := os.Stat(core + ".exe"); windowsErr == nil {
			core += ".exe"
		} else {
			fallback := filepath.Join(filepath.Dir(executable), "streamchat")
			if _, fallbackErr := os.Stat(fallback); fallbackErr != nil {
				_ = os.RemoveAll(temporaryDir)
				return nil, fmt.Errorf("streamchat-core was not found beside the GUI runtime")
			}
			core = fallback
		}
	}
	command := exec.Command(core, "serve", "--config", temporaryConfig)
	command.Env = append(os.Environ(), "STREAMCHAT_LOCAL_SERVER=1")
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if err = command.Start(); err != nil {
		_ = os.RemoveAll(temporaryDir)
		return nil, fmt.Errorf("start local Streamchat server: %w", err)
	}
	process := &localServerProcess{
		command:     command,
		shutdownURL: "http://" + address + "/_streamchat/local-shutdown",
		token:       serverConfig.RelayAuthToken,
		done:        make(chan struct{}),
	}
	go func() {
		_ = command.Wait()
		_ = os.RemoveAll(temporaryDir)
		close(process.done)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", address, 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			cfg.Client.ServerURL = "ws://" + address + "/relay"
			cfg.RelayAuthToken = serverConfig.RelayAuthToken
			cfg.Storage.SQLitePath = serverConfig.Storage.SQLitePath
			return process, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = command.Process.Kill()
	return nil, fmt.Errorf("local Streamchat server did not become ready")
}

func stopLocalServer(process *localServerProcess) {
	if process == nil || process.command == nil || process.command.Process == nil {
		return
	}
	request, err := http.NewRequest(http.MethodPost, process.shutdownURL, nil)
	if err == nil {
		request.Header.Set("Authorization", "Bearer "+process.token)
		client := &http.Client{Timeout: 2 * time.Second}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
		}
	}
	select {
	case <-process.done:
		return
	case <-time.After(3 * time.Second):
		_ = process.command.Process.Kill()
		<-process.done
	}
}

func publishDemo(ctx context.Context, server *gui.Server) {
	timer := time.NewTimer(600 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	now := time.Now()
	samples := []chat.Message{
		{ID: "demo-kick", Platform: chat.PlatformKick, Timestamp: now.Add(-2 * time.Minute), AuthorDisplayName: "sleepymario", AuthorColor: "#53fc18", Roles: chat.NewRoleSet(chat.RoleBroadcaster), Text: "Welcome — everything is connected.", EventType: chat.EventMessage},
		{ID: "demo-twitch", Platform: chat.PlatformTwitch, Timestamp: now.Add(-time.Minute), AuthorDisplayName: "nightowl", AuthorColor: "#a970ff", Roles: chat.NewRoleSet(chat.RoleSubscriber), Text: "The new unified chat looks sharp Kappa", Emotes: []chat.Emote{{ID: "25", Name: "Kappa", URL: "https://static-cdn.jtvnw.net/emoticons/v2/25/static/dark/3.0"}}, EventType: chat.EventMessage},
		{ID: "demo-youtube", Platform: chat.PlatformYouTube, Timestamp: now, AuthorDisplayName: "Mario Fan", Roles: chat.NewRoleSet(chat.RoleModerator), Text: "Ready for the next stream!", Paid: &chat.Paid{Display: "NT$150"}, EventType: chat.EventPaid},
	}
	for _, message := range samples {
		_ = server.Publish(message)
	}
}
