package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/iraosandeep/dispatch/internal/update"
)

func (a *App) updateCmd() *cobra.Command {
	var (
		check, force bool
		target       string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Upgrade dispatch to the latest release",
		Long: `Download the latest release from GitHub, verify its SHA-256 checksum, and
replace this binary in place. Your configuration is not touched.`,
		Example: `  dispatch update            # upgrade to the latest release
  dispatch update --check    # only report whether an update is available
  dispatch update --version v0.2.0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()

			current := a.Build.Version
			tag := target
			if tag == "" {
				latest, err := a.Updater.Latest(ctx)
				if errors.Is(err, update.ErrNoRelease) {
					return fmt.Errorf("no release of dispatch has been published yet")
				}
				if err != nil {
					return err
				}
				tag = latest
			} else if !update.IsVersion(tag) {
				return usageErrorf("--version must look like v1.2.3")
			}
			if tag[0] != 'v' {
				tag = "v" + tag
			}

			fmt.Fprintf(a.Stdout, "Current version: %s\n", current)
			fmt.Fprintf(a.Stdout, "Latest version:  %s\n", tag)

			devBuild := !update.IsVersion(current)
			switch {
			case target == "" && !devBuild && update.Compare(current, tag) >= 0:
				fmt.Fprintln(a.Stdout, "dispatch is up to date.")
				return nil
			case check:
				if devBuild {
					fmt.Fprintln(a.Stdout, "This is a development build; `dispatch update --force` replaces it with the latest release.")
				} else {
					fmt.Fprintln(a.Stdout, "An update is available. Run: dispatch update")
				}
				return nil
			case devBuild && !force:
				return usageErrorf("this is a development build (%s); run `dispatch update --force` to replace it with %s", current, tag)
			}

			exe, err := a.Executable()
			if err != nil {
				return fmt.Errorf("locate dispatch binary: %w", err)
			}
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}
			if mgr := update.ManagedBy(exe); mgr != "" {
				return fmt.Errorf("dispatch was installed with %s", mgr)
			}

			fmt.Fprintf(a.Stdout, "Downloading %s...\n", a.Updater.ArchiveName(tag))
			bin, err := a.Updater.Download(ctx, tag)
			if err != nil {
				return err
			}
			if err := update.Install(exe, bin); err != nil {
				if errors.Is(err, os.ErrPermission) {
					return fmt.Errorf("no permission to replace %s; re-run with sudo, or reinstall with the install script", exe)
				}
				return fmt.Errorf("install update: %w", err)
			}
			fmt.Fprintf(a.Stdout, "Updated %s to %s (checksum verified).\n", exe, tag)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&check, "check", false, "only check whether an update is available")
	f.BoolVar(&force, "force", false, "replace development builds")
	f.StringVar(&target, "version", "", "install a specific version, e.g. v0.2.0 (allows downgrades)")
	return cmd
}
