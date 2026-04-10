// Command mc-imagegen renders a Minecraft server Dockerfile (and optional
// build context tarball) from CLI flags. It is a thin wrapper around
// pkg/mcimage so the image generator can be used independently of the
// GitOps daemon.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/devvelvet/mc-operator/pkg/mcimage"
	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mc-imagegen",
		Short: "Generate Dockerfiles and build contexts for Minecraft servers",
	}
	cmd.AddCommand(renderCmd())
	return cmd
}

func renderCmd() *cobra.Command {
	var (
		serverType string
		version    string
		memory     int
		serverJAR  string
		plugins    []string
		outFile    string
	)
	c := &cobra.Command{
		Use:   "render",
		Short: "Render a Dockerfile to stdout or a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := mctypes.ServerType(strings.ToLower(serverType))
			spec := mcimage.BuildSpec{
				Type:       st,
				Version:    version,
				MemoryMB:   memory,
				ServerJAR:  serverJAR,
				PluginJARs: plugins,
			}
			df, err := mcimage.RenderDockerfile(spec)
			if err != nil {
				return err
			}
			if outFile == "" || outFile == "-" {
				os.Stdout.Write(df)
				return nil
			}
			return os.WriteFile(outFile, df, 0o644)
		},
	}
	c.Flags().StringVar(&serverType, "type", "paper", "server type (paper|spigot|vanilla|fabric|forge|velocity)")
	c.Flags().StringVar(&version, "version", "", "minecraft version (e.g. 1.20.4)")
	c.Flags().IntVar(&memory, "memory", 2048, "memory limit in MB")
	c.Flags().StringVar(&serverJAR, "jar", "server.jar", "path to the server jar in the build context")
	c.Flags().StringSliceVar(&plugins, "plugin", nil, "plugin jar path (repeat for multiple)")
	c.Flags().StringVarP(&outFile, "output", "o", "-", "output file (default stdout)")
	_ = c.MarkFlagRequired("version")
	return c
}
