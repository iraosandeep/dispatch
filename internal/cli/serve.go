package cli

import (
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/iraosandeep/dispatch/internal/config"
	"github.com/iraosandeep/dispatch/internal/server"
)

func (a *App) serveCmd() *cobra.Command {
	var (
		addr, tlsCert, tlsKey, logFormat string
		allowRemote                      bool
		rateLimit                        float64
		timeout                          time.Duration
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the authenticated HTTP notification API",
		Long: `Run an HTTP server that accepts authenticated notification requests.

Endpoints:
  POST /notify    send a notification (requires Authorization: Bearer <API key>)
  GET  /healthz   process is alive
  GET  /readyz    configuration is present (does not contact Telegram)

The server binds to 127.0.0.1:8787 by default. Listening on any other
interface requires --allow-remote; put it behind a VPN, private network, or
an authenticated TLS-terminating proxy, or pass --tls-cert/--tls-key.

API keys come from DISPATCH_API_KEY (comma-separated for rotation) or the
config file. They are never accepted as flags.`,
		Example: `  DISPATCH_API_KEY=$(dispatch config gen-key) dispatch serve
  dispatch serve --addr 127.0.0.1:9000
  dispatch serve --addr 0.0.0.0:8787 --allow-remote --tls-cert cert.pem --tls-key key.pem`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if logFormat != "text" && logFormat != "json" {
				return usageErrorf("--log-format must be text or json")
			}

			var o config.Overrides
			fl := cmd.Flags()
			if fl.Changed("addr") {
				o.Addr = &addr
			}
			if fl.Changed("allow-remote") {
				o.AllowRemote = &allowRemote
			}
			if fl.Changed("tls-cert") {
				o.TLSCert = &tlsCert
			}
			if fl.Changed("tls-key") {
				o.TLSKey = &tlsKey
			}
			if fl.Changed("rate-limit") {
				o.RateLimit = &rateLimit
			}
			if fl.Changed("timeout") {
				if timeout <= 0 {
					return usageErrorf("--timeout must be positive")
				}
				o.Timeout = &timeout
			}

			cfg, err := a.loadConfig(o)
			if err != nil {
				return err
			}
			if err := cfg.ValidateServer(); err != nil {
				return configError(err)
			}

			level := slog.LevelInfo
			if a.verbose {
				level = slog.LevelDebug
			}
			logger := newLogger(a.Stderr, level, logFormat)

			svc, _, err := a.buildService(cfg, logger)
			if err != nil {
				return err
			}

			if !config.IsLoopback(cfg.Addr) {
				logger.Warn("listening on a non-loopback address; restrict access with a firewall, VPN, or authenticated proxy",
					slog.String("addr", cfg.Addr))
				if cfg.TLSCert == "" {
					logger.Warn("TLS is disabled; API keys and messages travel in plain text unless a TLS-terminating proxy is in front")
				}
			}

			srv, err := server.New(server.Options{
				Addr:            cfg.Addr,
				APIKeys:         cfg.APIKeys,
				Service:         svc,
				Ready:           cfg.ValidateTelegram,
				Logger:          logger,
				RateLimit:       cfg.RateLimit,
				DeliveryTimeout: cfg.Timeout.Std(),
				TLSCert:         cfg.TLSCert,
				TLSKey:          cfg.TLSKey,
				Version:         a.Build.Version,
			})
			if err != nil {
				return configError(err)
			}
			return srv.ListenAndServe(cmd.Context())
		},
	}

	f := cmd.Flags()
	f.StringVar(&addr, "addr", config.DefaultAddr, "listen address (host:port)")
	f.BoolVar(&allowRemote, "allow-remote", false, "permit listening on non-loopback addresses")
	f.StringVar(&tlsCert, "tls-cert", "", "TLS certificate file (enables HTTPS)")
	f.StringVar(&tlsKey, "tls-key", "", "TLS private key file")
	f.Float64Var(&rateLimit, "rate-limit", config.DefaultRateLimit, "max /notify requests per second (0 disables)")
	f.DurationVar(&timeout, "timeout", config.DefaultTimeout, "max time per delivery, including retries")
	f.StringVar(&logFormat, "log-format", "text", "log format: text or json")
	return cmd
}
